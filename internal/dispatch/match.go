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

	// Attachments / Blocks carry the bot-authored rich content typical of
	// Sentry, Datadog, and Incoming Webhook posts. The triggering-message
	// renderer flattens these into the body so a `text:`-empty alert is
	// still usable.
	Attachments []Attachment
	Blocks      []Block
	// Files lists attached uploads (PDF, images, …). Rendered only when the
	// part opts in via `files: link`.
	Files []File

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
	Attachments    []Attachment
	Blocks         []Block
	Files          []File
}

// Attachment is a Slack legacy `attachments[]` element collapsed to the
// fields a renderer might surface. Each renderer chooses which to emit.
type Attachment struct {
	Fallback string
	Title    string
	Text     string
	Fields   []AttachmentField
}

// AttachmentField is one row in a Slack legacy attachment.
type AttachmentField struct {
	Title string
	Value string
}

// Block is one Block Kit block, collapsed to its visible text. Type carries
// the original block kind ("section", "header", "rich_text", …) so the
// renderer can group output if it wants. The flattener fills Text with the
// block's primary plain-text content.
type Block struct {
	Type string
	Text string
}

// File is one Slack file upload reference.
type File struct {
	Name string
	URL  string
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
	Text       string // normalized message text (mentions stripped for app_mention)
	FirstToken string // first non-mention token; empty if none
	Rest       string // text minus the first token (for keyword-based dispatch)
	Reason     string // populated for Skip / Unauthorized
}

var allowedMessageSubtypes = map[string]bool{
	"":                true, // plain message
	"bot_message":     true,
	"message_replied": true,
}

// mentionRe matches Slack's user-mention tokens (`<@U…>` or `<@U…|name>`).
// W-prefix covers enterprise grid users.
var mentionRe = regexp.MustCompile(`<@[UW][A-Z0-9]+(?:\|[^>]+)?>`)

// whitespaceCollapse matches runs of whitespace, including Unicode space
// separators like full-width space (U+3000) and NBSP (U+00A0) that Slack
// clients sometimes insert. Go's `\s` is ASCII-only.
var whitespaceCollapse = regexp.MustCompile(`[\s\p{Zs}]+`)

// ExtractText pulls the most-specific text field, accounting for subtype
// nesting (message_replied wraps the original in .message).
func ExtractText(ev IncomingEvent) string {
	if ev.Nested != nil && ev.Nested.Text != "" {
		return ev.Nested.Text
	}
	return ev.Text
}

// extractAttachments / extractBlocks / extractFiles prefer the nested
// `.message` envelope when it carries the field (subtype: message_replied),
// falling back to the root.
func extractAttachments(ev IncomingEvent) []Attachment {
	if ev.Nested != nil && len(ev.Nested.Attachments) > 0 {
		return ev.Nested.Attachments
	}
	return ev.Attachments
}

func extractBlocks(ev IncomingEvent) []Block {
	if ev.Nested != nil && len(ev.Nested.Blocks) > 0 {
		return ev.Nested.Blocks
	}
	return ev.Blocks
}

// ExtractFiles returns the files attached to the triggering event,
// preferring the nested `.message` envelope when set.
func ExtractFiles(ev IncomingEvent) []File {
	if ev.Nested != nil && len(ev.Nested.Files) > 0 {
		return ev.Nested.Files
	}
	return ev.Files
}

// NormalizeMentionText strips mention tokens, collapses whitespace, and
// returns the cleaned text along with the first token (the keyword candidate)
// and the rest.
func NormalizeMentionText(raw string) (text, firstToken, rest string) {
	cleaned := mentionRe.ReplaceAllString(raw, "")
	// Collapse on Unicode whitespace too, then split on the single ASCII space
	// the collapse produces.
	cleaned = strings.Trim(whitespaceCollapse.ReplaceAllString(cleaned, " "), " ")
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

// MessageBody returns the text body of the triggering message for the
// requested content mode, with bot-authored Block Kit / legacy attachments
// flattened in. Used by the trigger_message stdin part renderer.
//
// For type: message rules, all three content modes resolve to the same
// "raw + flatten" view because there is no mention / keyword to strip.
func MessageBody(ev IncomingEvent, res MatchResult, mode config.ContentMode) string {
	base := selectModeText(ev, res, mode)
	flattened := flattenBody(ev)
	switch {
	case base == "" && flattened == "":
		return ""
	case base == "":
		return flattened
	case flattened == "":
		return base
	}
	return base + "\n\n" + flattened
}

func selectModeText(ev IncomingEvent, res MatchResult, mode config.ContentMode) string {
	if ev.Type != "app_mention" {
		return ExtractText(ev)
	}
	switch mode {
	case config.ContentRawText:
		return ExtractText(ev)
	case config.ContentBodyText:
		return res.Text
	case config.ContentCommandText, "":
		// For default rules (no keyword) Rule.Trigger.Keyword is nil and
		// `Rest` would drop the user's first word, which is itself part of
		// the command body. Use the full mention-stripped Text in that case.
		if res.Rule != nil && res.Rule.Trigger.Keyword != nil {
			return res.Rest
		}
		return res.Text
	}
	return res.Text
}

// flattenBody folds Block Kit blocks and legacy attachments into a single
// plain-text string appended to the message body. Empty when there is no
// rich content. Stable order: blocks first (newer API), then attachments.
func flattenBody(ev IncomingEvent) string {
	var parts []string
	for _, b := range extractBlocks(ev) {
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	for _, a := range extractAttachments(ev) {
		parts = append(parts, flattenAttachment(a)...)
	}
	return strings.Join(parts, "\n\n")
}

func flattenAttachment(a Attachment) []string {
	var out []string
	add := func(s string) {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	add(a.Title)
	add(a.Text)
	for _, f := range a.Fields {
		switch {
		case f.Title != "" && f.Value != "":
			add(f.Title + ": " + f.Value)
		case f.Value != "":
			add(f.Value)
		}
	}
	// fallback is the legacy "text representation if blocks fail" — only use
	// it when nothing else surfaced.
	if len(out) == 0 {
		add(a.Fallback)
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
