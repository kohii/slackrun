package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/slack-go/slack"
)

// ChannelClient is the subset of *slack.Client RunChannel needs.
type ChannelClient interface {
	GetConversationInfo(input *slack.GetConversationInfoInput) (*slack.Channel, error)
}

// RunChannel resolves a channel ID to its full metadata via
// conversations.info (name, topic, purpose, member count, …). Useful for
// LLM children who want to answer "what channel am I in?" without extra
// context from the prompt.
//
// Usage:
//
//	slackrun channel [--channel C...] [--include-locale] [--include-num-members]
//
// Defaults: --channel from SLACKRUN_CHANNEL. Output (stdout, one line of
// JSON) is the slack.Channel struct verbatim.
func RunChannel(args []string, stdout, stderr io.Writer) int {
	client, err := slackClientFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runChannelWith(args, stdout, stderr, client)
}

func runChannelWith(args []string, stdout, stderr io.Writer, client ChannelClient) int {
	fs := flag.NewFlagSet("channel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	channel := fs.String("channel", "", "channel ID (defaults to SLACKRUN_CHANNEL)")
	includeLocale := fs.Bool("include-locale", false, "include the locale field on the channel")
	includeNumMembers := fs.Bool("include-num-members", false, "include num_members on the channel (extra API cost)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	chanID := resolveFromEnv(*channel, "SLACKRUN_CHANNEL")
	if chanID == "" {
		fmt.Fprintln(stderr, "--channel is required (or set SLACKRUN_CHANNEL)")
		return 2
	}
	ch, err := client.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID:         chanID,
		IncludeLocale:     *includeLocale,
		IncludeNumMembers: *includeNumMembers,
	})
	if err != nil {
		fmt.Fprintln(stderr, "channel failed:", err)
		return 1
	}
	out, _ := json.Marshal(ch)
	fmt.Fprintln(stdout, string(out))
	return 0
}
