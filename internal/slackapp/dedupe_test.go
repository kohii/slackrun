package slackapp

import (
	"fmt"
	"testing"
	"time"
)

func TestDedupe_AcceptThenDuplicate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	d := NewDedupe(DedupeOptions{
		TTL:            5 * time.Minute,
		BootTime:       now,
		MinAgeFromBoot: 5 * time.Minute,
		Now:            func() time.Time { return now },
	})
	ts := timeToTS(now)
	if got := d.Decide("C01", ts); got != DedupeAccept {
		t.Fatalf("first call: %v", got)
	}
	if got := d.Decide("C01", ts); got != DedupeDuplicate {
		t.Fatalf("second call: %v", got)
	}
}

func TestDedupe_TooOldRejectedAtBoot(t *testing.T) {
	t.Parallel()
	boot := time.Now()
	d := NewDedupe(DedupeOptions{
		TTL:            5 * time.Minute,
		BootTime:       boot,
		MinAgeFromBoot: 5 * time.Minute,
		Now:            func() time.Time { return boot },
	})
	stale := boot.Add(-10 * time.Minute)
	if got := d.Decide("C01", timeToTS(stale)); got != DedupeTooOld {
		t.Fatalf("got %v", got)
	}
}

func TestDedupe_GCAfterTTL(t *testing.T) {
	t.Parallel()
	clock := time.Unix(1_700_000_000, 0)
	d := NewDedupe(DedupeOptions{
		TTL:            1 * time.Minute,
		BootTime:       clock,
		MinAgeFromBoot: time.Hour,
		Now:            func() time.Time { return clock },
	})
	ts := timeToTS(clock)
	_ = d.Decide("C01", ts)
	clock = clock.Add(2 * time.Minute)
	if got := d.Decide("C01", ts); got != DedupeAccept {
		t.Fatalf("expected accept after TTL, got %v", got)
	}
}

func timeToTS(t time.Time) string {
	sec := t.Unix()
	us := (t.UnixNano() - sec*1e9) / 1000
	return fmt.Sprintf("%d.%06d", sec, us)
}
