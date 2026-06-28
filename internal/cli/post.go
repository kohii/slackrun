package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

// Poster is the subset of *slack.Client RunPost needs. Tests substitute a
// fake.
type Poster interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
}

// RunPost posts a chat message. Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun post --channel C... [--thread-ts T] (--text TEXT | --text - [reads stdin]) [--disable-markdown]
//
// `--text -` reads the body from stdin. `--text "literal"` and stdin are
// mutually exclusive in the sense that the literal wins and stdin is ignored.
// Output (stdout, one line of JSON): {"channel": "C...", "ts": "..."}.
func RunPost(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runPostWith(args, stdin, stdout, stderr, client)
}

func runPostWith(args []string, stdin io.Reader, stdout, stderr io.Writer, client Poster) int {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (required)")
	threadTS := fs.String("thread-ts", "", "thread timestamp to reply under (optional)")
	text := fs.String("text", "", `body text; pass "-" to read from stdin`)
	disableMarkdown := fs.Bool("disable-markdown", false, "send as plain text (no markdown parsing)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *channel == "" {
		fmt.Fprintln(stderr, "--channel is required")
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
		// Sanitize trims whitespace, so the check happens *after* it: a
		// `--text "   "` would otherwise reach Slack and fail with no_text.
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
	ch, ts, err := client.PostMessage(*channel, opts...)
	if err != nil {
		fmt.Fprintln(stderr, "post failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]string{"channel": ch, "ts": ts})
	fmt.Fprintln(stdout, string(out))
	return 0
}
