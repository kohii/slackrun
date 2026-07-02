package runmgr

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProgress records the terminal Update text and enforces the same
// once-only semantics real progress backends have — so a Kill / Complete
// race can't produce a spurious second update in the test.
type fakeProgress struct {
	mu   sync.Mutex
	once sync.Once
	text string
}

func (p *fakeProgress) Update(t string) error {
	p.once.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.text = t
	})
	return nil
}
func (p *fakeProgress) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text
}

type fakeHandle struct {
	killed atomic.Bool
	pid    int
}

func (h *fakeHandle) Kill()    { h.killed.Store(true) }
func (h *fakeHandle) PID() int { return h.pid }

func newRunningEntry(t *testing.T, m *Manager) (id string, p *fakeProgress, h *fakeHandle) {
	t.Helper()
	p = &fakeProgress{}
	h = &fakeHandle{pid: 4242}
	id, err := m.Register(Meta{
		FullID:    "C1:1.0:rule",
		RuleName:  "rule",
		ChannelID: "C1",
		StartedAt: time.Unix(1000, 0),
	}, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.AttachHandle(id, h); err != nil {
		t.Fatalf("AttachHandle: %v", err)
	}
	return id, p, h
}

func TestManager_RegisterAndSnapshot(t *testing.T) {
	m := New()
	p := &fakeProgress{}
	id, err := m.Register(Meta{FullID: "C:1:r", RuleName: "r", ChannelID: "C", StartedAt: time.Unix(1, 0)}, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(id) != shortIDLen {
		t.Errorf("short id length = %d, want %d", len(id), shortIDLen)
	}
	got := m.Snapshot()
	if len(got) != 1 || got[0].ID != id || got[0].State != StatePreparing {
		t.Fatalf("Snapshot mismatch: %+v", got)
	}
}

func TestManager_Kill_Preparing_IsNotKillable(t *testing.T) {
	m := New()
	id, err := m.Register(Meta{FullID: "C:1:r", RuleName: "r"}, &fakeProgress{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := m.Kill(id, KillOptions{Reason: "no"}); !errors.Is(err, ErrNotKillable) {
		t.Errorf("Kill in preparing state: got %v, want ErrNotKillable", err)
	}
}

func TestManager_Kill_Running(t *testing.T) {
	m := New()
	id, p, h := newRunningEntry(t, m)

	res, err := m.Kill(id, KillOptions{Reason: "manual"})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if res.ID != id {
		t.Errorf("Kill returned id %q, want %q", res.ID, id)
	}
	if !h.killed.Load() {
		t.Errorf("child handle was not signalled")
	}
	// Progress update is fired in a goroutine — wait briefly.
	waitFor(t, func() bool { return p.Text() != "" }, 500*time.Millisecond)
	if got := p.Text(); got != "⏹️ Killed by admin: manual" {
		t.Errorf("progress text = %q", got)
	}
}

func TestManager_Kill_NotFound(t *testing.T) {
	m := New()
	if _, err := m.Kill("NOPE", KillOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Kill nonexistent: got %v, want ErrNotFound", err)
	}
}

func TestManager_Complete_AfterKill_KeepsKilledCause(t *testing.T) {
	m := New()
	id, _, _ := newRunningEntry(t, m)

	if _, err := m.Kill(id, KillOptions{Reason: "x"}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// runner reports natural-looking exit code after SIGTERM.
	cause, reason := m.Complete(id, CauseExit, -1)
	if cause != CauseKilled {
		t.Errorf("cause = %v, want CauseKilled (first-writer-wins)", cause)
	}
	if reason != "x" {
		t.Errorf("kill reason = %q", reason)
	}
	// After Complete the run must be gone.
	if _, err := m.Lookup(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup after Complete: got %v, want ErrNotFound", err)
	}
}

func TestManager_Complete_NaturalExit_Wins(t *testing.T) {
	m := New()
	id, _, _ := newRunningEntry(t, m)

	cause, _ := m.Complete(id, CauseExit, 0)
	if cause != CauseExit {
		t.Errorf("cause = %v, want CauseExit", cause)
	}
}

func TestManager_Shutdown_KillsRunningAndPostsProgress(t *testing.T) {
	m := New()
	id, p, h := newRunningEntry(t, m)

	// simulate owner Complete after Shutdown's Kill lands
	go func() {
		time.Sleep(20 * time.Millisecond)
		m.Complete(id, CauseExit, -1)
	}()
	m.Shutdown(context.Background(), "⚠️ Bot stopped", 500*time.Millisecond)

	if !h.killed.Load() {
		t.Errorf("child not signalled during Shutdown")
	}
	if got := p.Text(); got != "⚠️ Bot stopped" {
		t.Errorf("progress text = %q, want ⚠️ Bot stopped", got)
	}
}

func TestManager_Shutdown_CancelsPreparing(t *testing.T) {
	m := New()
	p := &fakeProgress{}
	id, err := m.Register(Meta{FullID: "C:1:r", RuleName: "r", StartedAt: time.Unix(1, 0)}, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Simulate an owner that races: Shutdown fires, then the owner tries
	// to AttachHandle and Complete after Kill.
	go func() {
		time.Sleep(20 * time.Millisecond)
		h := &fakeHandle{}
		attachErr := m.AttachHandle(id, h)
		if !errors.Is(attachErr, ErrCancelled) {
			t.Errorf("AttachHandle in shutdown: got %v, want ErrCancelled", attachErr)
		}
		m.Complete(id, CauseExit, -1)
	}()
	m.Shutdown(context.Background(), "⚠️ Bot stopped", 500*time.Millisecond)

	if got := p.Text(); got != "⚠️ Bot stopped" {
		t.Errorf("progress text = %q, want ⚠️ Bot stopped", got)
	}
	if _, err := m.Lookup(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup post-shutdown: got %v, want ErrNotFound", err)
	}
}

func TestManager_KillAll_SignalsEveryRunner(t *testing.T) {
	m := New()
	handles := make([]*fakeHandle, 3)
	for i := range handles {
		_, _, h := newRunningEntry(t, m)
		handles[i] = h
	}
	res := m.KillAll(KillOptions{Reason: "op"})
	if len(res) != 3 {
		t.Errorf("KillAll returned %d results, want 3", len(res))
	}
	for i, h := range handles {
		if !h.killed.Load() {
			t.Errorf("handle %d not killed", i)
		}
	}
}

func waitFor(t *testing.T, ok func() bool, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition never satisfied within %v", budget)
}
