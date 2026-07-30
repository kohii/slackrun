package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kohii/slackrun/internal/adminapi"
)

func TestRunPrepareRestart_Usage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunPrepareRestart([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "takes no arguments") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPrepareRestart_DaemonUnreachable(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLACKRUN_ENV_PATH", envPath)
	t.Setenv(adminapi.SocketEnvVar, filepath.Join(t.TempDir(), "missing.sock"))
	var stdout, stderr bytes.Buffer
	if code := RunPrepareRestart(nil, &stdout, &stderr); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if !strings.Contains(stderr.String(), "daemon not reachable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
