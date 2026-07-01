package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// UsersClient is the subset of *slack.Client RunUsers needs.
type UsersClient interface {
	GetUsers(options ...slack.GetUsersOption) ([]slack.User, error)
}

// RunUsers lists workspace members via users.list. slack-go paginates
// internally and returns the fully aggregated list — the caller does not
// manage cursors here. On very large workspaces this can be slow / heavy;
// tighten scope with --team-id (Enterprise Grid) or handle pagination
// manually via `curl` if needed.
//
// Usage:
//
//	slackrun users [--limit N] [--presence] [--team-id T]
//
// Output (stdout, one line of JSON):
//
//	{"users": [...]}
func RunUsers(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUsersWith(args, stdout, stderr, client)
}

func runUsersWith(args []string, stdout, stderr io.Writer, client UsersClient) int {
	fs := flag.NewFlagSet("users", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 0, "page size (Slack default is 200)")
	presence := fs.Bool("presence", false, "include presence on each user")
	teamID := fs.String("team-id", "", "restrict to a specific team (Enterprise Grid)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var opts []slack.GetUsersOption
	if *limit > 0 {
		opts = append(opts, slack.GetUsersOptionLimit(*limit))
	}
	if *presence {
		opts = append(opts, slack.GetUsersOptionPresence(true))
	}
	if *teamID != "" {
		opts = append(opts, slack.GetUsersOptionTeamID(*teamID))
	}
	users, err := client.GetUsers(opts...)
	if err != nil {
		fmt.Fprintln(stderr, "users failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{"users": users})
	fmt.Fprintln(stdout, string(out))
	return 0
}
