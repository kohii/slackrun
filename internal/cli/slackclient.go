package cli

import (
	"errors"
	"os"

	"github.com/slack-go/slack"
)

// errNoSlackToken is what every write subcommand returns when the child was
// not given a token. The wording points the user at the rules-level opt-in.
var errNoSlackToken = errors.New(
	"SLACK_BOT_TOKEN is not set in this child process; " +
		"set expose_slack_token: true on the rule to forward it",
)

// slackClientFromEnv builds a Slack client from SLACK_BOT_TOKEN. Returns
// errNoSlackToken when the token is absent — the dispatcher strips it from
// child env unless the rule opts in.
func slackClientFromEnv() (*slack.Client, error) {
	tok := os.Getenv("SLACK_BOT_TOKEN")
	if tok == "" {
		return nil, errNoSlackToken
	}
	return slack.New(tok), nil
}
