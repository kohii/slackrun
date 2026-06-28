package slackapp

import (
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/runner"
)

// jobRegistry tracks in-flight executions so we can finalize their progress
// messages and kill the children on shutdown.
type jobRegistry struct {
	mu   sync.Mutex
	live map[string]*jobEntry
}

type jobEntry struct {
	progress *ProgressHandle
	handle   *runner.Handle
}

func newJobRegistry() *jobRegistry {
	return &jobRegistry{live: make(map[string]*jobEntry)}
}

func (r *jobRegistry) register(id string, p *ProgressHandle, h *runner.Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live[id] = &jobEntry{progress: p, handle: h}
}

func (r *jobRegistry) updateExec(id string, h *runner.Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.live[id]; ok {
		e.handle = h
	}
}

func (r *jobRegistry) unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.live, id)
}

// stopAll signals SIGTERM (via Kill) to every running job, waits up to
// `wait` per job for the child to exit (so SIGKILL has a chance to land
// before we tear the process down), then overwrites the progress message.
// Best-effort: errors are logged but do not stop iteration.
func (r *jobRegistry) stopAll(message string, wait time.Duration) {
	r.mu.Lock()
	entries := make([]*jobEntry, 0, len(r.live))
	for _, e := range r.live {
		entries = append(entries, e)
	}
	r.live = make(map[string]*jobEntry)
	r.mu.Unlock()

	for _, e := range entries {
		if e.handle != nil {
			e.handle.Kill()
		}
	}
	// Give the SIGTERM → SIGKILL ladder time to land before we walk away.
	deadline := time.Now().Add(wait)
	for _, e := range entries {
		if e.handle != nil {
			remaining := time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
			select {
			case <-e.handle.Done:
			case <-time.After(remaining):
			}
		}
		if e.progress != nil {
			if err := e.progress.Update(message); err != nil {
				logging.Warn("shutdown progress update failed", logging.F("error", err))
			}
		}
	}
}
