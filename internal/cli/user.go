package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// UserClient is the subset of *slack.Client RunUser needs.
type UserClient interface {
	GetUserInfo(user string) (*slack.User, error)
}

// RunUser fetches a user's profile via users.info. Exit codes: 0 success,
// 1 API error, 2 usage.
//
// Usage:
//
//	slackrun user [--user U...]
//
// Defaults: --user from SLACKRUN_USER (the author of the triggering event).
// Output (stdout, one line of JSON) is the slack.User struct verbatim so
// the child can pick out `real_name`, `profile.email`, etc. as needed.
func RunUser(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUserWith(args, stdout, stderr, client)
}

func runUserWith(args []string, stdout, stderr io.Writer, client UserClient) int {
	fs := flag.NewFlagSet("user", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "user ID (defaults to SLACKRUN_USER)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	userID := resolveFromEnv(*user, "SLACKRUN_USER")
	if userID == "" {
		fmt.Fprintln(stderr, "--user is required (or set SLACKRUN_USER)")
		return 2
	}
	u, err := client.GetUserInfo(userID)
	if err != nil {
		fmt.Fprintln(stderr, "user failed:", err)
		return 1
	}
	out, _ := json.Marshal(u)
	fmt.Fprintln(stdout, string(out))
	return 0
}
