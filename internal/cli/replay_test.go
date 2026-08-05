package cli

import (
	"testing"

	"github.com/kohii/slackrun/internal/dispatch"
)

func TestParsePermalink_TopLevel(t *testing.T) {
	t.Parallel()
	ch, ts, thread, err := parsePermalink("https://henry-app.slack.com/archives/C01DRUDMA2C/p1782906914616759")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ch != "C01DRUDMA2C" || ts != "1782906914.616759" || thread != "" {
		t.Errorf("got ch=%q ts=%q thread=%q", ch, ts, thread)
	}
}

func TestParsePermalink_ThreadReply(t *testing.T) {
	t.Parallel()
	ch, ts, thread, err := parsePermalink("https://henry-app.slack.com/archives/C01DRUDMA2C/p1782906920000000?thread_ts=1782906914.616759&cid=C01DRUDMA2C")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ch != "C01DRUDMA2C" || ts != "1782906920.000000" || thread != "1782906914.616759" {
		t.Errorf("got ch=%q ts=%q thread=%q", ch, ts, thread)
	}
}

func TestEventType(t *testing.T) {
	t.Parallel()
	human := func(text string) dispatch.IncomingEvent {
		return dispatch.IncomingEvent{User: "U0HUMAN", Text: text}
	}
	bot := func(text string) dispatch.IncomingEvent {
		return dispatch.IncomingEvent{BotID: "B0SENTRY", Subtype: "bot_message", Text: text}
	}
	cases := []struct {
		name     string
		override string
		ev       dispatch.IncomingEvent
		self     string
		want     string
	}{
		{"plain message", "", human("deploy failed"), "U0SELF", "message"},
		{"mention", "", human("<@U0SELF> /workspace fix it"), "U0SELF", "app_mention"},
		{"labelled mention", "", human("<@U0SELF|slackrun> hi"), "U0SELF", "app_mention"},
		{"someone else mentioned", "", human("<@U0OTHER> hi"), "U0SELF", "message"},
		{"id that merely starts with ours", "", human("<@U0SELFISH> hi"), "U0SELF", "message"},
		{"self id unknown", "", human("<@U0SELF> hi"), "", "message"},
		{"integration mentioning us", "", bot("<@U0SELF> alert"), "U0SELF", "message"},
		{"override to message", "message", human("<@U0SELF> hi"), "U0SELF", "message"},
		{"override to app_mention", "app_mention", bot("no mention here"), "U0SELF", "app_mention"},
	}
	for _, c := range cases {
		if got := eventType(c.override, c.ev, c.self); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParsePermalink_Rejects(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"https://slack.com/foo",
		"https://henry-app.slack.com/archives/C01/pnotdigits",
	}
	for _, c := range cases {
		if _, _, _, err := parsePermalink(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
