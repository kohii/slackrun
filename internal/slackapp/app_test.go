package slackapp

import (
	"strings"
	"testing"

	"github.com/kohii/slackrun/internal/config"
	"github.com/kohii/slackrun/internal/dispatch"
	"github.com/kohii/slackrun/internal/runner"
	"github.com/kohii/slackrun/internal/slackthread"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func TestNeedsThreadFetch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		parts []config.StdinPart
		want  bool
	}{
		{"nil", nil, false},
		{"text only", []config.StdinPart{{Kind: config.PartKindText, Text: "x"}}, false},
		{"trigger_message only", []config.StdinPart{{Kind: config.PartKindTriggerMessage, TriggerMessage: &config.TriggerMessageSpec{}}}, false},
		{"with thread", []config.StdinPart{
			{Kind: config.PartKindText, Text: "x"},
			{Kind: config.PartKindThread, Thread: &config.ThreadSpec{}},
		}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := needsThreadFetch(c.parts); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestThreadFetchPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		parts []config.StdinPart
		want  config.OnFetchErrorPolicy
	}{
		{"no thread → fail (vacuous default)", nil, config.OnFetchErrorFail},
		{"text only → fail", []config.StdinPart{{Kind: config.PartKindText, Text: "x"}}, config.OnFetchErrorFail},
		{"explicit fail", []config.StdinPart{
			{Kind: config.PartKindThread, Thread: &config.ThreadSpec{OnFetchError: config.OnFetchErrorFail}},
		}, config.OnFetchErrorFail},
		{"explicit omit", []config.StdinPart{
			{Kind: config.PartKindThread, Thread: &config.ThreadSpec{OnFetchError: config.OnFetchErrorOmit}},
		}, config.OnFetchErrorOmit},
		{"default thread → fail", []config.StdinPart{
			{Kind: config.PartKindThread, Thread: &config.ThreadSpec{}},
		}, config.OnFetchErrorFail},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := threadFetchPolicy(c.parts); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildStdinPayload_ConcatsPartsInOrder(t *testing.T) {
	t.Parallel()
	parts := []config.StdinPart{
		{Kind: config.PartKindText, Text: "INTRO\n"},
		{Kind: config.PartKindThread, Thread: &config.ThreadSpec{IncludeTriggeringMessage: true, Format: "text"}},
		{Kind: config.PartKindText, Text: "\nOUTRO"},
	}
	thread := []slackthread.Message{{TS: "1", Source: slackthread.SourceUser, User: "U1", Text: "hi"}}
	out := buildStdinPayload(stdinBuildInput{Parts: parts, Thread: thread})
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

func TestBuildStdinPayload_ExpandsMetadataVars(t *testing.T) {
	t.Parallel()
	parts := []config.StdinPart{
		{Kind: config.PartKindText, Text: "user={{event.user_id}} channel={{event.channel_id}}"},
	}
	out := buildStdinPayload(stdinBuildInput{
		Parts: parts,
		Vars:  dispatch.TemplateVars{UserID: "U1", ChannelID: "C9"},
	})
	if out != "user=U1 channel=C9" {
		t.Fatalf("got %q", out)
	}
}

func TestBuildStdinPayload_ThreadStandaloneMention_PartVanishes(t *testing.T) {
	t.Parallel()
	// A standalone mention's "thread" is just the trigger itself; with
	// IncludeTriggeringMessage:false the thread part has nothing to render
	// and must contribute nothing — including its heading.
	parts := []config.StdinPart{
		{Kind: config.PartKindText, Text: "PRE\n"},
		{Kind: config.PartKindThread, Thread: &config.ThreadSpec{
			Heading:                  "参考スレッド",
			IncludeTriggeringMessage: false,
		}},
	}
	thread := []slackthread.Message{{TS: "1.0", Source: slackthread.SourceUser, User: "U1", Text: "hi"}}
	out := buildStdinPayload(stdinBuildInput{
		Parts:  parts,
		Event:  dispatch.IncomingEvent{TS: "1.0"},
		Thread: thread,
	})
	if out != "PRE\n" {
		t.Fatalf("expected only the PRE text, got %q", out)
	}
	if strings.Contains(out, "UNTRUSTED_SLACK_THREAD") {
		t.Fatalf("wrapper leaked into empty-thread output: %q", out)
	}
	if strings.Contains(out, "参考スレッド") {
		t.Fatalf("heading leaked alongside empty thread: %q", out)
	}
}

func TestBuildStdinPayload_ThreadInThread_DropsTriggerByDefault(t *testing.T) {
	t.Parallel()
	parts := []config.StdinPart{
		{Kind: config.PartKindTriggerMessage, TriggerMessage: &config.TriggerMessageSpec{}},
		{Kind: config.PartKindThread, Thread: &config.ThreadSpec{IncludeTriggeringMessage: false}},
	}
	thread := []slackthread.Message{
		{TS: "1000", Source: slackthread.SourceUser, User: "U1", Text: "parent"},
		{TS: "1001", Source: slackthread.SourceUser, User: "U2", Text: "reply"},
		{TS: "1003", Source: slackthread.SourceUser, User: "U1", Text: "trigger"}, // excluded
	}
	ev := dispatch.IncomingEvent{Type: "app_mention", TS: "1003", User: "U1", Text: "trigger"}
	res := dispatch.MatchResult{Text: "trigger", Rest: "trigger"}
	out := buildStdinPayload(stdinBuildInput{Parts: parts, Event: ev, Match: res, Thread: thread, Nonce: "TEST"})
	if !strings.Contains(out, "UNTRUSTED_SLACK_MESSAGE_TEST") {
		t.Errorf("trigger_message wrapper missing: %q", out)
	}
	if !strings.Contains(out, "UNTRUSTED_SLACK_THREAD_TEST") {
		t.Errorf("thread wrapper missing: %q", out)
	}
	if !strings.Contains(out, "ts=1000>: parent") || !strings.Contains(out, "ts=1001>: reply") {
		t.Errorf("prior messages missing: %q", out)
	}
	// "trigger" body must appear once in trigger_message; it must NOT also
	// appear inside the thread block.
	idxFirst := strings.Index(out, "trigger")
	if idxFirst < 0 {
		t.Fatalf("trigger body missing: %q", out)
	}
	// Check that the trigger body does not appear inside the thread block
	// (which starts after the first occurrence of UNTRUSTED_SLACK_THREAD_TEST).
	threadStart := strings.Index(out, "UNTRUSTED_SLACK_THREAD_TEST")
	if threadStart >= 0 && strings.Contains(out[threadStart:], "ts=1003>") {
		t.Errorf("trigger leaked into thread block: %q", out)
	}
}

func TestBuildStdinPayload_TriggerMessage_AlwaysRenders(t *testing.T) {
	t.Parallel()
	parts := []config.StdinPart{
		{Kind: config.PartKindTriggerMessage, TriggerMessage: &config.TriggerMessageSpec{
			Heading: "最新の依頼",
			Content: config.ContentCommandText,
		}},
	}
	ev := dispatch.IncomingEvent{Type: "app_mention", TS: "1.0", User: "U1", Text: "<@UBOT> hello there"}
	res := dispatch.MatchResult{Text: "hello there", Rest: "there"} // mention stripped; "hello" is first token
	out := buildStdinPayload(stdinBuildInput{Parts: parts, Event: ev, Match: res, Nonce: "ZZZ"})
	if !strings.Contains(out, "最新の依頼") {
		t.Errorf("heading missing: %q", out)
	}
	if !strings.Contains(out, "UNTRUSTED_SLACK_MESSAGE_ZZZ") {
		t.Errorf("wrapper with nonce missing: %q", out)
	}
}

func TestBuildStdinPayload_TriggerMessage_DefaultModeForDefaultRule(t *testing.T) {
	t.Parallel()
	// Default rule (no keyword) → CommandText falls back to full mention-
	// stripped text (the first word IS part of the command).
	parts := []config.StdinPart{
		{Kind: config.PartKindTriggerMessage, TriggerMessage: &config.TriggerMessageSpec{}},
	}
	ev := dispatch.IncomingEvent{Type: "app_mention", TS: "1.0", User: "U1"}
	rule := &config.Rule{Trigger: config.Trigger{Type: config.TriggerTypeAppMention}} // Keyword: nil → default rule
	res := dispatch.MatchResult{Rule: rule, Text: "hello there", Rest: "there"}
	out := buildStdinPayload(stdinBuildInput{Parts: parts, Event: ev, Match: res})
	if !strings.Contains(out, "hello there") {
		t.Errorf("expected full mention-stripped text for default rule, got: %q", out)
	}
	if strings.Contains(out, "<@") && !strings.Contains(out, "<@U1 ") {
		t.Errorf("speaker tag missing: %q", out)
	}
}

func TestBuildTriggerMessage_BotSentryStyle(t *testing.T) {
	t.Parallel()
	ev := dispatch.IncomingEvent{
		Type:  "message",
		TS:    "1.0",
		BotID: "B999",
		Nested: &dispatch.NestedMessage{
			Text:           "alert!",
			BotID:          "B999",
			BotProfileName: "Sentry",
			AppID:          "A123",
		},
	}
	res := dispatch.MatchResult{Text: "alert!"}
	msg := buildTriggerMessage(ev, res, config.ContentRawText, "", "")
	if msg.Source != slackthread.SourceBot {
		t.Errorf("source = %v, want SourceBot", msg.Source)
	}
	if msg.Bot != "Sentry" {
		t.Errorf("bot name = %q, want Sentry", msg.Bot)
	}
	if msg.Text != "alert!" {
		t.Errorf("text = %q", msg.Text)
	}
}

func TestBuildTriggerMessage_TagsSelfMessages(t *testing.T) {
	t.Parallel()
	ev := dispatch.IncomingEvent{TS: "1.0", User: "U_SELF", Text: "self"}
	res := dispatch.MatchResult{Text: "self"}
	msg := buildTriggerMessage(ev, res, config.ContentRawText, "U_SELF", "")
	if msg.Source != slackthread.SourceSelf {
		t.Errorf("source = %v, want SourceSelf", msg.Source)
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

func TestFromAppMention_CarriesAttachments(t *testing.T) {
	t.Parallel()
	e := &slackevents.AppMentionEvent{
		Channel:   "C01",
		User:      "U1",
		TimeStamp: "1.0",
		Text:      "<@UBOT> hi",
		Attachments: []slack.Attachment{{
			Title: "ctx",
			Text:  "body",
		}},
	}
	got := fromAppMention(e)
	if len(got.Attachments) != 1 || got.Attachments[0].Title != "ctx" {
		t.Errorf("attachment conversion failed: %+v", got.Attachments)
	}
}

func TestDecideCompletion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		result          runner.Result
		replyWithStdout bool
		want            completionAction
	}{
		{"timeout beats everything", runner.Result{TimedOut: true}, true, completionTimeout},
		{"not-found beats failure", runner.Result{NotFound: true, ExitCode: 1}, true, completionNotFound},
		{"exit code != 0 → failed", runner.Result{ExitCode: 2}, true, completionFailed},
		{"success default → post stdout", runner.Result{}, true, completionPostStdout},
		{"success + reply disabled → mark done", runner.Result{}, false, completionMarkDone},
		{"failure + reply disabled → still report failure", runner.Result{ExitCode: 1}, false, completionFailed},
		{"timeout + reply disabled → still report timeout", runner.Result{TimedOut: true}, false, completionTimeout},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := decideCompletion(c.result, c.replyWithStdout); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildStdinPayload_SlackrunHelpInjectsChildUsage(t *testing.T) {
	t.Parallel()
	parts := []config.StdinPart{
		{Kind: config.PartKindText, Text: "Lead:\n"},
		{Kind: config.PartKindSlackrunHelp, SlackrunHelp: &config.SlackrunHelpSpec{}},
	}
	out := buildStdinPayload(stdinBuildInput{Parts: parts})
	if !strings.HasPrefix(out, "Lead:\n") {
		t.Errorf("missing text prefix: %q", out)
	}
	// Both write and read subcommands must appear in the injected help so
	// an LLM child sees the whole surface, not just half of it.
	wantSubcommands := []string{
		"slackrun post", "slackrun react", "slackrun upload",
		"slackrun history", "slackrun replies", "slackrun reactions",
		"slackrun user", "slackrun usergroups",
	}
	for _, want := range wantSubcommands {
		if !strings.Contains(out, want) {
			t.Errorf("injected help missing %q:\n%s", want, out)
		}
	}
}

func TestGenerateNonce_HexAndLength(t *testing.T) {
	t.Parallel()
	n := generateNonce()
	if len(n) != 8 {
		t.Fatalf("expected 8 hex chars, got %d (%q)", len(n), n)
	}
	for _, c := range n {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char %q in nonce %q", c, n)
		}
	}
}
