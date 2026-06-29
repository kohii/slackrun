package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleRules = `
rules:
  - name: mention-default
    trigger:
      type: app_mention
    action:
      cwd: %s
      command: ["echo"]
      timeout_ms: 60000
      stdin:
        parts:
          - template: "{{text}}"

  - name: sentry-alert
    trigger:
      type: message
      channel: C01ALERT0
      from:
        bot_user_ids: [U01SENTRY1]
    action:
      cwd: %s
      command: ["echo"]
      timeout_ms: 600000
      stdin:
        parts:
          - template: "/alert {{permalink}}"
`

func writeRules(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := strings.ReplaceAll(sampleRules, "%s", dir)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCheck_OK(t *testing.T) {
	t.Parallel()
	path := writeRules(t)
	var out, errBuf bytes.Buffer
	code := RunCheck([]string{path}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "2 rule(s) loaded") {
		t.Fatalf("unexpected stdout: %q", out.String())
	}
}

func TestRunCheck_BadFile(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	code := RunCheck([]string{"/nope/missing.yaml"}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
}

func TestRunDryRun_Match(t *testing.T) {
	t.Parallel()
	path := writeRules(t)

	eventPath := filepath.Join(t.TempDir(), "ev.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"type":"app_mention","user":"U01OK","channel":"C01X","ts":"100.001",
		"text":"<@U00SELFTEST> hello world"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := RunDryRun([]string{
		path,
		"--event", eventPath,
		"--allowed-user-ids", "U01OK",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if got["kind"] != "matched" {
		t.Fatalf("kind=%v\n%s", got["kind"], out.String())
	}
	if got["rule"] != "mention-default" {
		t.Fatalf("rule=%v", got["rule"])
	}
	cmd := got["command"].([]any)
	if len(cmd) != 1 || cmd[0] != "echo" {
		t.Fatalf("command=%v", cmd)
	}
	if s, _ := got["stdin"].(string); s != "hello world" {
		t.Fatalf("stdin=%q", s)
	}
}

func TestRunDryRun_NoMatch(t *testing.T) {
	t.Parallel()
	path := writeRules(t)

	eventPath := filepath.Join(t.TempDir(), "ev.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"type":"message","subtype":"bot_message","channel":"C00OTHER","ts":"100.002"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := RunDryRun([]string{path, "--event", eventPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["kind"] != "no-match" {
		t.Fatalf("kind=%v", got["kind"])
	}
}

func TestRunDryRun_Unauthorized(t *testing.T) {
	t.Parallel()
	path := writeRules(t)

	eventPath := filepath.Join(t.TempDir(), "ev.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"type":"app_mention","user":"U99STRANGER","channel":"C01X","ts":"100.003",
		"text":"<@U00SELFTEST> hi"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := RunDryRun([]string{path, "--event", eventPath, "--allowed-user-ids", "U01OK"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errBuf.String())
	}
	var got map[string]any
	_ = json.Unmarshal(out.Bytes(), &got)
	if got["kind"] != "unauthorized" {
		t.Fatalf("kind=%v", got["kind"])
	}
}
