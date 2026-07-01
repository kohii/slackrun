package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// ChannelsClient is the subset of *slack.Client RunChannels needs.
type ChannelsClient interface {
	GetConversations(params *slack.GetConversationsParameters) (channels []slack.Channel, nextCursor string, err error)
}

// RunChannels lists conversations the bot has access to via
// conversations.list. Exit codes: 0 success, 1 API error, 2 usage.
//
// Usage:
//
//	slackrun channels [--types public_channel,private_channel,im,mpim] [--exclude-archived] [--limit N] [--cursor CUR]
//
// Defaults: --types public_channel (mirrors Slack's own default).
// Output (stdout, one line of JSON):
//
//	{"channels": [...], "next_cursor": "..."}
func RunChannels(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runChannelsWith(args, stdout, stderr, client)
}

func runChannelsWith(args []string, stdout, stderr io.Writer, client ChannelsClient) int {
	fs := flag.NewFlagSet("channels", flag.ContinueOnError)
	fs.SetOutput(stderr)
	types := fs.String("types", "public_channel", "comma-separated conversation types: public_channel,private_channel,im,mpim")
	excludeArchived := fs.Bool("exclude-archived", false, "omit archived channels")
	limit := fs.Int("limit", 100, "page size (1-1000)")
	cursor := fs.String("cursor", "", "pagination cursor")
	teamID := fs.String("team-id", "", "restrict to a specific team (Enterprise Grid)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *limit < 1 || *limit > 1000 {
		fmt.Fprintln(stderr, "--limit must be between 1 and 1000")
		return 2
	}
	channels, nextCursor, err := client.GetConversations(&slack.GetConversationsParameters{
		Types:           splitCSV(*types),
		ExcludeArchived: *excludeArchived,
		Limit:           *limit,
		Cursor:          *cursor,
		TeamID:          *teamID,
	})
	if err != nil {
		fmt.Fprintln(stderr, "channels failed:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{
		"channels":    channels,
		"next_cursor": nextCursor,
	})
	fmt.Fprintln(stdout, string(out))
	return 0
}
