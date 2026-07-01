package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/slack-go/slack"
)

// Unreacter is the subset of *slack.Client RunUnreact needs.
type Unreacter interface {
	RemoveReaction(name string, item slack.ItemRef) error
}

// RunUnreact removes a reaction emoji the bot previously added
// (reactions.remove). Paired with `slackrun react` for on-then-off status
// indicators (e.g. `⏳` while working, remove and add `✅` when done).
//
// Usage:
//
//	slackrun unreact [--channel C...] [--ts T] --emoji NAME
//
// Defaults: --channel from SLACKRUN_CHANNEL, --ts from SLACKRUN_TS. `NAME`
// is given without surrounding colons; `:eyes:` and `eyes` both work.
func RunUnreact(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runUnreactWith(args, stdout, stderr, client)
}

func runUnreactWith(args []string, stdout, stderr io.Writer, client Unreacter) int {
	fs := flag.NewFlagSet("unreact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	ts := fs.String("ts", "", "message timestamp (defaults to SLACKRUN_TS)")
	emoji := fs.String("emoji", "", "emoji name without colons (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	tsID := resolveFromEnv(*ts, "SLACKRUN_TS")
	switch {
	case chanID == "":
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	case tsID == "":
		fmt.Fprintln(stderr, "--ts is required (or set SLACKRUN_TS)")
		return 2
	case *emoji == "":
		fmt.Fprintln(stderr, "--emoji is required")
		return 2
	}
	name := strings.Trim(*emoji, ":")
	if name == "" {
		fmt.Fprintln(stderr, "--emoji is empty after stripping colons")
		return 2
	}
	if strings.ContainsRune(name, ':') {
		fmt.Fprintln(stderr, "--emoji must not contain ':'")
		return 2
	}
	if err := client.RemoveReaction(name, slack.ItemRef{Channel: chanID, Timestamp: tsID}); err != nil {
		fmt.Fprintln(stderr, "unreact failed:", err)
		return 1
	}
	return 0
}
