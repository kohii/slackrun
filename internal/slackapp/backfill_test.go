package slackapp

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kohii/slackrun/internal/config"
	"github.com/slack-go/slack"
)

type fakeHistory struct {
	mu    sync.Mutex
	calls []slack.GetConversationHistoryParameters
	resp  *slack.GetConversationHistoryResponse
	err   error
}

func (f *fakeHistory) GetConversationHistoryContext(_ context.Context, p *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, *p)
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func msg(ts string) slack.Message {
	m := slack.Message{}
	m.Timestamp = ts
	return m
}

func TestBackfiller_ReplaysChronologicalAndAdvancesSince(t *testing.T) {
	t.Parallel()
	api := &fakeHistory{
		resp: &slack.GetConversationHistoryResponse{
			// Slack returns newest-first.
			Messages: []slack.Message{msg("1700.000030"), msg("1700.000020"), msg("1700.000010")},
		},
	}
	var got []string
	b := newBackfiller("C1", time.Second, 10*time.Minute, api, func(_ context.Context, m slack.Message, ch string) {
		if ch != "C1" {
			t.Errorf("dispatch channel = %q; want C1", ch)
		}
		got = append(got, m.Timestamp)
	})
	// Pin `now` inside the lookback window so all three messages sit after the
	// first-poll oldest floor.
	b.now = func() time.Time { return time.Unix(1700, 0) }
	b.pollOnce(context.Background())

	want := []string{"1700.000010", "1700.000020", "1700.000030"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatched order = %v; want %v", got, want)
	}
	if len(api.calls) != 1 || api.calls[0].ChannelID != "C1" || api.calls[0].Limit != backfillPollLimit {
		t.Fatalf("unexpected call: %+v", api.calls)
	}
	// sinceTS advanced to the newest ts.
	if b.sinceTS != "1700.000030" {
		t.Fatalf("sinceTS = %q; want 1700.000030", b.sinceTS)
	}
}

func TestBackfiller_ObserveSkipsRedispatch(t *testing.T) {
	t.Parallel()
	api := &fakeHistory{
		resp: &slack.GetConversationHistoryResponse{
			Messages: []slack.Message{msg("1700.500000"), msg("1700.300000"), msg("1700.100000")},
		},
	}
	var got []string
	b := newBackfiller("C1", time.Second, 10*time.Minute, api, func(_ context.Context, m slack.Message, _ string) {
		got = append(got, m.Timestamp)
	})

	// Live Socket Mode delivered 1700.400000; anything at or below must not replay.
	b.Observe("1700.400000")
	b.pollOnce(context.Background())

	if want := []string{"1700.500000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatched = %v; want %v", got, want)
	}
	if api.calls[0].Oldest != "1700.400000" {
		t.Fatalf("Oldest = %q; want 1700.400000", api.calls[0].Oldest)
	}
}

func TestBackfiller_InitialLookbackUsedWhenNoObservation(t *testing.T) {
	t.Parallel()
	api := &fakeHistory{resp: &slack.GetConversationHistoryResponse{}}
	b := newBackfiller("C1", time.Second, 10*time.Minute, api, func(context.Context, slack.Message, string) {})
	b.now = func() time.Time { return time.Unix(1_800_000_000, 0) }

	b.pollOnce(context.Background())
	// 1_800_000_000 - 600s lookback = 1_799_999_400
	if got := api.calls[0].Oldest; got != "1799999400.000000" {
		t.Fatalf("Oldest = %q; want 1799999400.000000", got)
	}
}

func TestBackfiller_ErrorDoesNotAdvanceSince(t *testing.T) {
	t.Parallel()
	api := &fakeHistory{err: errors.New("boom")}
	b := newBackfiller("C1", time.Second, 10*time.Minute, api, func(context.Context, slack.Message, string) {
		t.Fatal("dispatch called on error")
	})
	b.Observe("1700.000000")
	b.pollOnce(context.Background())
	if b.sinceTS != "1700.000000" {
		t.Fatalf("sinceTS moved on error: %q", b.sinceTS)
	}
}

func TestUniqueMessageChannels_DedupesAndSkipsMentions(t *testing.T) {
	t.Parallel()
	rules := []config.Rule{
		{Name: "a", Trigger: config.Trigger{Type: config.TriggerTypeMessage, Channel: "C1"}},
		{Name: "b", Trigger: config.Trigger{Type: config.TriggerTypeMessage, Channel: "C2"}},
		{Name: "c", Trigger: config.Trigger{Type: config.TriggerTypeMessage, Channel: "C1"}}, // dupe
		{Name: "d", Trigger: config.Trigger{Type: config.TriggerTypeAppMention}},             // no channel
	}
	got := uniqueMessageChannels(rules)
	want := []string{"C1", "C2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %v; want %v", got, want)
	}
}
