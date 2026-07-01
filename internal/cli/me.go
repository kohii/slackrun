package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// AuthChecker is the subset of *slack.Client RunMe needs.
type AuthChecker interface {
	AuthTest() (*slack.AuthTestResponse, error)
}

// RunMe returns the bot's own identity (auth.test): user ID, bot ID, team,
// and workspace URL. Useful for the child to know who it is speaking as —
// e.g. to avoid replying to its own past messages.
//
// Usage:
//
//	slackrun me
//
// Output (stdout, one line of JSON) mirrors slack-go's AuthTestResponse.
func RunMe(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runMeWith(args, stdout, stderr, client)
}

func runMeWith(args []string, stdout, stderr io.Writer, client AuthChecker) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "slackrun me takes no arguments")
		return 2
	}
	resp, err := client.AuthTest()
	if err != nil {
		fmt.Fprintln(stderr, "me failed:", err)
		return 1
	}
	out, _ := json.Marshal(resp)
	fmt.Fprintln(stdout, string(out))
	return 0
}
