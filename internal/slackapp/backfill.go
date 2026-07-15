package slackapp

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/logging"
	"github.com/slack-go/slack"
)

// backfillPollLimit caps how many messages a single poll pulls. One page is
// enough for normal traffic; if we ever hit HasMore=true we log a warning
// rather than paginate — the backstop should not become the primary path.
const backfillPollLimit = 100

// historyClient is the small slice of the Slack Web API used by backfiller.
// Kept narrow so tests can inject a fake without stubbing the full client.
type historyClient interface {
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

// backfillDispatch is invoked for each history-fetched message. It is
// expected to route the message through the normal dispatch pipeline, where
// Dedupe filters anything Socket Mode already delivered.
type backfillDispatch func(ctx context.Context, msg slack.Message, channel string)

// backfiller polls conversations.history for a single channel to compensate
// for Socket Mode losses. `sinceTS` — the highest ts we have already
// processed — advances via both live Socket Mode events (Observe) and each
// successful poll, so the fetch window shrinks with each cycle.
type backfiller struct {
	channel  string
	interval time.Duration
	lookback time.Duration
	api      historyClient
	dispatch backfillDispatch
	now      func() time.Time

	mu      sync.Mutex
	sinceTS string
}

func newBackfiller(channel string, interval, lookback time.Duration, api historyClient, dispatch backfillDispatch) *backfiller {
	return &backfiller{
		channel:  channel,
		interval: interval,
		lookback: lookback,
		api:      api,
		dispatch: dispatch,
		now:      time.Now,
	}
}

// Observe records that a live event with this ts was already handled by the
// Socket Mode path, so the poller does not re-dispatch it.
func (b *backfiller) Observe(ts string) {
	if ts == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if tsGreater(ts, b.sinceTS) {
		b.sinceTS = ts
	}
}

// Run polls until ctx is cancelled. The first poll fires after one interval:
// startup does not need a special case because Socket Mode is already trying
// to (re)connect, and Dedupe drops events older than MIN_EVENT_AGE_MS_AT_BOOT.
func (b *backfiller) Run(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.pollOnce(ctx)
		}
	}
}

func (b *backfiller) pollOnce(ctx context.Context) {
	oldest := b.oldestForPoll()
	resp, err := b.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: b.channel,
		Oldest:    oldest,
		Limit:     backfillPollLimit,
	})
	if err != nil {
		logging.Warn("backfill poll failed",
			logging.F("channel", b.channel),
			logging.F("error", err))
		return
	}
	// Slack returns messages newest-first. Replay in chronological order so
	// sinceTS advances monotonically even if a downstream dispatch panics.
	replayed := 0
	for i := len(resp.Messages) - 1; i >= 0; i-- {
		m := resp.Messages[i]
		if m.Timestamp == "" || !tsGreater(m.Timestamp, oldest) {
			continue
		}
		b.dispatch(ctx, m, b.channel)
		b.Observe(m.Timestamp)
		replayed++
	}
	if replayed > 0 {
		logging.Info("backfill replayed",
			logging.F("channel", b.channel),
			logging.F("count", replayed),
			logging.F("oldest", oldest))
	}
	if resp.HasMore {
		logging.Warn("backfill has_more; older messages skipped this cycle",
			logging.F("channel", b.channel),
			logging.F("limit", backfillPollLimit))
	}
}

func (b *backfiller) oldestForPoll() string {
	b.mu.Lock()
	since := b.sinceTS
	b.mu.Unlock()
	if since != "" {
		return since
	}
	return tsFromTime(b.now().Add(-b.lookback))
}

// tsGreater reports whether Slack ts a > b. Empty is less than any real ts.
func tsGreater(a, b string) bool {
	if b == "" {
		return a != ""
	}
	if a == "" {
		return false
	}
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea != nil || eb != nil {
		return false
	}
	return fa > fb
}

// tsFromTime formats a Slack-style ts ("<sec>.<micros>") from a time.Time.
func tsFromTime(t time.Time) string {
	sec := t.Unix()
	us := (t.UnixNano() - sec*1e9) / 1000
	return fmt.Sprintf("%d.%06d", sec, us)
}

// uniqueMessageChannels returns the set of channels referenced by
// type:message rules. Preserves first-seen order so log lines are stable.
func uniqueMessageChannels(rules []config.Rule) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for i := range rules {
		r := &rules[i]
		if r.Trigger.Type != config.TriggerTypeMessage || r.Trigger.Channel == "" {
			continue
		}
		if _, ok := seen[r.Trigger.Channel]; ok {
			continue
		}
		seen[r.Trigger.Channel] = struct{}{}
		out = append(out, r.Trigger.Channel)
	}
	return out
}
