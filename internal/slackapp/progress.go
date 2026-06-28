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
	progressTick    = 5 * time.Second
	progressJitter  = 750 * time.Millisecond
)

// Poster is the small slack-API surface progress + reply need. *slack.Client
// satisfies it; tests can substitute a fake.
type Poster interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	DeleteMessage(channel, messageTimestamp string) (string, string, error)
}

// ProgressHandle is the state machine returned by StartProgress. Update()
// overwrites the progress message and stops further ticks; Remove() deletes
// it entirely (the path used before file uploads).
type ProgressHandle struct {
	Channel string
	TS      string

	post Poster
	stop context.CancelFunc

	once    sync.Once
	stopped chan struct{}
}

// StartProgress posts the initial "⏳ Working…" message and starts a
// background updater that rewrites the ts with elapsed time every ~5s plus a
// small jitter.
func StartProgress(ctx context.Context, post Poster, channel, threadTS string) (*ProgressHandle, error) {
	_, ts, err := post.PostMessage(channel,
		slack.MsgOptionText("⏳ Working…", false),
		slack.MsgOptionTS(threadTS),
		slack.MsgOptionDisableMarkdown(),
	)
	if err != nil {
		return nil, fmt.Errorf("post progress: %w", err)
	}

	tickCtx, cancel := context.WithCancel(ctx)
	h := &ProgressHandle{
		Channel: channel,
		TS:      ts,
		post:    post,
		stop:    cancel,
		stopped: make(chan struct{}),
	}

	go func() {
		defer close(h.stopped)
		started := time.Now()
		for {
			// math/rand/v2 is goroutine-safe and seeded by the runtime.
			delay := progressTick + rand.N(progressJitter)
			t := time.NewTimer(delay)
			select {
			case <-tickCtx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			elapsed := time.Since(started)
			_, _, _, err := post.UpdateMessage(channel, ts,
				slack.MsgOptionText("⏳ Working… "+util.FormatDuration(elapsed), false),
			)
			if err != nil {
				logging.Warn("progress update failed", logging.F("error", err))
			}
		}
	}()
	return h, nil
}

// Update overwrites the progress message with the given text and stops the
// updater. Safe to call multiple times — only the first call writes.
func (h *ProgressHandle) Update(text string) error {
	var firstErr error
	h.once.Do(func() {
		h.stop()
		<-h.stopped
		_, _, _, err := h.post.UpdateMessage(h.Channel, h.TS, slack.MsgOptionText(text, false))
		firstErr = err
	})
	return firstErr
}

// Remove deletes the progress message entirely (used before files.uploadV2 so
// the thread does not show a stranded "⏳" message).
func (h *ProgressHandle) Remove() error {
	var firstErr error
	h.once.Do(func() {
		h.stop()
		<-h.stopped
		_, _, err := h.post.DeleteMessage(h.Channel, h.TS)
		firstErr = err
	})
	return firstErr
}

