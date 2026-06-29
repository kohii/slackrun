package slackapp

import (
	"strings"
	"testing"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/slackthread"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func TestNeedsThreadFetch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec *config.StdinSpec
		want bool
	}{
		{"nil", nil, false},
		{"text only", &config.StdinSpec{Parts: []config.StdinPart{{Kind: config.PartKindText, Text: "x"}}}, false},
		{"with slack_thread", &config.StdinSpec{Parts: []config.StdinPart{
			{Kind: config.PartKindText, Text: "x"},
			{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{}},
		}}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := needsThreadFetch(c.spec); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestStrictestFetchErrorPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec *config.StdinSpec
		want string
	}{
		{"nil → fail", nil, "fail"},
		{"no slack_thread → fail (vacuous)", &config.StdinSpec{Parts: []config.StdinPart{{Kind: config.PartKindText, Text: "x"}}}, "fail"},
		{"explicit fail wins", &config.StdinSpec{Parts: []config.StdinPart{
			{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{OnFetchError: "fallback_event"}},
			{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{OnFetchError: "fail"}},
		}}, "fail"},
		{"all fallback → fallback", &config.StdinSpec{Parts: []config.StdinPart{
			{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{OnFetchError: "fallback_event"}},
		}}, "fallback_event"},
		{"empty defaults to fail", &config.StdinSpec{Parts: []config.StdinPart{
			{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{}},
		}}, "fail"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := strictestFetchErrorPolicy(c.spec); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildStdinPayload_ConcatsPartsInOrder(t *testing.T) {
	t.Parallel()
	spec := &config.StdinSpec{Parts: []config.StdinPart{
		{Kind: config.PartKindText, Text: "INTRO\n"},
		{Kind: config.PartKindSlackThread, SlackThread: &config.SlackThreadSpec{Format: "text"}},
		{Kind: config.PartKindText, Text: "\nOUTRO"},
	}}
	thread := []slackthread.Message{{TS: "1", Source: slackthread.SourceUser, User: "U1", Text: "hi"}}
	out := buildStdinPayload(spec, dispatch.TemplateVars{}, thread)
	if !strings.HasPrefix(out, "INTRO\n") {
		t.Errorf("missing INTRO prefix: %q", out)
	}
	if !strings.HasSuffix(out, "OUTRO") {
		t.Errorf("missing OUTRO suffix: %q", out)
	}
	if !strings.Contains(out, "<@U1 user ts=1>: hi") {
		t.Errorf("thread not rendered between parts: %q", out)
	}
}

func TestBuildStdinPayload_ExpandsTemplateVars(t *testing.T) {
	t.Parallel()
	spec := &config.StdinSpec{Parts: []config.StdinPart{
		{Kind: config.PartKindTemplate, Template: "user={{user}} text={{text}}"},
	}}
	out := buildStdinPayload(spec, dispatch.TemplateVars{User: "U1", Text: "hello"}, nil)
	if out != "user=U1 text=hello" {
		t.Fatalf("got %q", out)
	}
}

func TestSynthesizeFallbackThread_UserEvent(t *testing.T) {
	t.Parallel()
	ev := dispatch.IncomingEvent{TS: "1.0", User: "U1", Text: "hi"}
	got := synthesizeFallbackThread(ev, "U_SELF", "")
	if len(got) != 1 {
		t.Fatalf("want 1 msg, got %d", len(got))
	}
	if got[0].Source != slackthread.SourceUser || got[0].User != "U1" {
		t.Errorf("source/user wrong: %+v", got[0])
	}
}

func TestSynthesizeFallbackThread_TagsSelfMessages(t *testing.T) {
	t.Parallel()
	ev := dispatch.IncomingEvent{TS: "1.0", User: "U_SELF", Text: "self"}
	got := synthesizeFallbackThread(ev, "U_SELF", "")
	if got[0].Source != slackthread.SourceSelf {
		t.Errorf("expected self, got %v", got[0].Source)
	}
}

func TestSynthesizeFallbackThread_BotPicksReadableName(t *testing.T) {
	t.Parallel()
	ev := dispatch.IncomingEvent{
		TS:    "1.0",
		BotID: "B999",
		Nested: &dispatch.NestedMessage{
			Text:           "alert!",
			BotID:          "B999",
			BotProfileName: "Sentry",
			AppID:          "A123",
		},
	}
	got := synthesizeFallbackThread(ev, "", "")
	if got[0].Source != slackthread.SourceBot {
		t.Fatalf("got source %v", got[0].Source)
	}
	if got[0].Bot != "Sentry" {
		t.Errorf("expected Sentry, got %q", got[0].Bot)
	}
	if got[0].Text != "alert!" {
		t.Errorf("text from nested not used: %q", got[0].Text)
	}
}

func TestFromMessage_PopulatesAppIDFromBotProfile(t *testing.T) {
	t.Parallel()
	// Reproduces the Sentry-style bot post: top-level fields are empty but
	// `.message.bot_profile.app_id` carries the identity we match on.
	e := &slackevents.MessageEvent{
		SubType:         "",
		Channel:         "C01",
		TimeStamp:       "1.0",
		ThreadTimeStamp: "",
		BotID:           "B_SENTRY",
		Username:        "Sentry",
		Text:            "alert!",
		Message: &slack.Msg{
			Text:       "alert!",
			BotID:      "B_SENTRY",
			Username:   "Sentry",
			BotProfile: &slack.BotProfile{Name: "Sentry", AppID: "A00SENTRY"},
		},
	}
	got := fromMessage(e)
	if got.Nested == nil {
		t.Fatalf("expected Nested set: %+v", got)
	}
	if got.Nested.AppID != "A00SENTRY" {
		t.Errorf("Nested.AppID = %q, want A00SENTRY", got.Nested.AppID)
	}
	if got.Nested.BotProfileName != "Sentry" {
		t.Errorf("Nested.BotProfileName = %q", got.Nested.BotProfileName)
	}
}
