package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveFakeAdmin spins up a UDS HTTP server that records the last request
// path and returns a stock kill response. Used to verify the CLI's flag
// parsing and network calls without booting the real slackrun start.
func serveFakeAdmin(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// t.TempDir() honours $TMPDIR; on macOS that's under /var/folders/...
	// which pushes the socket path past the 104-byte sun_path limit when
	// the test name is long. /tmp is short enough on both macOS (symlink
	// to /private/tmp) and Linux; use it explicitly.
	dir, err := os.MkdirTemp("/tmp", "sr")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = ln.Close()
	})
	// Point the CLI at this socket.
	t.Setenv("SLACKRUN_ADMIN_SOCKET", sockPath)
	// Wait briefly for the listener to be accept-ready.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return sockPath
}

func TestRunKill_InterleavesReasonAfterID(t *testing.T) {
	var gotPath string
	var gotBody bytes.Buffer
	serveFakeAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.Copy(&gotBody, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"killed":true,"id":"ABC12345","full_id":"C:1:r"}`))
	})

	var stdout, stderr bytes.Buffer
	// The failure case in the codex review: id first, then --reason.
	code := RunKill([]string{"ABC12345", "--reason", "manual op"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if gotPath != "/v1/runs/ABC12345/kill" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody.String(), `"reason":"manual op"`) {
		t.Errorf("body missing reason: %s", gotBody.String())
	}
}

func TestRunKill_MultipleIDsWithFlagsBetween(t *testing.T) {
	var paths []string
	serveFakeAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"killed":true}`))
	})

	var stdout, stderr bytes.Buffer
	code := RunKill([]string{"AAA00001", "--reason", "x", "BBB00002"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if len(paths) != 2 || paths[0] != "/v1/runs/AAA00001/kill" || paths[1] != "/v1/runs/BBB00002/kill" {
		t.Errorf("paths = %v", paths)
	}
}

func TestRunKill_AllRequiresYesOrConfirmation(t *testing.T) {
	serveFakeAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/runs" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"killed":true,"killed_ids":[]}`))
	})
	var stdout, stderr bytes.Buffer
	// Empty --all with --yes should short-circuit and print "no runs".
	code := RunKill([]string{"--all", "--yes"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no runs") {
		t.Errorf("stdout=%q", stdout.String())
	}
}
