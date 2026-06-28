package runner

import (
	"os"
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

func TestRun_StripsProtectedParentEnv(t *testing.T) {
	// Parent SLACK_* / ALLOWED_USER_IDS must never reach the child unless
	// ExposeSlackToken opts in (and even then only SLACK_BOT_TOKEN).
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-parent-secret")
	t.Setenv("SLACK_APP_TOKEN", "xapp-parent-secret")
	t.Setenv("ALLOWED_USER_IDS", "U01PARENT")

	h := Run(Options{
		Command: []string{"sh", "-c",
			`printf "BOT=%s\nAPP=%s\nALLOW=%s\n" "${SLACK_BOT_TOKEN-unset}" "${SLACK_APP_TOKEN-unset}" "${ALLOWED_USER_IDS-unset}"`},
		Cwd:     "/tmp",
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "BOT=unset") {
		t.Fatalf("SLACK_BOT_TOKEN leaked: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "APP=unset") {
		t.Fatalf("SLACK_APP_TOKEN leaked: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "ALLOW=unset") {
		t.Fatalf("ALLOWED_USER_IDS leaked: %q", res.Stdout)
	}
}

func TestRun_ExposeSlackTokenPasses(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-parent-secret")
	t.Setenv("SLACK_APP_TOKEN", "xapp-parent-secret")

	h := Run(Options{
		Command:          []string{"sh", "-c", `printf "%s|%s" "${SLACK_BOT_TOKEN-unset}" "${SLACK_APP_TOKEN-unset}"`},
		Cwd:              "/tmp",
		ExposeSlackToken: true,
		Timeout:          5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.Stdout != "xoxb-parent-secret|unset" {
		t.Fatalf("got %q", res.Stdout)
	}
}

func TestRun_OverridesCannotReintroduceProtectedKeys(t *testing.T) {
	// Even if a caller smuggles a protected key through Options.Env, buildEnv
	// must drop it on the final strip pass.
	t.Setenv("SLACK_BOT_TOKEN", "")
	os.Unsetenv("SLACK_BOT_TOKEN")

	h := Run(Options{
		Command: []string{"sh", "-c", `printf "%s|%s" "${SLACK_BOT_TOKEN-unset}" "${ALLOWED_USER_IDS-unset}"`},
		Cwd:     "/tmp",
		Env: map[string]string{
			"SLACK_BOT_TOKEN":  "xoxb-from-rule",
			"ALLOWED_USER_IDS": "U01RULE",
		},
		Timeout: 5 * time.Second,
	})
	res := <-h.Done
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if res.Stdout != "unset|unset" {
		t.Fatalf("override leaked protected keys: %q", res.Stdout)
	}
}

func TestIsProtectedEnvKey(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"SLACK_BOT_TOKEN":    true,
		"SLACK_APP_TOKEN":    true,
		"ALLOWED_USER_IDS":   true,
		"SLACKRUN_CHANNEL":   true,
		"SLACKRUN_TS":        true,
		"SLACKRUN_THREAD_TS": true,
		"SLACKRUN_USER":      true,
		"PATH":               false,
		"HOME":               false,
		"MY_VAR":             false,
		"SLACK_OTHER":        false,
		"SLACKRUN_OTHER":     false,
	}
	for k, want := range cases {
		if got := IsProtectedEnvKey(k); got != want {
			t.Errorf("IsProtectedEnvKey(%q) = %v, want %v", k, got, want)
		}
	}
}
