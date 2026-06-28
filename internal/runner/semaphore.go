// Package runner spawns external commands matched by the dispatcher and
// caps how many run concurrently.
package runner

import (
	"errors"
	"sync"
)

// Semaphore is a FIFO admission controller. Callers Acquire to enter, the
// returned Release frees their slot. The bot owns exactly one instance sized
// by MAX_CONCURRENT.
//
// The "FIFO" property matters because we surface the queue position (`waitPos`)
// to the user as part of the progress message — out-of-order admission would
// make those numbers lie.
type Semaphore struct {
	mu      sync.Mutex
	permits int
	waiters []chan struct{}
}

// NewSemaphore returns a semaphore with capacity n (must be > 0).
func NewSemaphore(n int) (*Semaphore, error) {
	if n <= 0 {
		return nil, errors.New("semaphore capacity must be > 0")
	}
	return &Semaphore{permits: n}, nil
}

// Acquire blocks until a permit is granted. The returned waitPos is 0 when
// admission was immediate, or the 1-based queue position when the caller had
// to wait. Release must be called exactly once.
func (s *Semaphore) Acquire() (waitPos int, release func()) {
	s.mu.Lock()
	if s.permits > 0 {
		s.permits--
		s.mu.Unlock()
		return 0, s.release
	}
	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	pos := len(s.waiters)
	s.mu.Unlock()

	<-ch
	return pos, s.release
}

func (s *Semaphore) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.waiters) > 0 {
		next := s.waiters[0]
		s.waiters = s.waiters[1:]
		close(next)
		return
	}
	s.permits++
}

// QueuedCount reports the number of pending waiters. For diagnostics only —
// inherently racy.
func (s *Semaphore) QueuedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.waiters)
}
