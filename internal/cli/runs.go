package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kohii/slackrun/internal/adminapi"
	"github.com/kohii/slackrun/internal/util"
)

// RunRuns lists in-flight runs in the daemon. Exit codes:
//   0 success (empty table on no runs)
//   1 API error
//   2 usage
//   3 daemon unreachable (start not running)
//
// Usage:
//
//	slackrun runs [--json]
func RunRuns(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit JSON array instead of a table")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "runs takes no positional arguments")
		return 2
	}

	client, sockPath, err := adminapi.NewClientFromEnv()
	if err != nil {
		return handleClientErr(stderr, err, sockPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := client.Runs(ctx)
	if err != nil {
		return handleClientErr(stderr, err, sockPath)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	printRunsTable(stdout, rows)
	return 0
}

func printRunsTable(w io.Writer, rows []adminapi.RunView) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tRULE\tDURATION\tSTATE\tCHANNEL\tPID")
	for _, r := range rows {
		dur := time.Duration(r.ElapsedMs) * time.Millisecond
		pid := "-"
		if r.PID > 0 {
			pid = fmt.Sprintf("%d", r.PID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Rule, util.FormatDuration(dur), r.State, r.ChannelID, pid)
	}
	_ = tw.Flush()
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no in-flight runs)")
	}
}

// handleClientErr renders a helpful message and picks the CLI exit code
// depending on which kind of failure happened. Split out so runs / kill can
// share the copy.
func handleClientErr(stderr io.Writer, err error, socketHint string) int {
	if errors.Is(err, adminapi.ErrDisabled) {
		fmt.Fprintf(stderr, "admin API is disabled (%s=off); start slackrun without that setting to use runs/kill.\n", adminapi.SocketEnvVar)
		return 3
	}
	if errors.Is(err, adminapi.ErrDaemonUnreachable) {
		hint := ""
		if socketHint != "" {
			hint = " (expected socket: " + socketHint + ")"
		}
		fmt.Fprintf(stderr, "slackrun daemon not reachable%s\nStart it with: slackrun start\n", hint)
		return 3
	}
	var apiErr *adminapi.APIError
	if errors.As(err, &apiErr) {
		fmt.Fprintln(stderr, apiErr.Error())
		return 1
	}
	fmt.Fprintln(stderr, err)
	return 1
}

// promptYesNo prints question to stdout and reads a line from stdin. Returns
// true for empty (default no unless defaultYes), "y" or "yes"; false
// otherwise. Any read error yields false too, since a controller running
// this via cron won't have a tty.
func promptYesNo(stdin io.Reader, stdout io.Writer, question string) bool {
	fmt.Fprint(stdout, question+" [y/N]: ")
	buf := make([]byte, 32)
	n, err := stdin.Read(buf)
	if err != nil {
		return false
	}
	resp := strings.TrimSpace(strings.ToLower(string(buf[:n])))
	return resp == "y" || resp == "yes"
}
