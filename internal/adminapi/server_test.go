package adminapi

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kohii/slackrun/internal/runmgr"
)

// fakeProgress satisfies runmgr.ProgressUpdater with once-only semantics
// mirroring the real backends. See runmgr/manager_test.go for the sibling.
type fakeProgress struct {
	mu   sync.Mutex
	once sync.Once
	text string
}

func (p *fakeProgress) Update(t string) error {
	p.once.Do(func() {
		p.mu.Lock()
		p.text = t
		p.mu.Unlock()
	})
	return nil
}

type fakeHandle struct {
	killed bool
	mu     sync.Mutex
}

func (h *fakeHandle) Kill() {
	h.mu.Lock()
	h.killed = true
	h.mu.Unlock()
}
func (h *fakeHandle) PID() int { return 4242 }
func (h *fakeHandle) WasKilled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killed
}

// startTestServer spins up the admin server on a temp socket and returns a
// client bound to it. Socket path is shortened via TMPDIR override so the
// 104-byte macOS limit never bites in CI.
func startTestServer(t *testing.T, mgr *runmgr.Manager) *Client {
	t.Helper()
	dir := t.TempDir()
	// A path like `/var/folders/.../TestAdminAPI_.../slackrun.sock` is
	// already comfortably under 104 bytes; keeping the filename short
	// (`s.sock`) keeps room for CI paths.
	sockPath := filepath.Join(dir, "s.sock")
	t.Setenv(SocketEnvVar, sockPath)

	srv := New(Options{Runs: mgr, Version: "test"})
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		srv.Stop(ctx)
	})
	// Wait for the file to exist so client dial doesn't race the goroutine.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return NewClient(sockPath)
}

func TestServer_HealthAndRuns(t *testing.T) {
	mgr := runmgr.New()
	client := startTestServer(t, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.OK || h.Version != "test" {
		t.Errorf("health payload = %+v", h)
	}

	// Register a running entry so /runs has one row.
	p := &fakeProgress{}
	id, err := mgr.Register(runmgr.Meta{
		FullID:    "C:1:r",
		RuleName:  "rule-a",
		ChannelID: "C",
		StartedAt: time.Unix(1000, 0),
	}, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	handle := &fakeHandle{}
	if err := mgr.AttachHandle(id, handle); err != nil {
		t.Fatalf("AttachHandle: %v", err)
	}

	rows, err := client.Runs(ctx)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].State != "running" {
		t.Fatalf("runs payload = %+v", rows)
	}
}

func TestServer_KillAndNotFound(t *testing.T) {
	mgr := runmgr.New()
	client := startTestServer(t, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p := &fakeProgress{}
	id, err := mgr.Register(runmgr.Meta{FullID: "C:1:r", RuleName: "r", StartedAt: time.Unix(1, 0)}, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	handle := &fakeHandle{}
	_ = mgr.AttachHandle(id, handle)

	res, err := client.Kill(ctx, id, "op-request")
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !res.Killed || res.ID != id {
		t.Errorf("kill response = %+v", res)
	}
	if !handle.WasKilled() {
		t.Errorf("handle not signalled")
	}

	// Bad id → 404 with structured error.
	if _, err := client.Kill(ctx, "NOPE00", "x"); err == nil {
		t.Fatalf("Kill expected error, got nil")
	} else if apiErr, ok := err.(*APIError); !ok || apiErr.StatusCode != 404 {
		t.Errorf("expected 404 APIError, got %v", err)
	}
}

func TestServer_KillAll(t *testing.T) {
	mgr := runmgr.New()
	client := startTestServer(t, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := 0; i < 3; i++ {
		p := &fakeProgress{}
		id, _ := mgr.Register(runmgr.Meta{FullID: "C:1:r", RuleName: "r"}, p)
		_ = mgr.AttachHandle(id, &fakeHandle{})
	}
	res, err := client.KillAll(ctx, "shutdown-test")
	if err != nil {
		t.Fatalf("KillAll: %v", err)
	}
	if len(res.KilledIDs) != 3 {
		t.Errorf("KillAll returned %d ids, want 3", len(res.KilledIDs))
	}
}

func TestResolveSocketPath_OffDisables(t *testing.T) {
	t.Setenv(SocketEnvVar, "off")
	if _, err := ResolveSocketPath(); err == nil {
		t.Fatalf("expected ErrDisabled, got nil")
	} else if err != ErrDisabled {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestResolveSocketPath_ExplicitRelativeRejected(t *testing.T) {
	t.Setenv(SocketEnvVar, "relative/path.sock")
	if _, err := ResolveSocketPath(); err == nil {
		t.Fatalf("expected error for relative path")
	}
}

func TestPrepareSocket_RejectsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	// Drop a regular file at the target path.
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}
	if err := PrepareSocket(path); err == nil {
		t.Fatalf("PrepareSocket accepted a regular file")
	}
	// File must still be there — the whole point of the check.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("regular file was deleted by PrepareSocket: %v", err)
	}
}

func TestClient_NoDaemon(t *testing.T) {
	t.Setenv(SocketEnvVar, filepath.Join(t.TempDir(), "does-not-exist.sock"))
	_, _, err := NewClientFromEnv()
	if err != ErrDaemonUnreachable {
		t.Errorf("expected ErrDaemonUnreachable, got %v", err)
	}
}
