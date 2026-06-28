package dispatch

import (
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
