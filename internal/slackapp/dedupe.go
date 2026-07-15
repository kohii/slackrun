// Package slackapp wires up the Socket Mode listener and owns the small
// helpers (progress, reply, dedupe) that talk to the Slack Web API.
package slackapp

import (
	"strconv"
	"sync"
	"time"
)

// DedupeDecision classifies the verdict for one (channel, ts) pair.
type DedupeDecision int

const (
	DedupeAccept DedupeDecision = iota
	DedupeDuplicate
	DedupeTooOld
)

// DedupeOptions configures TTL and the boot-time replay cutoff.
type DedupeOptions struct {
	TTL              time.Duration
	BootTime         time.Time
	MinAgeFromBoot   time.Duration
	// Now is injected by tests. Defaults to time.Now.
	Now func() time.Time
}

// Dedupe rejects (channel, ts) pairs we have already handled, and (for live
// deliveries only) drops events older than `bootTime - minAgeFromBoot`. The
// latter is the Slack-replay defense for fresh starts: Socket Mode may replay
// recent events, but we are not interested in stale alerts at startup.
// Intentional catchup paths (see DecideCatchup) bypass the age cutoff.
//
// Concurrent-safe.
type Dedupe struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	opts   DedupeOptions
}

// NewDedupe constructs a Dedupe with sensible defaults applied.
func NewDedupe(opts DedupeOptions) *Dedupe {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Dedupe{
		seen: make(map[string]time.Time),
		opts: opts,
	}
}

// Decide records the (channel, ts) pair if we have not seen it within the
// TTL window. Applies the boot-time TooOld cutoff so stale replays from
// Socket Mode do not fire rules at startup. Slack timestamps are decimal
// seconds with µs precision — "1719456789.012345".
func (d *Dedupe) Decide(channel, ts string) DedupeDecision {
	return d.decide(channel, ts, true)
}

// DecideCatchup is the entry point for events pulled by an intentional
// backfill (e.g. the conversations.history poller). Skips the TooOld
// cutoff — catchup is by definition our willingness to process stale
// events — but still enforces duplicate suppression.
func (d *Dedupe) DecideCatchup(channel, ts string) DedupeDecision {
	return d.decide(channel, ts, false)
}

func (d *Dedupe) decide(channel, ts string, enforceTooOld bool) DedupeDecision {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.opts.Now()
	d.gcLocked(now)

	if enforceTooOld {
		if eventTime, ok := parseSlackTS(ts); ok {
			cutoff := d.opts.BootTime.Add(-d.opts.MinAgeFromBoot)
			if eventTime.Before(cutoff) {
				return DedupeTooOld
			}
		}
	}

	key := channel + ":" + ts
	if _, ok := d.seen[key]; ok {
		return DedupeDuplicate
	}
	d.seen[key] = now
	return DedupeAccept
}

func (d *Dedupe) gcLocked(now time.Time) {
	cutoff := now.Add(-d.opts.TTL)
	for k, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, k)
		}
	}
}

// Size returns the number of remembered keys. Diagnostics only.
func (d *Dedupe) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

func parseSlackTS(ts string) (time.Time, bool) {
	f, err := strconv.ParseFloat(ts, 64)
	if err != nil || f <= 0 {
		return time.Time{}, false
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec), true
}
