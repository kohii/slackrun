// Package runmgr owns the lifecycle of in-flight rule executions: state
// tracking (preparing → running → done), IDs surfaced to `slackrun runs` /
// `slackrun kill`, and coordinated shutdown. It is intentionally decoupled
// from Slack: callers hand it a ProgressUpdater and a Handle, and it hands
// back a final ExitCause. This keeps the admin HTTP layer testable without
// a Slack client.
package runmgr

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ProgressUpdater is the tiny slice of slackapp.ProgressHandle the manager
// needs. Update is guarded by the backend's own sync.Once so multiple writers
// (kill path + owner path) race safely; only the first one lands.
type ProgressUpdater interface {
	Update(text string) error
}

// Handle is what the manager needs from a spawned process: request SIGTERM
// (idempotent) and report the child's OS PID. runner.Handle is adapted to
// this by slackapp.
type Handle interface {
	Kill()
	PID() int
}

// State is the current position of a run in its lifecycle.
type State int

const (
	// StatePreparing: manager knows about the run (progress placeholder is
	// up) but the child process has not been spawned yet. v1 rejects kill in
	// this state with ErrNotKillable — cancelling the spawn is a v2 concern.
	StatePreparing State = iota
	// StateRunning: child process is live; PID is meaningful.
	StateRunning
	// StateCancelled: Shutdown intercepted the run while it was still
	// Preparing. AttachHandle returns ErrCancelled in this state so the
	// owner can kill its (already-spawned but unattached) child before
	// waiting on it, rather than leaking a live process past shutdown.
	StateCancelled
	// StateDone: Complete has been called. Entry is removed from the active
	// map, so external lookups see ErrNotFound.
	StateDone
)

func (s State) String() string {
	switch s {
	case StatePreparing:
		return "preparing"
	case StateRunning:
		return "running"
	case StateCancelled:
		return "cancelled"
	case StateDone:
		return "done"
	default:
		return "unknown"
	}
}

// ExitCause is the reason a run finished. Set first-writer-wins: whichever
// terminal path (timeout timer / admin kill / shutdown / natural exit) marks
// the entry first is what shows up.
type ExitCause int

const (
	CauseNone ExitCause = iota
	CauseExit           // process exited on its own (zero or non-zero code)
	CauseTimedOut
	CauseKilled     // admin API
	CauseShutdown   // manager.Shutdown
	CauseStartError // Start() failed
	CauseNotFound   // binary not found (ENOENT)
)

func (c ExitCause) String() string {
	switch c {
	case CauseExit:
		return "exit"
	case CauseTimedOut:
		return "timed_out"
	case CauseKilled:
		return "killed"
	case CauseShutdown:
		return "shutdown"
	case CauseStartError:
		return "start_error"
	case CauseNotFound:
		return "not_found"
	default:
		return "none"
	}
}

// Meta is the caller-provided descriptor for a new run. FullID is the stable
// "channel:ts:rule" tuple slackrun already uses internally; it is surfaced
// alongside the short ID so operators debugging via logs can still cross-
// reference.
type Meta struct {
	FullID    string
	RuleName  string
	ChannelID string
	UserID    string
	ThreadTS  string
	StartedAt time.Time
}

// Snapshot is the outside-visible view of a live run. `Kill*` and `Exit*`
// fields are populated only after Complete has fired — until then they hold
// zero values.
type Snapshot struct {
	ID         string    // short ID
	FullID     string    // channel:ts:rulename
	RuleName   string
	ChannelID  string
	UserID     string
	ThreadTS   string
	StartedAt  time.Time
	State      State
	PID        int
	ExitCause  ExitCause
	ExitCode   int
	KillReason string
}

// KillOptions carries per-request kill parameters. Only Reason is honoured
// in v1; grace override is a v2 addition.
type KillOptions struct {
	Reason string
}

// KillResult is what Kill returns on success. Kept minimal — callers that
// need the full row should follow up with Snapshot.
type KillResult struct {
	ID     string
	FullID string
	State  State // state at the moment kill was accepted (always StateRunning in v1)
}

// Sentinel errors surfaced to the admin API as 404 / 409.
var (
	ErrNotFound    = errors.New("run not found")
	ErrNotKillable = errors.New("run not killable in current state")
	// ErrCancelled is returned by AttachHandle when Shutdown has already
	// transitioned this run out of Preparing. The owner is expected to
	// kill its orphaned child and let Complete run normally.
	ErrCancelled = errors.New("run cancelled during preparation")
)

type entry struct {
	id       string
	meta     Meta
	progress ProgressUpdater
	handle   Handle // nil until AttachHandle
	state    State
	cause    ExitCause
	reason   string
	exitCode int
	done     chan struct{} // closed by Complete
}

func (e *entry) snapshot() Snapshot {
	pid := 0
	if e.handle != nil {
		pid = e.handle.PID()
	}
	return Snapshot{
		ID:         e.id,
		FullID:     e.meta.FullID,
		RuleName:   e.meta.RuleName,
		ChannelID:  e.meta.ChannelID,
		UserID:     e.meta.UserID,
		ThreadTS:   e.meta.ThreadTS,
		StartedAt:  e.meta.StartedAt,
		State:      e.state,
		PID:        pid,
		ExitCause:  e.cause,
		ExitCode:   e.exitCode,
		KillReason: e.reason,
	}
}

// Manager is the central registry. Zero value is not usable; call New.
type Manager struct {
	mu   sync.Mutex
	byID map[string]*entry
	now  func() time.Time
}

// New returns an empty manager.
func New() *Manager {
	return &Manager{
		byID: make(map[string]*entry),
		now:  time.Now,
	}
}

// Register records a new run in Preparing state and returns its short ID.
// The caller must eventually call either AttachHandle+Complete (normal path)
// or, if the run aborts before spawn, AbortPreparing to release the slot.
func (m *Manager) Register(meta Meta, p ProgressUpdater) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Try a few times in case of a birthday collision. 40 bits × ~10 active
	// runs is astronomically safe; the retry is a hedge, not the algorithm.
	for attempts := 0; attempts < 8; attempts++ {
		id, err := newShortID()
		if err != nil {
			return "", err
		}
		if _, exists := m.byID[id]; exists {
			continue
		}
		e := &entry{
			id:       id,
			meta:     meta,
			progress: p,
			state:    StatePreparing,
			done:     make(chan struct{}),
		}
		m.byID[id] = e
		return id, nil
	}
	return "", ErrIDGeneration
}

// AttachHandle transitions a run from Preparing → Running. If a kill request
// arrived while the run was still Preparing, the returned killPending is
// true and the caller (owner) should propagate the kill immediately after
// AttachHandle returns — v1 keeps that path a no-op (kill in Preparing is
// rejected). Kept in the signature for the v2 pre-spawn-cancel work.
func (m *Manager) AttachHandle(id string, h Handle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok {
		return ErrNotFound
	}
	if e.state == StateCancelled {
		// Record the handle so a subsequent Complete() unblocks Shutdown's
		// per-entry wait — otherwise the shutdown would fall through to
		// its timeout even though the owner is racing to kill the child.
		e.handle = h
		return ErrCancelled
	}
	if e.state != StatePreparing {
		return errors.New("run not in preparing state")
	}
	e.handle = h
	e.state = StateRunning
	return nil
}

// Complete finalises a run: records the exit code, resolves the cause
// (first-writer-wins with any earlier Kill / Shutdown), closes the done
// channel, and removes the entry from the active map. Returns the *final*
// cause and kill reason so the caller can render the terminal progress
// message correctly.
func (m *Manager) Complete(id string, cause ExitCause, exitCode int) (ExitCause, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok {
		return cause, ""
	}
	if e.cause == CauseNone {
		e.cause = cause
	}
	e.exitCode = exitCode
	e.state = StateDone
	close(e.done)
	delete(m.byID, id)
	return e.cause, e.reason
}

// AbortPreparing removes a run that never made it to Running (e.g. Slack
// permalink lookup errored out before spawn). Optional — callers that only
// call Register on the happy path are fine leaving Complete to clean up.
func (m *Manager) AbortPreparing(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok || e.state != StatePreparing {
		return
	}
	e.state = StateDone
	close(e.done)
	delete(m.byID, id)
}

// Kill delivers SIGTERM to a Running child and posts a terminal progress
// message. Returns:
//   - ErrNotFound       — no such id
//   - ErrNotKillable    — id exists but state != Running (v1: Preparing yields
//                         this too)
//
// The progress.Update call is done off-lock so a slow Slack API doesn't
// block Complete. The backend's sync.Once ensures the eventual owner-side
// finalisation doesn't overwrite our message.
func (m *Manager) Kill(id string, opts KillOptions) (KillResult, error) {
	m.mu.Lock()
	e, ok := m.byID[id]
	if !ok {
		m.mu.Unlock()
		return KillResult{}, ErrNotFound
	}
	if e.state != StateRunning {
		m.mu.Unlock()
		return KillResult{}, ErrNotKillable
	}
	// First-writer-wins: mark the cause now so Complete can't clobber it.
	if e.cause == CauseNone {
		e.cause = CauseKilled
		e.reason = opts.Reason
	}
	h := e.handle
	p := e.progress
	res := KillResult{ID: e.id, FullID: e.meta.FullID, State: e.state}
	m.mu.Unlock()

	if h != nil {
		h.Kill()
	}
	if p != nil {
		// Fire-and-forget: the ticker in progress backends is already
		// halted by the sync.Once inside Update; errors are backend-logged.
		go func() { _ = p.Update(killMessage(opts.Reason)) }()
	}
	return res, nil
}

// KillAll invokes Kill on every currently Running entry. Returns the list of
// short IDs that were signalled (so the CLI can echo them back). Entries
// still Preparing are skipped in v1.
func (m *Manager) KillAll(opts KillOptions) []KillResult {
	m.mu.Lock()
	ids := make([]string, 0, len(m.byID))
	for id, e := range m.byID {
		if e.state == StateRunning {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	sort.Strings(ids)
	out := make([]KillResult, 0, len(ids))
	for _, id := range ids {
		if r, err := m.Kill(id, opts); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// Snapshot returns the current view of all runs (Preparing + Running),
// sorted by StartedAt then ID for stable output.
func (m *Manager) Snapshot() []Snapshot {
	m.mu.Lock()
	out := make([]Snapshot, 0, len(m.byID))
	for _, e := range m.byID {
		out = append(out, e.snapshot())
	}
	m.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Lookup returns a Snapshot for id, or ErrNotFound.
func (m *Manager) Lookup(id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byID[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return e.snapshot(), nil
}

// Shutdown signals every Running entry, waits up to `wait` in total (not
// per-entry) for owners to finish their Complete calls, and overwrites the
// progress message via each entry's ProgressUpdater. This replaces the old
// jobRegistry.stopAll and — critically — never reads runner.Handle.Done
// directly, so the owner side of each run keeps its single consumer.
func (m *Manager) Shutdown(_ context.Context, message string, wait time.Duration) {
	type item struct {
		id       string
		handle   Handle
		progress ProgressUpdater
		done     chan struct{}
	}
	m.mu.Lock()
	items := make([]item, 0, len(m.byID))
	for _, e := range m.byID {
		switch e.state {
		case StateRunning:
			// fall through
		case StatePreparing:
			// Flip to Cancelled so AttachHandle short-circuits the owner
			// and it stops its (about-to-be-spawned or freshly-spawned)
			// child before waiting on Done.
			e.state = StateCancelled
		default:
			continue
		}
		if e.cause == CauseNone {
			e.cause = CauseShutdown
		}
		items = append(items, item{id: e.id, handle: e.handle, progress: e.progress, done: e.done})
	}
	m.mu.Unlock()

	for _, it := range items {
		if it.handle != nil {
			it.handle.Kill()
		}
	}
	deadline := m.now().Add(wait)
	for _, it := range items {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		select {
		case <-it.done:
		case <-time.After(remaining):
		}
		if it.progress != nil {
			_ = it.progress.Update(message)
		}
	}
}

// killMessage renders the terminal Slack progress line for an admin kill.
// Kept unified across progress backends so the message backend's chat.update
// and the assistant_status backend's fresh post read the same.
func killMessage(reason string) string {
	if reason == "" {
		return "⏹️ Killed by admin"
	}
	return "⏹️ Killed by admin: " + reason
}
