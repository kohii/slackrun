package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// RepliesClient is the subset of *slack.Client RunReplies needs.
type RepliesClient interface {
	GetConversationReplies(params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
}

// RunReplies fetches messages in a thread via conversations.replies.
// Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun replies [--channel C...] [--thread-ts T] [--limit N] [--cursor CUR]
//
// Defaults: --channel from SLACKRUN_CHANNEL, --thread-ts from
// SLACKRUN_THREAD_TS. Output shape mirrors `slackrun history`:
//
//	{"messages": [...], "next_cursor": "...", "has_more": bool}
func RunReplies(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runRepliesWith(args, stdout, stderr, client)
}

func runRepliesWith(args []string, stdout, stderr io.Writer, client RepliesClient) int {
	fs := flag.NewFlagSet("replies", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	threadTS := fs.String("thread-ts", "", "thread parent ts (defaults to SLACKRUN_THREAD_TS)")
	limit := fs.Int("limit", 100, "max messages to return (1-1000)")
	cursor := fs.String("cursor", "", "pagination cursor")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	tsID := resolveFromEnv(*threadTS, "SLACKRUN_THREAD_TS")
	if tsID == "" {
		fmt.Fprintln(stderr, "--thread-ts is required (or set SLACKRUN_THREAD_TS)")
		return 2
	}
	if *limit < 1 || *limit > 1000 {
		fmt.Fprintln(stderr, "--limit must be between 1 and 1000")
		return 2
	}
	msgs, hasMore, nextCursor, err := client.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: chanID,
		Timestamp: tsID,
		Cursor:    *cursor,
		Limit:     *limit,
	})
	if err != nil {
		fmt.Fprintln(stderr, "replies failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{
		"messages":    msgs,
		"next_cursor": nextCursor,
		"has_more":    hasMore,
	})
	fmt.Fprintln(stdout, string(out))
	return 0
}
