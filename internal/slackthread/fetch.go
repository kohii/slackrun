package slackthread

import (
	"context"

	"github.com/slack-go/slack"
)

// Replier is the slack-go subset slackthread.Fetch depends on. Defined here
// so tests can substitute fakes without bringing the full slack.Client.
type Replier interface {
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
}

// FetchOptions controls a single call to Fetch. PerPage and MaxPages bound
// the total number of messages we will pull; the caller is also expected to
// supply ctx with a deadline.
//
// Pagination caveat: conversations.replies returns messages chronologically
// (oldest first). When the actual thread length exceeds PerPage * MaxPages,
// truncation happens at the *tail* (latest messages are lost). Render's
// per-message cap then prefers the *tail* of what Fetch returned, so the
// two layers' priorities are misaligned for very large threads. Default
// 100 * 5 = 500 messages handles essentially all personal-use threads;
// raise MaxPages if you regularly see threads beyond that size.
type FetchOptions struct {
	Channel    string
	ThreadTS   string
	PerPage    int // slack.GetConversationRepliesParameters.Limit; default 100
	MaxPages   int // bound on pagination loops; default 5
	SelfUserID string
	SelfBotID  string
}

// FetchResult bundles the normalized thread plus a hasMore flag that is true
// only when pagination was capped by MaxPages (the API still had more).
// Callers can surface a truncation notice to the operator.
type FetchResult struct {
	Messages []Message
	HasMore  bool
}

// Fetch calls conversations.replies with bounded pagination and returns the
// chronological list of normalized messages. The caller's ctx provides the
// deadline; if it expires mid-pagination we stop and return what we got.
//
// On a thread with no replies, conversations.replies returns the parent
// alone — this is the normal case, not a failure mode.
func Fetch(ctx context.Context, api Replier, opts FetchOptions) (FetchResult, error) {
	if opts.PerPage <= 0 {
		opts.PerPage = 100
	}
	if opts.MaxPages <= 0 {
		opts.MaxPages = 5
	}

	var out []Message
	cursor := ""
	hasMore := false
	for page := 0; page < opts.MaxPages; page++ {
		msgs, more, next, err := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID: opts.Channel,
			Timestamp: opts.ThreadTS,
			Cursor:    cursor,
			Limit:     opts.PerPage,
		})
		if err != nil {
			return FetchResult{Messages: out}, err
		}
		for _, m := range msgs {
			out = append(out, FromSlackMessage(m, opts.SelfUserID, opts.SelfBotID))
		}
		if !more || next == "" {
			hasMore = false
			break
		}
		cursor = next
		if page == opts.MaxPages-1 {
			hasMore = true
		}
	}
	return FetchResult{Messages: out, HasMore: hasMore}, nil
}

// FromSlackMessage normalizes a slack.Message into the internal Message form.
// Source is resolved by precedence: self → user → bot, with the bot name
// derived from username, BotProfile.Name, BotProfile.AppID, or BotID in that
// order.
func FromSlackMessage(m slack.Message, selfUserID, selfBotID string) Message {
	out := Message{
		TS:     m.Timestamp,
		Text:   m.Text,
		Edited: m.Edited != nil,
	}

	selfHit := (selfUserID != "" && m.User == selfUserID) ||
		(selfBotID != "" && m.BotID == selfBotID)
	if selfHit {
		out.Source = SourceSelf
		return out
	}
	if m.User != "" {
		out.Source = SourceUser
		out.User = m.User
		return out
	}
	out.Source = SourceBot
	out.Bot = botNameFromMsg(m)
	return out
}

func botNameFromMsg(m slack.Message) string {
	if m.Username != "" {
		return m.Username
	}
	if m.BotProfile != nil {
		if m.BotProfile.Name != "" {
			return m.BotProfile.Name
		}
		if m.BotProfile.AppID != "" {
			return m.BotProfile.AppID
		}
	}
	return m.BotID
}
