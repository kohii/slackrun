package cli

import (
	"errors"
	"os"

	"github.com/slack-go/slack"
)

// resolveFromEnv returns `flagVal` when set; otherwise the value of envKey.
// Used by post/react/upload so children invoked by slackrun can omit
// --channel / --ts / --thread-ts and pick up the triggering event from the
// SLACKRUN_* env vars slackrun injects.
func resolveFromEnv(flagVal, envKey string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envKey)
}

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
	return slack.New(tok, slack.OptionHTTPClient(httpClient())), nil
}
