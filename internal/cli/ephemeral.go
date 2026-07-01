package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

// EphemeralPoster is the subset of *slack.Client RunEphemeral needs.
type EphemeralPoster interface {
	PostEphemeral(channelID, userID string, options ...slack.MsgOption) (string, error)
}

// RunEphemeral posts a message visible only to a single user (chat.postEphemeral).
// Useful for confirmation prompts and diagnostic hints the wider channel
// shouldn't see.
//
// Usage:
//
//	slackrun ephemeral [--channel C...] [--user U...] (--text TEXT | --text - reads stdin) [--thread-ts T] [--disable-markdown]
//
// Defaults: --channel from SLACKRUN_CHANNEL, --user from SLACKRUN_USER
// (the person who triggered this run — usually the desired audience).
func RunEphemeral(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runEphemeralWith(args, stdin, stdout, stderr, client)
}

func runEphemeralWith(args []string, stdin io.Reader, stdout, stderr io.Writer, client EphemeralPoster) int {
	fs := flag.NewFlagSet("ephemeral", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	user := fs.String("user", "", "target user ID (defaults to SLACKRUN_USER)")
	text := fs.String("text", "", `body text; pass "-" to read from stdin`)
	threadTS := fs.String("thread-ts", "", "thread timestamp (optional)")
	disableMarkdown := fs.Bool("disable-markdown", false, "send as plain text (no markdown parsing)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	userID := resolveFromEnv(*user, "SLACKRUN_USER")
	if userID == "" {
		fmt.Fprintln(stderr, "--user is required (or set SLACKRUN_USER)")
		return 2
	}
	body := *text
	if body == "-" {
		buf, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintln(stderr, "read stdin:", err)
			return 1
		}
		body = string(buf)
	}
	body = util.SanitizeForSlack(body)
	if body == "" {
		fmt.Fprintln(stderr, "--text is required (use \"-\" to read from stdin)")
		return 2
	}
	opts := []slack.MsgOption{slack.MsgOptionText(body, false)}
	if *threadTS != "" {
		opts = append(opts, slack.MsgOptionTS(*threadTS))
	}
	if *disableMarkdown {
		opts = append(opts, slack.MsgOptionDisableMarkdown())
	}
	ts, err := client.PostEphemeral(chanID, userID, opts...)
	if err != nil {
		fmt.Fprintln(stderr, "ephemeral failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]string{"channel": chanID, "user": userID, "message_ts": ts})
	fmt.Fprintln(stdout, string(out))
	return 0
}
