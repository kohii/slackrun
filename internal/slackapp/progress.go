package slackapp

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

const (
	progressTick   = 5 * time.Second
	progressJitter = 750 * time.Millisecond
)

// Poster is the small slack-API surface progress + reply need. *slack.Client
// satisfies it; tests can substitute a fake.
type Poster interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	DeleteMessage(channel, messageTimestamp string) (string, string, error)
}

// StatusSetter is the assistant.threads.setStatus surface. *slack.Client
// satisfies it. Kept separate from Poster so the "message" progress style
// never needs assistant scopes.
type StatusSetter interface {
	SetAssistantThreadsStatus(params slack.AssistantThreadsSetStatusParameters) error
}

// ProgressHandle is the lifecycle every progress backend implements: start
// (in the constructor), periodic keep-alive ticks, and a single terminal
// Update / Done / Remove call.
type ProgressHandle interface {
	// Channel is where the job's progress indicator lives, needed by callers
	// that post follow-up content (multi-part replies, file uploads).
	Channel() string
	// Update settles the indicator with final text the user must see (error
	// details, stdout, etc.) and stops ticking. Safe to call multiple times
	// — only the first call has effect.
	Update(text string) error
	// Done settles the indicator for a successful completion that carries no
	// content the user needs to see. For the message backend this leaves a
	// "✅ Done" marker so the placeholder is not orphaned; for the
	// assistant-status backend it just clears the status (no new post).
	// Safe to call multiple times — only the first call has effect.
	Done() error
	// Remove clears the indicator without leaving any final text (used
	// ahead of a file upload, which supplies its own content). Safe to call
	// multiple times — only the first call has effect.
	Remove() error
}

// ticker runs onTick every ~5s plus jitter until halted. Shared by both
// progress backends so only the Slack call they make at each tick differs.
type ticker struct {
	stop    context.CancelFunc
	stopped chan struct{}
}

func startTicker(ctx context.Context, onTick func(elapsed time.Duration)) *ticker {
	tickCtx, cancel := context.WithCancel(ctx)
	t := &ticker{stop: cancel, stopped: make(chan struct{})}
	go func() {
		defer close(t.stopped)
		started := time.Now()
		for {
			// math/rand/v2 is goroutine-safe and seeded by the runtime.
			delay := progressTick + rand.N(progressJitter)
			timer := time.NewTimer(delay)
			select {
			case <-tickCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			onTick(time.Since(started))
		}
	}()
	return t
}

// halt cancels the tick loop and blocks until the goroutine has actually
// exited, so no tick can race the caller's terminal write.
func (t *ticker) halt() {
	t.stop()
	<-t.stopped
}

// messageProgress is the default progress style: a "⏳ Working…" message
// that gets rewritten in place via chat.update.
type messageProgress struct {
	channel string
	ts      string
	post    Poster
	ticker  *ticker
	once    sync.Once
}

// StartMessageProgress posts the initial "⏳ Working…" message and starts a
// background updater that rewrites it with elapsed time every ~5s plus a
// small jitter.
func StartMessageProgress(ctx context.Context, post Poster, channel, threadTS string) (ProgressHandle, error) {
	_, ts, err := post.PostMessage(channel,
		slack.MsgOptionText("⏳ Working…", false),
		slack.MsgOptionTS(threadTS),
		slack.MsgOptionDisableMarkdown(),
	)
	if err != nil {
		return nil, fmt.Errorf("post progress: %w", err)
	}

	h := &messageProgress{channel: channel, ts: ts, post: post}
	h.ticker = startTicker(ctx, func(elapsed time.Duration) {
		if _, _, _, err := post.UpdateMessage(channel, ts,
			slack.MsgOptionText("⏳ Working… "+util.FormatDuration(elapsed), false),
		); err != nil {
			logging.Warn("progress update failed", logging.F("error", err))
		}
	})
	return h, nil
}

func (h *messageProgress) Channel() string { return h.channel }

func (h *messageProgress) Update(text string) error {
	var firstErr error
	h.once.Do(func() {
		h.ticker.halt()
		_, _, _, err := h.post.UpdateMessage(h.channel, h.ts, slack.MsgOptionText(text, false))
		firstErr = err
	})
	return firstErr
}

func (h *messageProgress) Done() error {
	return h.Update("✅ Done")
}

func (h *messageProgress) Remove() error {
	var firstErr error
	h.once.Do(func() {
		h.ticker.halt()
		_, _, err := h.post.DeleteMessage(h.channel, h.ts)
		firstErr = err
	})
	return firstErr
}

// assistantStatusProgress uses assistant.threads.setStatus for a transient
// "is thinking…" indicator instead of a visible message. There is no
// placeholder to rewrite, so Update posts the final text as a new message
// and then clears the status.
type assistantStatusProgress struct {
	channel  string
	threadTS string
	post     Poster
	status   StatusSetter
	ticker   *ticker
	once     sync.Once
}

// StartAssistantStatusProgress sets the initial "Working…" status and starts
// a background refresher so the status survives Slack's 2-minute
// no-activity timeout on long-running jobs.
func StartAssistantStatusProgress(ctx context.Context, post Poster, status StatusSetter, channel, threadTS string) (ProgressHandle, error) {
	if err := status.SetAssistantThreadsStatus(slack.AssistantThreadsSetStatusParameters{
		ChannelID: channel,
		ThreadTS:  threadTS,
		Status:    "Working…",
	}); err != nil {
		return nil, fmt.Errorf("assistant.threads.setStatus: %w", err)
	}

	h := &assistantStatusProgress{channel: channel, threadTS: threadTS, post: post, status: status}
	h.ticker = startTicker(ctx, func(elapsed time.Duration) {
		if err := status.SetAssistantThreadsStatus(slack.AssistantThreadsSetStatusParameters{
			ChannelID: channel,
			ThreadTS:  threadTS,
			Status:    "Working… " + util.FormatDuration(elapsed),
		}); err != nil {
			logging.Warn("assistant status update failed", logging.F("error", err))
		}
	})
	return h, nil
}

func (h *assistantStatusProgress) Channel() string { return h.channel }

func (h *assistantStatusProgress) Update(text string) error {
	var firstErr error
	h.once.Do(func() {
		h.ticker.halt()
		_, _, err := h.post.PostMessage(h.channel,
			slack.MsgOptionText(text, false),
			slack.MsgOptionTS(h.threadTS),
			slack.MsgOptionDisableMarkdown(),
		)
		firstErr = err
		if clearErr := h.clearStatus(); clearErr != nil {
			logging.Warn("assistant status clear failed", logging.F("error", clearErr))
		}
	})
	return firstErr
}

func (h *assistantStatusProgress) Done() error {
	// Silent completion: clear the status but post nothing. This is what
	// makes assistant_status distinct from the message backend — successful
	// no-output runs vanish instead of leaving a "✅ Done" trail.
	return h.Remove()
}

func (h *assistantStatusProgress) Remove() error {
	var firstErr error
	h.once.Do(func() {
		h.ticker.halt()
		firstErr = h.clearStatus()
	})
	return firstErr
}

func (h *assistantStatusProgress) clearStatus() error {
	return h.status.SetAssistantThreadsStatus(slack.AssistantThreadsSetStatusParameters{
		ChannelID: h.channel,
		ThreadTS:  h.threadTS,
		Status:    "",
	})
}
