package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kohii/slackrun/internal/adminapi"
	"github.com/kohii/slackrun/internal/config"
)

// RunPrepareRestart atomically prepares and stops an idle daemon. Exit codes:
//
//	0 prepared
//	1 busy or API error
//	2 usage
//	3 daemon unreachable
func RunPrepareRestart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prepare-restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	expectedPID := fs.Int("pid", 0, "require the daemon to have this process id")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "prepare-restart takes no arguments")
		return 2
	}
	if *expectedPID < 0 {
		fmt.Fprintln(stderr, "pid must be positive")
		return 2
	}
	if _, err := config.LoadDotenv(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client, sockPath, err := adminapi.NewClientFromEnv()
	if err != nil {
		return handleClientErr(stderr, err, sockPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := client.PrepareRestart(ctx, *expectedPID)
	if err != nil {
		return handleClientErr(stderr, err, sockPath)
	}
	if !result.Prepared {
		fmt.Fprintln(stderr, "daemon did not prepare for restart")
		return 1
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(sockPath); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stdout, "daemon %d stopped safely\n", result.PID)
			return 0
		} else if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		select {
		case <-deadline.C:
			fmt.Fprintln(stderr, "timed out waiting for daemon to stop")
			return 1
		case <-ticker.C:
		case <-ctx.Done():
			fmt.Fprintln(stderr, ctx.Err())
			return 1
		}
	}
}
