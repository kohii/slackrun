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

func TestRender_WrapsWithUntrustedTags(t *testing.T) {
	t.Parallel()
	out := Render([]Message{userMsg("1.0", "U1", "hi")}, FormatOptions{})
	if !strings.HasPrefix(out, BeginTag+"\n") {
		t.Fatalf("missing begin tag: %q", out)
	}
	if !strings.Contains(out, "\n"+EndTag+"\n") {
		t.Fatalf("missing end tag: %q", out)
	}
}

func TestRender_TextFormat_SpeakerTags(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		userMsg("100.0", "U1", "parent"),
		botMsg("100.1", "Sentry", "alert"),
		selfMsg("100.2", "⏳ Working"),
		{TS: "100.3", Source: SourceUser, User: "U2", Text: "edited body", Edited: true},
	}
	out := Render(msgs, FormatOptions{})
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

func TestRender_JSONLFormat(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		userMsg("1.0", "U1", "hi"),
		botMsg("1.1", "Sentry", "alert"),
		selfMsg("1.2", "self"),
	}
	out := Render(msgs, FormatOptions{Format: FormatJSONL})
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

func TestRender_CapsMessageCount(t *testing.T) {
	t.Parallel()
	msgs := make([]Message, 60)
	for i := range msgs {
		msgs[i] = userMsg("t", "U1", "msg")
	}
	out := Render(msgs, FormatOptions{MaxMessages: 10})
	// 1 parent + 9 tail = 10 kept, 50 omitted.
	if c := strings.Count(out, "msg"); c != 10 {
		t.Errorf("kept %d, want 10", c)
	}
	if !strings.Contains(out, "(50 messages omitted)") {
		t.Errorf("missing head-omitted marker in:\n%s", out)
	}
}

func TestRender_BytesCapKeepsParentAndTail(t *testing.T) {
	t.Parallel()
	parent := userMsg("1.0", "U1", strings.Repeat("P", 200))
	msgs := []Message{parent}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, userMsg("1.x", "U2", strings.Repeat("X", 200)))
	}
	// Last few should be most-recent-first preferred.
	tail := userMsg("9.9", "U9", "LAST_MESSAGE_MARKER")
	msgs = append(msgs, tail)

	out := Render(msgs, FormatOptions{MaxBytes: 800})
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

func TestRender_ParentAloneExceedsCap_TruncatesAtBoundary(t *testing.T) {
	t.Parallel()
	// Build a parent with newlines so line-boundary truncation has somewhere to land.
	body := strings.Repeat("A line of text.\n", 200)
	parent := userMsg("1.0", "U1", body)
	out := Render([]Message{parent}, FormatOptions{MaxBytes: 300})
	if len(out) > 300 {
		t.Errorf("output %d bytes exceeds cap 300", len(out))
	}
	if !strings.Contains(out, "[truncated]") {
		t.Errorf("missing truncation marker:\n%s", out)
	}
}

func TestRender_BelowEnvelopeCapReturnsEmptyWrapper(t *testing.T) {
	t.Parallel()
	// MaxBytes smaller than the BEGIN/END tags themselves: there is no
	// useful body subset, so Render returns the empty wrapper unchanged
	// rather than emitting a partial output that exceeds the cap.
	out := Render([]Message{userMsg("1.0", "U1", "long body")}, FormatOptions{MaxBytes: 5})
	if out != BeginTag+"\n"+EndTag+"\n" {
		t.Errorf("expected empty wrapper, got:\n%s", out)
	}
}

func TestRender_EmptyMessagesProducesWrapperOnly(t *testing.T) {
	t.Parallel()
	out := Render(nil, FormatOptions{})
	if out != BeginTag+"\n"+EndTag+"\n" {
		t.Errorf("unexpected empty output:\n%s", out)
	}
}

func TestRender_DefaultsApply(t *testing.T) {
	t.Parallel()
	msgs := []Message{userMsg("1.0", "U1", "hi")}
	out := Render(msgs, FormatOptions{})
	if !strings.Contains(out, "hi") {
		t.Errorf("default render dropped content: %s", out)
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
	// multi-byte runes (Japanese) to verify we don't cut mid-rune.
	s := strings.Repeat("あ", 100) // 3 bytes per rune
	got := truncateText(s, 10)
	if len(got) > 10 {
		t.Errorf("got %d bytes, want <= 10", len(got))
	}
	// Must remain valid UTF-8.
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
