package slackapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/slack-go/slack"
)

type fakeProgressPoster struct {
	mu      sync.Mutex
	posted  []string
	updated []string
	deleted int
	postErr error
	tsSeq   int
}

func (f *fakeProgressPoster) PostMessage(channelID string, opts ...slack.MsgOption) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postErr != nil {
		return "", "", f.postErr
	}
	f.tsSeq++
	f.posted = append(f.posted, textOf(opts))
	return channelID, fmt.Sprintf("ts-%d", f.tsSeq), nil
}

func (f *fakeProgressPoster) UpdateMessage(channelID, ts string, opts ...slack.MsgOption) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, textOf(opts))
	return channelID, ts, "", nil
}

func (f *fakeProgressPoster) DeleteMessage(channel, ts string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted++
	return channel, ts, nil
}

func textOf(opts []slack.MsgOption) string {
	_, values, err := slack.UnsafeApplyMsgOptions("token", "C1", "https://slack.com/api/", opts...)
	if err != nil {
		return ""
	}
	return values.Get("text")
}

type fakeStatusSetter struct {
	mu       sync.Mutex
	statuses []string
	err      error
}

func (f *fakeStatusSetter) SetAssistantThreadsStatus(params slack.AssistantThreadsSetStatusParameters) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.statuses = append(f.statuses, params.Status)
	return nil
}

func TestStartMessageProgress_UpdateRewritesPlaceholderOnce(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	h, err := StartMessageProgress(context.Background(), post, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartMessageProgress: %v", err)
	}
	if h.Channel() != "C1" {
		t.Errorf("Channel() = %q, want C1", h.Channel())
	}
	if len(post.posted) != 1 || post.posted[0] != "⏳ Working…" {
		t.Fatalf("posted=%v", post.posted)
	}

	if err := h.Update("done"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := h.Update("done again"); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if len(post.updated) != 1 || post.updated[0] != "done" {
		t.Fatalf("updated=%v, want exactly one call with %q", post.updated, "done")
	}
}

func TestStartMessageProgress_DoneOverwritesWithDoneMarker(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	h, err := StartMessageProgress(context.Background(), post, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartMessageProgress: %v", err)
	}
	if err := h.Done(); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if len(post.updated) != 1 || post.updated[0] != "✅ Done" {
		t.Fatalf("updated=%v, want a single \"✅ Done\" rewrite", post.updated)
	}
}

func TestStartMessageProgress_RemoveDeletesPlaceholder(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	h, err := StartMessageProgress(context.Background(), post, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartMessageProgress: %v", err)
	}
	if err := h.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if post.deleted != 1 {
		t.Fatalf("deleted=%d, want 1", post.deleted)
	}
	// Update/Remove are mutually exclusive terminal calls — the first one wins.
	if err := h.Update("x"); err != nil {
		t.Fatalf("Update after Remove: %v", err)
	}
	if len(post.updated) != 0 {
		t.Fatalf("updated=%v, want none", post.updated)
	}
}

func TestStartAssistantStatusProgress_SetsInitialStatus(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	status := &fakeStatusSetter{}
	h, err := StartAssistantStatusProgress(context.Background(), post, status, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartAssistantStatusProgress: %v", err)
	}
	if h.Channel() != "C1" {
		t.Errorf("Channel() = %q, want C1", h.Channel())
	}
	if len(status.statuses) != 1 || status.statuses[0] != "Working…" {
		t.Fatalf("statuses=%v", status.statuses)
	}
}

func TestStartAssistantStatusProgress_InitialErrorPropagates(t *testing.T) {
	t.Parallel()
	status := &fakeStatusSetter{err: errors.New("boom")}
	if _, err := StartAssistantStatusProgress(context.Background(), &fakeProgressPoster{}, status, "C1", "100.1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStartAssistantStatusProgress_UpdatePostsMessageAndClearsStatus(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	status := &fakeStatusSetter{}
	h, err := StartAssistantStatusProgress(context.Background(), post, status, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartAssistantStatusProgress: %v", err)
	}

	if err := h.Update("✅ Done"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(post.posted) != 1 || post.posted[0] != "✅ Done" {
		t.Fatalf("posted=%v, want a single \"✅ Done\" message (no placeholder to rewrite in this style)", post.posted)
	}
	if post.deleted != 0 {
		t.Fatalf("deleted=%d, want 0", post.deleted)
	}
	if want := []string{"Working…", ""}; !equalStrings(status.statuses, want) {
		t.Fatalf("statuses=%v, want %v", status.statuses, want)
	}

	// Second call is a no-op: no extra message, no extra status call.
	if err := h.Update("again"); err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if len(post.posted) != 1 {
		t.Fatalf("posted=%v, want no second post", post.posted)
	}
}

func TestStartAssistantStatusProgress_DoneClearsStatusWithoutPosting(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	status := &fakeStatusSetter{}
	h, err := StartAssistantStatusProgress(context.Background(), post, status, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartAssistantStatusProgress: %v", err)
	}

	if err := h.Done(); err != nil {
		t.Fatalf("Done: %v", err)
	}
	// The whole point of assistant_status: silent success = no new message.
	if len(post.posted) != 0 {
		t.Fatalf("posted=%v, want none (Done must not post a \"✅ Done\" message in this style)", post.posted)
	}
	if want := []string{"Working…", ""}; !equalStrings(status.statuses, want) {
		t.Fatalf("statuses=%v, want %v", status.statuses, want)
	}
}

func TestStartAssistantStatusProgress_RemoveClearsStatusWithoutPosting(t *testing.T) {
	t.Parallel()
	post := &fakeProgressPoster{}
	status := &fakeStatusSetter{}
	h, err := StartAssistantStatusProgress(context.Background(), post, status, "C1", "100.1")
	if err != nil {
		t.Fatalf("StartAssistantStatusProgress: %v", err)
	}

	if err := h.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(post.posted) != 0 {
		t.Fatalf("posted=%v, want none", post.posted)
	}
	if want := []string{"Working…", ""}; !equalStrings(status.statuses, want) {
		t.Fatalf("statuses=%v, want %v", status.statuses, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
