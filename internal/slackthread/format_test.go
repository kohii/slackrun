package slackthread

import (
	"strings"
	"testing"
)

func userMsg(ts, user, text string) Message {
	return Message{TS: ts, Source: SourceUser, User: user, Text: text}
}
func botMsg(ts, bot, text string) Message {
	return Message{TS: ts, Source: SourceBot, Bot: bot, Text: text}
}
func selfMsg(ts, text string) Message {
	return Message{TS: ts, Source: SourceSelf, Text: text}
}

func threadTags(nonce string) (string, string) {
	open, close := tagPair(ThreadTagBase, nonce)
	return open, close
}

func messageTags(nonce string) (string, string) {
	open, close := tagPair(MessageTagBase, nonce)
	return open, close
}

func TestRenderThread_WrapsWithUntrustedTags(t *testing.T) {
	t.Parallel()
	open, close := threadTags("")
	out := RenderThread([]Message{userMsg("1.0", "U1", "hi")}, RenderOptions{})
	if !strings.HasPrefix(out, open+"\n") {
		t.Fatalf("missing begin tag: %q", out)
	}
	if !strings.Contains(out, "\n"+close+"\n") {
		t.Fatalf("missing end tag: %q", out)
	}
}

func TestRenderThread_NonceAppearsInBothTags(t *testing.T) {
	t.Parallel()
	out := RenderThread([]Message{userMsg("1.0", "U1", "hi")}, RenderOptions{Nonce: "abcd1234"})
	wantOpen, wantClose := threadTags("abcd1234")
	if !strings.Contains(out, wantOpen) {
		t.Fatalf("missing open tag with nonce: %q", out)
	}
	if !strings.Contains(out, wantClose) {
		t.Fatalf("missing close tag with nonce: %q", out)
	}
}

func TestRenderThread_EmptyMessagesReturnsEmpty(t *testing.T) {
	t.Parallel()
	// An empty thread renders to "" so the caller can elide the heading.
	if got := RenderThread(nil, RenderOptions{}); got != "" {
		t.Fatalf("expected empty string, got: %q", got)
	}
}

func TestRenderTriggerMessage_AlwaysEmitsWrapper(t *testing.T) {
	t.Parallel()
	out := RenderTriggerMessage(userMsg("1.0", "U1", "hi"), RenderOptions{Nonce: "abcd"})
	open, close := messageTags("abcd")
	if !strings.Contains(out, open) || !strings.Contains(out, close) {
		t.Fatalf("missing message-wrapper tags: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("missing body: %q", out)
	}
}

func TestRenderTriggerMessage_EmptyBodyStillWraps(t *testing.T) {
	t.Parallel()
	// Even with empty Text, the wrapper signals the trigger exists.
	out := RenderTriggerMessage(userMsg("1.0", "U1", ""), RenderOptions{})
	if out == "" {
		t.Fatal("expected non-empty output for empty trigger body")
	}
	open, _ := messageTags("")
	if !strings.Contains(out, open) {
		t.Fatalf("missing wrapper: %q", out)
	}
}

func TestRenderThread_TextFormat_SpeakerTags(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		userMsg("100.0", "U1", "parent"),
		botMsg("100.1", "Sentry", "alert"),
		selfMsg("100.2", "⏳ Working"),
		{TS: "100.3", Source: SourceUser, User: "U2", Text: "edited body", Edited: true},
	}
	out := RenderThread(msgs, RenderOptions{})
	checks := []string{
		"<@U1 user ts=100.0>: parent",
		"<bot Sentry ts=100.1>: alert",
		"[self bot ts=100.2]: ⏳ Working",
		"<@U2 user ts=100.3> (edited): edited body",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("missing %q in:\n%s", c, out)
		}
	}
}

func TestRenderThread_JSONLFormat_StillWrapped(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		userMsg("1.0", "U1", "hi"),
		botMsg("1.1", "Sentry", "alert"),
		selfMsg("1.2", "self"),
	}
	out := RenderThread(msgs, RenderOptions{Format: FormatJSONL, Nonce: "ABCD"})
	open, close := threadTags("ABCD")
	if !strings.Contains(out, open) || !strings.Contains(out, close) {
		t.Fatalf("jsonl render must still wrap with the untrusted tags: %q", out)
	}
	for _, want := range []string{
		`"user":"U1"`,
		`"bot":"Sentry"`,
		`"self_bot":true`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("jsonl missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderThread_IncludeTimestamps_AddsTimeAttr(t *testing.T) {
	t.Parallel()
	out := RenderThread(
		[]Message{userMsg("1700000000.0", "U1", "x")},
		RenderOptions{IncludeTimestamps: true},
	)
	if !strings.Contains(out, " time=") {
		t.Fatalf("expected time= attribute in:\n%s", out)
	}
}

func TestRenderThread_Files_LinkMode(t *testing.T) {
	t.Parallel()
	msg := Message{
		TS: "1.0", Source: SourceUser, User: "U1", Text: "see attached",
		Files: []File{{Name: "report.pdf", URL: "https://files.example/p"}},
	}
	out := RenderThread([]Message{msg}, RenderOptions{Files: FilesLink})
	if !strings.Contains(out, "[file: report.pdf url=https://files.example/p]") {
		t.Fatalf("expected file ref in:\n%s", out)
	}
}

func TestRenderThread_CapsMessageCount(t *testing.T) {
	t.Parallel()
	msgs := make([]Message, 60)
	for i := range msgs {
		msgs[i] = userMsg("t", "U1", "msg")
	}
	out := RenderThread(msgs, RenderOptions{MaxMessages: 10})
	if c := strings.Count(out, "msg"); c != 10 {
		t.Errorf("kept %d, want 10", c)
	}
	if !strings.Contains(out, "(50 messages omitted)") {
		t.Errorf("missing head-omitted marker in:\n%s", out)
	}
}

func TestRenderThread_BytesCapKeepsParentAndTail(t *testing.T) {
	t.Parallel()
	parent := userMsg("1.0", "U1", strings.Repeat("P", 200))
	msgs := []Message{parent}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, userMsg("1.x", "U2", strings.Repeat("X", 200)))
	}
	tail := userMsg("9.9", "U9", "LAST_MESSAGE_MARKER")
	msgs = append(msgs, tail)

	out := RenderThread(msgs, RenderOptions{MaxBytes: 800})
	if !strings.Contains(out, strings.Repeat("P", 200)) {
		t.Errorf("parent dropped under byte cap:\n%s", out)
	}
	if !strings.Contains(out, "LAST_MESSAGE_MARKER") {
		t.Errorf("tail dropped under byte cap (expected tail-priority):\n%s", out)
	}
	if !strings.Contains(out, "messages omitted") {
		t.Errorf("expected omitted marker:\n%s", out)
	}
	if len(out) > 800 {
		t.Errorf("rendered %d bytes, cap 800", len(out))
	}
}

func TestRenderThread_ParentAloneExceedsCap_TruncatesAtBoundary(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("A line of text.\n", 200)
	parent := userMsg("1.0", "U1", body)
	out := RenderThread([]Message{parent}, RenderOptions{MaxBytes: 300})
	if len(out) > 300 {
		t.Errorf("output %d bytes exceeds cap 300", len(out))
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("missing truncation marker:\n%s", out)
	}
}

func TestRenderThread_BelowEnvelopeCapReturnsEmptyWrapper(t *testing.T) {
	t.Parallel()
	out := RenderThread([]Message{userMsg("1.0", "U1", "long body")}, RenderOptions{MaxBytes: 5})
	open, close := threadTags("")
	want := open + "\n" + close + "\n"
	if out != want {
		t.Errorf("expected empty wrapper, got:\n%s", out)
	}
}

func TestTruncateText_PrefersLineBoundary(t *testing.T) {
	t.Parallel()
	s := "first line\nsecond line\nthird line"
	got := truncateText(s, 22)
	if !strings.HasSuffix(got, "second line") {
		t.Errorf("expected to cut on newline, got %q", got)
	}
}

func TestTruncateText_RuneBoundaryFallback(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("あ", 100)
	got := truncateText(s, 10)
	if len(got) > 10 {
		t.Errorf("got %d bytes, want <= 10", len(got))
	}
	if got != "" && !validUTF8(got) {
		t.Errorf("invalid UTF-8 in truncation: %q", got)
	}
}

func validUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
