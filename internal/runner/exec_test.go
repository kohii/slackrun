package runner

import (
	"strings"
	"testing"
	"time"
)

func TestRun_HelloStdout(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command: []string{"sh", "-c", "echo hello; >&2 echo bye"},
		Cwd:     "/tmp",
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout=%q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "bye") {
		t.Fatalf("stderr=%q", res.Stderr)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command: []string{"sh", "-c", "exit 7"},
		Cwd:     "/tmp",
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 7 {
		t.Fatalf("expected 7, got %d", res.ExitCode)
	}
}

func TestRun_BinaryNotFound(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command: []string{"this-binary-most-definitely-does-not-exist-xyz"},
		Cwd:     "/tmp",
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if !res.NotFound {
		t.Fatalf("expected NotFound, got %+v", res)
	}
}

func TestRun_Timeout(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command:      []string{"sh", "-c", "sleep 5"},
		Cwd:          "/tmp",
		Timeout:      100 * time.Millisecond,
		SigKillGrace: 200 * time.Millisecond,
	})
	start := time.Now()
	res := <-h.Done
	elapsed := time.Since(start)
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRun_KillStops(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command:      []string{"sh", "-c", "sleep 5"},
		Cwd:          "/tmp",
		Timeout:      10 * time.Second,
		SigKillGrace: 200 * time.Millisecond,
	})
	time.Sleep(50 * time.Millisecond)
	h.Kill()
	h.Kill() // idempotent
	res := <-h.Done
	if res.TimedOut {
		t.Fatalf("expected non-timeout kill")
	}
	if res.ExitCode == 0 {
		t.Fatalf("expected non-zero exit after kill, got %d", res.ExitCode)
	}
}

func TestRun_EnvOverride(t *testing.T) {
	t.Parallel()
	h := Run(Options{
		Command: []string{"sh", "-c", `printf "%s" "$SLACKRUN_TEST_KEY"`},
		Cwd:     "/tmp",
		Env:     map[string]string{"SLACKRUN_TEST_KEY": "from-rule"},
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.Stdout != "from-rule" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestRun_EmptyArgvIsError(t *testing.T) {
	t.Parallel()
	h := Run(Options{Command: nil, Cwd: "/tmp", Timeout: time.Second})
	res := <-h.Done
	if res.StartErr == nil {
		t.Fatal("expected StartErr")
	}
}
