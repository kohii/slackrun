package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/kohii/slackrun/internal/util"
	"github.com/slack-go/slack"
)

// Updater is the subset of *slack.Client RunUpdate needs.
type Updater interface {
	UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
}

// RunUpdate edits an existing chat message (chat.update). Useful when the
// child wants to iterate on its own reply — post once, then keep amending
// as more information becomes available.
//
// Usage:
//
//	slackrun update [--channel C...] --ts T (--text TEXT | --text - reads stdin) [--disable-markdown]
//
// Defaults: --channel from SLACKRUN_CHANNEL. `--ts` is intentionally NOT
// defaulted from SLACKRUN_TS: that value is the triggering event's ts, and
// editing the triggering user's message is almost never the intent.
// Callers keep the ts from their earlier `slackrun post` response.
func RunUpdate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUpdateWith(args, stdin, stdout, stderr, client)
}

func runUpdateWith(args []string, stdin io.Reader, stdout, stderr io.Writer, client Updater) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	ts := fs.String("ts", "", "ts of the message to edit (required)")
	text := fs.String("text", "", `new body text; pass "-" to read from stdin`)
	disableMarkdown := fs.Bool("disable-markdown", false, "send as plain text (no markdown parsing)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	if *ts == "" {
		fmt.Fprintln(stderr, "--ts is required (the ts of the message to edit)")
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
	if *disableMarkdown {
		opts = append(opts, slack.MsgOptionDisableMarkdown())
	}
	ch, respTS, _, err := client.UpdateMessage(chanID, *ts, opts...)
	if err != nil {
		fmt.Fprintln(stderr, "update failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]string{"channel": ch, "ts": respTS})
	fmt.Fprintln(stdout, string(out))
	return 0
}
