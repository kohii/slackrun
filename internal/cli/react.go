package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/slack-go/slack"
)

// Reacter is the subset of *slack.Client RunReact needs.
type Reacter interface {
	AddReaction(name string, item slack.ItemRef) error
}

// RunReact adds a reaction emoji to a message. Exit codes match RunPost.
//
// Usage:
//
//	slackrun react --channel C... --ts T --emoji NAME
//
// `NAME` is given without surrounding colons; `:eyes:` and `eyes` both work.
func RunReact(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runReactWith(args, stdout, stderr, client)
}

func runReactWith(args []string, stdout, stderr io.Writer, client Reacter) int {
	fs := flag.NewFlagSet("react", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (required)")
	ts := fs.String("ts", "", "message timestamp to react to (required)")
	emoji := fs.String("emoji", "", "emoji name without colons (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case *channel == "":
		fmt.Fprintln(stderr, "--channel is required")
		return 2
	case *ts == "":
		fmt.Fprintln(stderr, "--ts is required")
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
		// `:foo:bar:` or `foo:bar` — Slack would reject these too, but a
		// local error is faster than a network round trip.
		fmt.Fprintln(stderr, "--emoji must not contain ':'")
		return 2
	}
	if err := client.AddReaction(name, slack.ItemRef{Channel: *channel, Timestamp: *ts}); err != nil {
		fmt.Fprintln(stderr, "react failed:", err)
		return 1
	}
	return 0
}
