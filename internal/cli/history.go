package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// HistoryClient is the subset of *slack.Client RunHistory needs. Tests
// substitute a fake.
type HistoryClient interface {
	GetConversationHistory(params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

// RunHistory fetches messages from a channel via conversations.history.
// Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun history [--channel C...] [--limit N] [--cursor CUR] [--oldest TS] [--latest TS]
//
// Defaults: --channel from SLACKRUN_CHANNEL.
// Output (stdout, one line of JSON):
//
//	{"messages": [...], "next_cursor": "...", "has_more": bool}
func RunHistory(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runHistoryWith(args, stdout, stderr, client)
}

func runHistoryWith(args []string, stdout, stderr io.Writer, client HistoryClient) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	limit := fs.Int("limit", 100, "max messages to return (1-1000)")
	cursor := fs.String("cursor", "", "pagination cursor (from a previous response's next_cursor)")
	oldest := fs.String("oldest", "", "return messages newer than this ts (exclusive)")
	latest := fs.String("latest", "", "return messages older than this ts (exclusive)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	if *limit < 1 || *limit > 1000 {
		fmt.Fprintln(stderr, "--limit must be between 1 and 1000")
		return 2
	}
	resp, err := client.GetConversationHistory(&slack.GetConversationHistoryParameters{
		ChannelID: chanID,
		Limit:     *limit,
		Cursor:    *cursor,
		Oldest:    *oldest,
		Latest:    *latest,
	})
	if err != nil {
		fmt.Fprintln(stderr, "history failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{
		"messages":    resp.Messages,
		"next_cursor": resp.ResponseMetaData.NextCursor,
		"has_more":    resp.HasMore,
	})
	fmt.Fprintln(stdout, string(out))
	return 0
}
