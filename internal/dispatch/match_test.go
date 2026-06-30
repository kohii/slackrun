package dispatch

import (
	"strings"
	"testing"

	"github.com/kohii/slackrun/internal/config"
)

func ptr(s string) *string { return &s }

func newRule(name string, trig config.Trigger) config.Rule {
	return config.Rule{
		Name:    name,
		Trigger: trig,
		Action: config.Action{
			Cwd:       "/tmp",
			Command:   []string{"echo"},
			TimeoutMs: 1000,
		},
	}
}

func TestMatch_SelfLoopByUser(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "app_mention", User: "U01SELF", Text: "hi"}
	rules := []config.Rule{newRule("any", config.Trigger{Type: config.TriggerTypeAppMention})}
	res := Match(ev, rules, MatcherContext{SelfUserID: "U01SELF"})
	if res.Kind != MatchKindSkip || res.Reason != "self-user" {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_SelfLoopByBotID(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "message", Subtype: "bot_message", BotID: "B01SELF"}
	rules := []config.Rule{newRule("x", config.Trigger{
		Type: config.TriggerTypeMessage, Channel: "C01X",
		From: &config.TriggerFrom{Usernames: []string{"any"}},
	})}
	res := Match(ev, rules, MatcherContext{SelfBotID: "B01SELF"})
	if res.Kind != MatchKindSkip || res.Reason != "self-bot" {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_SubtypeDropped(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "message", Subtype: "channel_join", Channel: "C01X"}
	res := Match(ev, nil, MatcherContext{})
	if res.Kind != MatchKindSkip || res.Reason != "subtype:channel_join" {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_UnauthorizedMention(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "app_mention", User: "U99STRANGER", Text: "<@U01BOT> hello"}
	rules := []config.Rule{newRule("any", config.Trigger{Type: config.TriggerTypeAppMention})}
	res := Match(ev, rules, MatcherContext{AllowedUserIDs: []string{"U01ALLOWED"}})
	if res.Kind != MatchKindUnauthorized {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_MentionKeyword_MatchesCaseInsensitively(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "app_mention", User: "U01OK", Text: "<@U01BOT> Henry build something"}
	rules := []config.Rule{
		newRule("henry", config.Trigger{Type: config.TriggerTypeAppMention, Keyword: ptr("henry")}),
		newRule("default", config.Trigger{Type: config.TriggerTypeAppMention}),
	}
	res := Match(ev, rules, MatcherContext{AllowedUserIDs: []string{"U01OK"}})
	if res.Kind != MatchKindMatched || res.Rule.Name != "henry" {
		t.Fatalf("got %+v", res)
	}
	if res.Text != "Henry build something" {
		t.Fatalf("text=%q", res.Text)
	}
	if res.FirstToken != "Henry" || res.Rest != "build something" {
		t.Fatalf("token=%q rest=%q", res.FirstToken, res.Rest)
	}
}

func TestMatch_MentionDefault(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "app_mention", User: "U01OK", Text: "<@U01BOT> unknown command"}
	rules := []config.Rule{
		newRule("henry", config.Trigger{Type: config.TriggerTypeAppMention, Keyword: ptr("henry")}),
		newRule("default", config.Trigger{Type: config.TriggerTypeAppMention}),
	}
	res := Match(ev, rules, MatcherContext{AllowedUserIDs: []string{"U01OK"}})
	if res.Kind != MatchKindMatched || res.Rule.Name != "default" {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_MessageByAppID(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{
		Type: "message", Subtype: "bot_message",
		Channel: "C01ALERT", AppID: "A01SENTRY", BotID: "B01SENTRY",
	}
	rules := []config.Rule{newRule("sentry", config.Trigger{
		Type:    config.TriggerTypeMessage,
		Channel: "C01ALERT",
		From:    &config.TriggerFrom{AppIDs: []string{"A01SENTRY"}},
	})}
	res := Match(ev, rules, MatcherContext{})
	if res.Kind != MatchKindMatched {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_MessageByUsernameCaseInsensitive(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{
		Type: "message", Subtype: "bot_message",
		Channel: "C01ALERT", BotProfileName: "Sentry",
	}
	rules := []config.Rule{newRule("sentry", config.Trigger{
		Type:    config.TriggerTypeMessage,
		Channel: "C01ALERT",
		From:    &config.TriggerFrom{Usernames: []string{"sentry"}},
	})}
	res := Match(ev, rules, MatcherContext{})
	if res.Kind != MatchKindMatched {
		t.Fatalf("got %+v", res)
	}
}

func TestMatch_MessageNested(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{
		Type: "message", Subtype: "message_replied",
		Channel: "C01ALERT",
		Nested:  &NestedMessage{AppID: "A01SENTRY", Text: "[Sentry] error"},
	}
	rules := []config.Rule{newRule("sentry", config.Trigger{
		Type:    config.TriggerTypeMessage,
		Channel: "C01ALERT",
		From:    &config.TriggerFrom{AppIDs: []string{"A01SENTRY"}},
	})}
	res := Match(ev, rules, MatcherContext{})
	if res.Kind != MatchKindMatched {
		t.Fatalf("got %+v", res)
	}
	if res.Text != "[Sentry] error" {
		t.Fatalf("text=%q", res.Text)
	}
}

func TestMatch_NoMatch(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{Type: "message", Channel: "C01OTHER"}
	rules := []config.Rule{newRule("sentry", config.Trigger{
		Type:    config.TriggerTypeMessage,
		Channel: "C01ALERT",
		From:    &config.TriggerFrom{AppIDs: []string{"A01SENTRY"}},
	})}
	res := Match(ev, rules, MatcherContext{})
	if res.Kind != MatchKindNoMatch {
		t.Fatalf("got %+v", res)
	}
}

func TestNormalizeMentionText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		text string
		tok  string
		rest string
	}{
		{"<@U01BOT> henry build x", "henry build x", "henry", "build x"},
		{"<@U01BOT|kohii-ai> henry", "henry", "henry", ""},
		{"  <@U01BOT>   spaced   args  ", "spaced args", "spaced", "args"},
		{"<@U01BOT>", "", "", ""},
		// Slack mobile sometimes inserts U+00A0 (NBSP) or U+3000 (full-width
		// space) around mentions; ASCII `\s` would miss them.
		{"<@U01BOT> henry　build", "henry build", "henry", "build"},
	}
	for _, c := range cases {
		text, tok, rest := NormalizeMentionText(c.in)
		if text != c.text || tok != c.tok || rest != c.rest {
			t.Fatalf("in=%q got (%q,%q,%q) want (%q,%q,%q)", c.in, text, tok, rest, c.text, c.tok, c.rest)
		}
	}
}

func TestMessageBody_AppMention_CommandText_StripsKeyword(t *testing.T) {
	t.Parallel()
	keyword := "henry"
	rule := config.Rule{Trigger: config.Trigger{Type: config.TriggerTypeAppMention, Keyword: &keyword}}
	ev := IncomingEvent{Type: "app_mention", Text: "<@UBOT> henry refactor this"}
	res := MatchResult{Rule: &rule, Text: "henry refactor this", Rest: "refactor this"}
	got := MessageBody(ev, res, config.ContentCommandText)
	if got != "refactor this" {
		t.Fatalf("got %q, want %q", got, "refactor this")
	}
}

func TestMessageBody_AppMention_DefaultRule_KeepsFullBody(t *testing.T) {
	t.Parallel()
	// Default rule has no keyword; the first word IS part of the user
	// command and must NOT be stripped (Rest would drop it incorrectly).
	rule := config.Rule{Trigger: config.Trigger{Type: config.TriggerTypeAppMention}}
	ev := IncomingEvent{Type: "app_mention", Text: "<@UBOT> hello there"}
	res := MatchResult{Rule: &rule, Text: "hello there", Rest: "there"}
	got := MessageBody(ev, res, config.ContentCommandText)
	if got != "hello there" {
		t.Fatalf("got %q, want %q", got, "hello there")
	}
}

func TestMessageBody_AppMention_RichTextBlock_NotDuplicated(t *testing.T) {
	t.Parallel()
	// Regression: Slack delivers user mentions with `text` AND an
	// auto-generated rich_text block carrying the same content. Flattening
	// the block would re-inject the bot mention + keyword that
	// `command_text` mode just stripped. rich_text blocks are deliberately
	// filtered out by the upstream slackevents → dispatch conversion, so
	// MessageBody sees only the structured blocks bots author (section,
	// header, …).
	keyword := "henry"
	rule := config.Rule{Trigger: config.Trigger{Type: config.TriggerTypeAppMention, Keyword: &keyword}}
	ev := IncomingEvent{
		Type: "app_mention",
		Text: "<@UBOT> henry refactor this",
		// Caller upstream filters rich_text — verify MessageBody is a no-op
		// when no extra blocks are passed in.
	}
	res := MatchResult{Rule: &rule, Text: "henry refactor this", Rest: "refactor this"}
	if got := MessageBody(ev, res, config.ContentCommandText); got != "refactor this" {
		t.Fatalf("got %q, want %q", got, "refactor this")
	}
}

func TestMessageBody_BotWithBlocks_FlattensIntoBody(t *testing.T) {
	t.Parallel()
	// Sentry-style: empty text + structured blocks. The flatten path
	// recovers the alert body so the spawned child has something useful.
	ev := IncomingEvent{
		Type: "message",
		Text: "",
		Blocks: []Block{
			{Type: "header", Text: "New Issue"},
			{Type: "section", Text: "Error: nil pointer at users.go:42"},
		},
	}
	res := MatchResult{Text: ""}
	got := MessageBody(ev, res, config.ContentRawText)
	if got == "" {
		t.Fatal("expected flattened body, got empty")
	}
	for _, want := range []string{"New Issue", "nil pointer", "users.go:42"} {
		if !contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

func TestMessageBody_BotWithAttachments_FlattensFields(t *testing.T) {
	t.Parallel()
	ev := IncomingEvent{
		Type: "message",
		Text: "alert",
		Attachments: []Attachment{{
			Title: "Issue",
			Text:  "details",
			Fields: []AttachmentField{
				{Title: "env", Value: "prod"},
				{Title: "service", Value: "checkout"},
			},
		}},
	}
	res := MatchResult{Text: "alert"}
	got := MessageBody(ev, res, config.ContentRawText)
	for _, want := range []string{"alert", "Issue", "details", "env: prod", "service: checkout"} {
		if !contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
