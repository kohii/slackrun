package dispatch

import (
	"regexp"
	"strings"

	"github.com/kohii/slackrun/internal/config"
)

// IncomingEvent is the small subset of Slack `message` / `app_mention` payload
// we look at. Kept structural so tests can hand-craft literals without
// dragging slack-go types around.
type IncomingEvent struct {
	Type     string // "message" | "app_mention"
	Subtype  string // empty for plain messages
	Channel  string
	User     string
	BotID    string
	AppID    string
	Username string
	TS       string
	Text     string
	ThreadTS string

	// BotProfile lets us read display name when the sender exposes only that.
	BotProfileName string

	// Some subtypes (notably message_replied) nest the original message
	// fields under .message instead of the root. Pull these in too so the
	// dispatcher can see through that nesting transparently.
	Nested *NestedMessage
}

// NestedMessage mirrors the subset of fields Slack nests under `.message`
// for subtype `message_replied`.
type NestedMessage struct {
	Text           string
	User           string
	BotID          string
	AppID          string
	Username       string
	BotProfileName string
}

// MatcherContext is the static configuration the matcher needs to filter
// self-loops and apply mention authorization.
type MatcherContext struct {
	SelfUserID     string
	SelfBotID      string // optional; if empty, the bot_id-based self-loop guard is skipped
	AllowedUserIDs []string
}

// MatchKind classifies the outcome of a single matchEvent call.
type MatchKind int

const (
	MatchKindMatched MatchKind = iota
	MatchKindSkip
	MatchKindUnauthorized
	MatchKindNoMatch
)

// MatchResult is the discriminated outcome of matching one event against a
// rule list. A non-nil `Rule` is only present for MatchKindMatched.
type MatchResult struct {
	Kind       MatchKind
	Rule       *config.Rule
	Text       string  // normalized message text (mentions stripped for app_mention)
	FirstToken string  // first non-mention token; empty if none
	Rest       string  // text minus the first token (for keyword-based dispatch)
	Reason     string  // populated for Skip / Unauthorized
}

var allowedMessageSubtypes = map[string]bool{
	"":                true, // plain message
	"bot_message":     true,
	"message_replied": true,
}

// mentionRe matches Slack's user-mention tokens (`<@U…>` or `<@U…|name>`).
// W-prefix covers enterprise grid users.
var mentionRe = regexp.MustCompile(`<@[UW][A-Z0-9]+(?:\|[^>]+)?>`)

var whitespaceCollapse = regexp.MustCompile(`\s+`)

// ExtractText pulls the most-specific text field, accounting for subtype
// nesting (message_replied wraps the original in .message).
func ExtractText(ev IncomingEvent) string {
	if ev.Nested != nil && ev.Nested.Text != "" {
		return ev.Nested.Text
	}
	return ev.Text
}

// NormalizeMentionText strips mention tokens, collapses whitespace, and
// returns the cleaned text along with the first token (the keyword candidate)
// and the rest.
func NormalizeMentionText(raw string) (text, firstToken, rest string) {
	cleaned := mentionRe.ReplaceAllString(raw, "")
	cleaned = strings.TrimSpace(whitespaceCollapse.ReplaceAllString(cleaned, " "))
	if cleaned == "" {
		return "", "", ""
	}
	if i := strings.IndexByte(cleaned, ' '); i >= 0 {
		return cleaned, cleaned[:i], cleaned[i+1:]
	}
	return cleaned, cleaned, ""
}

// Match runs the full event → rule list match. Pure (no Slack API calls). The
// dispatcher logs the returned reason so silent misses are debuggable.
func Match(ev IncomingEvent, rules []config.Rule, ctx MatcherContext) MatchResult {
	userID := ev.User
	botID := ev.BotID
	if ev.Nested != nil {
		if userID == "" {
			userID = ev.Nested.User
		}
		if botID == "" {
			botID = ev.Nested.BotID
		}
	}

	// 1. Self-loop guard — never react to our own posts.
	if userID != "" && userID == ctx.SelfUserID {
		return MatchResult{Kind: MatchKindSkip, Reason: "self-user"}
	}
	if ctx.SelfBotID != "" && botID != "" && botID == ctx.SelfBotID {
		return MatchResult{Kind: MatchKindSkip, Reason: "self-bot"}
	}

	// 2. Subtype allow-list (only relevant for type: message).
	if ev.Type == "message" && !allowedMessageSubtypes[ev.Subtype] {
		return MatchResult{Kind: MatchKindSkip, Reason: "subtype:" + ev.Subtype}
	}

	rawText := ExtractText(ev)

	// 3. Authorization for mentions — only ALLOWED_USER_IDS may invoke.
	if ev.Type == "app_mention" {
		if userID == "" || !containsString(ctx.AllowedUserIDs, userID) {
			who := userID
			if who == "" {
				who = "<none>"
			}
			return MatchResult{Kind: MatchKindUnauthorized, Reason: "user " + who + " not in ALLOWED_USER_IDS"}
		}
	}

	// 4. Walk rules; first match wins. Type must align.
	if ev.Type == "app_mention" {
		text, firstToken, rest := NormalizeMentionText(rawText)
		for i := range rules {
			r := &rules[i]
			if r.Trigger.Type != config.TriggerTypeAppMention {
				continue
			}
			if matchMention(r, firstToken) {
				return MatchResult{Kind: MatchKindMatched, Rule: r, Text: text, FirstToken: firstToken, Rest: rest}
			}
		}
		return MatchResult{Kind: MatchKindNoMatch, Text: text, FirstToken: firstToken}
	}

	for i := range rules {
		r := &rules[i]
		if r.Trigger.Type != config.TriggerTypeMessage {
			continue
		}
		if matchMessage(ev, userID, r) {
			return MatchResult{Kind: MatchKindMatched, Rule: r, Text: rawText}
		}
	}
	return MatchResult{Kind: MatchKindNoMatch, Text: rawText}
}

func matchMention(r *config.Rule, firstToken string) bool {
	if r.Trigger.Keyword == nil {
		return true // default rule
	}
	if firstToken == "" {
		return false
	}
	return strings.EqualFold(firstToken, *r.Trigger.Keyword)
}

func matchMessage(ev IncomingEvent, userID string, r *config.Rule) bool {
	if ev.Channel != r.Trigger.Channel {
		return false
	}
	if r.Trigger.From == nil {
		return false
	}
	from := r.Trigger.From

	appID := ev.AppID
	username := ev.Username
	if username == "" {
		username = ev.BotProfileName
	}
	if ev.Nested != nil {
		if appID == "" {
			appID = ev.Nested.AppID
		}
		if username == "" {
			username = ev.Nested.Username
		}
		if username == "" {
			username = ev.Nested.BotProfileName
		}
	}

	if len(from.BotUserIDs) > 0 && userID != "" && containsString(from.BotUserIDs, userID) {
		return true
	}
	if len(from.AppIDs) > 0 && appID != "" && containsString(from.AppIDs, appID) {
		return true
	}
	if len(from.Usernames) > 0 && username != "" {
		lo := strings.ToLower(username)
		for _, u := range from.Usernames {
			if strings.ToLower(u) == lo {
				return true
			}
		}
	}
	// B-prefixed bot_id is intentionally not matched against bot_user_ids
	// (U-prefix). If the sender publishes only bot_id, use app_ids or
	// usernames.
	return false
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
