package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/kohii/slackrun/internal/adminapi"
)

// RunKill sends SIGTERM to one or more in-flight runs. Exit codes:
//   0 all requested kills succeeded
//   1 at least one kill failed
//   2 usage
//   3 daemon unreachable
//
// Usage:
//
//	slackrun kill <id...> [--reason "..."]
//	slackrun kill --all [--reason "..."] [--yes]
func RunKill(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "kill every currently-running child")
	reason := fs.String("reason", "", "reason surfaced in Slack thread and logs")
	yes := fs.Bool("yes", false, "skip the --all confirmation prompt")
	// stdlib flag stops at the first positional, so a natural
	// `slackrun kill <id> --reason "..."` invocation would leave --reason
	// unparsed. Interleave: after each Parse, treat one positional as an
	// ID and re-Parse the remainder so flags in any position land.
	var ids []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		ids = append(ids, rest[0])
		rest = rest[1:]
	}
	switch {
	case *all && len(ids) > 0:
		fmt.Fprintln(stderr, "cannot combine --all with explicit ids")
		return 2
	case !*all && len(ids) == 0:
		fmt.Fprintln(stderr, "usage: slackrun kill <id...> | --all [--reason ...]")
		return 2
	}

	client, sockPath, err := adminapi.NewClientFromEnv()
	if err != nil {
		return handleClientErr(stderr, err, sockPath)
	}

	// Each API call gets its own bounded context. Sharing one blanket
	// deadline across the interactive prompt in --all would expire while
	// the operator is still deciding and turn a valid kill into a bogus
	// "daemon unreachable".
	callCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	}

	if *all {
		if !*yes {
			ctx, cancel := callCtx()
			rows, err := client.Runs(ctx)
			cancel()
			if err != nil {
				return handleClientErr(stderr, err, sockPath)
			}
			if len(rows) == 0 {
				fmt.Fprintln(stdout, "(no in-flight runs)")
				return 0
			}
			printRunsTable(stdout, rows)
			if !promptYesNo(stdin, stdout, fmt.Sprintf("Kill all %d run(s)?", len(rows))) {
				fmt.Fprintln(stdout, "aborted")
				return 0
			}
		}
		ctx, cancel := callCtx()
		defer cancel()
		res, err := client.KillAll(ctx, *reason)
		if err != nil {
			return handleClientErr(stderr, err, sockPath)
		}
		for _, id := range res.KilledIDs {
			fmt.Fprintf(stdout, "killed %s\n", id)
		}
		if len(res.KilledIDs) == 0 {
			fmt.Fprintln(stdout, "(no runs to kill)")
		}
		return 0
	}

	failed := false
	for _, id := range ids {
		// One id-worth of budget per call, so a slow first request
		// doesn't cascade a "deadline exceeded" onto every subsequent id.
		ctx, cancel := callCtx()
		_, err := client.Kill(ctx, id, *reason)
		cancel()
		if err != nil {
			failed = true
			var apiErr *adminapi.APIError
			if errors.As(err, &apiErr) {
				fmt.Fprintf(stderr, "kill %s: %s\n", id, apiErr.Message)
				continue
			}
			if errors.Is(err, adminapi.ErrDaemonUnreachable) {
				return handleClientErr(stderr, err, sockPath)
			}
			fmt.Fprintf(stderr, "kill %s: %v\n", id, err)
			continue
		}
		fmt.Fprintf(stdout, "killed %s\n", id)
	}
	if failed {
		return 1
	}
	return 0
}
