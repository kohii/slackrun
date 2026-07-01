package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// ReactionsClient is the subset of *slack.Client RunReactions needs.
type ReactionsClient interface {
	GetReactions(item slack.ItemRef, params slack.GetReactionsParameters) (slack.ReactedItem, error)
}

// RunReactions fetches reactions on a message via reactions.get. Named in
// the plural to distinguish from the singular write subcommand
// `slackrun react`. Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun reactions [--channel C...] [--ts T] [--full]
//
// Defaults: --channel from SLACKRUN_CHANNEL, --ts from SLACKRUN_TS.
// Output (stdout, one line of JSON):
//
//	{"reactions": [{"name": "eyes", "count": 2, "users": ["U1", "U2"]}, ...]}
func RunReactions(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runReactionsWith(args, stdout, stderr, client)
}

func runReactionsWith(args []string, stdout, stderr io.Writer, client ReactionsClient) int {
	fs := flag.NewFlagSet("reactions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	ts := fs.String("ts", "", "message timestamp (defaults to SLACKRUN_TS)")
	full := fs.Bool("full", false, "return the full user list even when a message has many reactors")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	tsID := resolveFromEnv(*ts, "SLACKRUN_TS")
	if tsID == "" {
		fmt.Fprintln(stderr, "--ts is required (or set SLACKRUN_TS)")
		return 2
	}
	item, err := client.GetReactions(
		slack.ItemRef{Channel: chanID, Timestamp: tsID},
		slack.GetReactionsParameters{Full: *full},
	)
	if err != nil {
		fmt.Fprintln(stderr, "reactions failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{"reactions": item.Reactions})
	fmt.Fprintln(stdout, string(out))
	return 0
}
