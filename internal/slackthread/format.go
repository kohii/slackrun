package slackthread

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Wrapping tags. We mark the body explicitly as untrusted so a downstream AI
// can be instructed to treat instructions inside as data rather than orders.
// The tag wording is intentionally verbose and English-only to be visible in
// any Anthropic / OpenAI prompt-injection mitigation playbook.
const (
	BeginTag = "<UNTRUSTED_SLACK_THREAD>"
	EndTag   = "</UNTRUSTED_SLACK_THREAD>"
)

// Defaults for FormatOptions zero values.
const (
	DefaultMaxMessages = 50
	DefaultMaxBytes    = 64 * 1024
)

// Format selects the output shape of Format().
type Format string

const (
	FormatText  Format = "text"
	FormatJSONL Format = "jsonl"
)

// FormatOptions configures Format(). Zero-values resolve to package defaults.
type FormatOptions struct {
	Format      Format
	MaxMessages int
	MaxBytes    int
}

// Render produces the formatted thread context with BEGIN/END tags. The
// returned string is suitable for piping to a child's stdin (or composing
// into a larger prompt). Parent is always retained; tail messages are
// preferred when truncation is necessary because the most recent context is
// usually the most relevant.
//
// If MaxBytes is so small that even the wrapping tags do not fit, Render
// returns the empty wrapper unchanged — there is no useful subset to emit.
func Render(msgs []Message, opts FormatOptions) string {
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = DefaultMaxMessages
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	fmtr := pickFormatter(opts.Format)

	empty := wrap("")
	if opts.MaxBytes < len(empty) {
		// Cap is too small to fit even the wrapping tags. Returning the
		// empty wrapper is the closest in-spec output.
		return empty
	}
	if len(msgs) == 0 {
		return empty
	}

	// Step 1: cap message count. Parent + last (N-1) gets us within bounds.
	pre, omittedCount := capMessages(msgs, opts.MaxMessages)

	// Step 2: try rendering everything we kept. The common path: nothing to do.
	body := renderBody(pre, omittedCount, fmtr)
	wrapped := wrap(body)
	if len(wrapped) <= opts.MaxBytes {
		return wrapped
	}

	// Step 3: byte cap exceeded. Keep parent + as many tail messages as fit,
	// with a marker for the gap.
	return shrinkToFit(pre, omittedCount, fmtr, opts.MaxBytes)
}

func wrap(body string) string {
	if body == "" {
		return BeginTag + "\n" + EndTag + "\n"
	}
	return BeginTag + "\n" + body + "\n" + EndTag + "\n"
}

func capMessages(msgs []Message, maxMsgs int) ([]Message, int) {
	if len(msgs) <= maxMsgs {
		return msgs, 0
	}
	parent := msgs[0]
	tailStart := len(msgs) - (maxMsgs - 1)
	out := make([]Message, 0, maxMsgs)
	out = append(out, parent)
	out = append(out, msgs[tailStart:]...)
	return out, tailStart - 1
}

// formatter abstracts the text vs jsonl representation. Each implementation
// chooses how to encode one Message and how to join multiple ones.
type formatter interface {
	formatOne(m Message) string
	joiner() string
	omittedMarker(n int) string
}

func pickFormatter(f Format) formatter {
	if f == FormatJSONL {
		return jsonlFormatter{}
	}
	return textFormatter{}
}

type textFormatter struct{}

func (textFormatter) formatOne(m Message) string {
	speaker := speakerTag(m)
	edited := ""
	if m.Edited {
		edited = " (edited)"
	}
	return fmt.Sprintf("%s%s: %s", speaker, edited, m.Text)
}

func (textFormatter) joiner() string { return "\n\n" }

func (textFormatter) omittedMarker(n int) string {
	return fmt.Sprintf("... (%d messages omitted) ...", n)
}

type jsonlFormatter struct{}

func (jsonlFormatter) formatOne(m Message) string {
	obj := map[string]any{
		"ts":   m.TS,
		"text": m.Text,
	}
	switch m.Source {
	case SourceUser:
		obj["user"] = m.User
	case SourceBot:
		obj["bot"] = m.Bot
	case SourceSelf:
		obj["self_bot"] = true
	}
	if m.Edited {
		obj["edited"] = true
	}
	b, _ := json.Marshal(obj)
	return string(b)
}

func (jsonlFormatter) joiner() string { return "\n" }

func (jsonlFormatter) omittedMarker(n int) string {
	b, _ := json.Marshal(map[string]any{"omitted": n})
	return string(b)
}

func speakerTag(m Message) string {
	switch m.Source {
	case SourceUser:
		return fmt.Sprintf("<@%s user ts=%s>", m.User, m.TS)
	case SourceBot:
		name := m.Bot
		if name == "" {
			name = "unknown"
		}
		return fmt.Sprintf("<bot %s ts=%s>", name, m.TS)
	case SourceSelf:
		return fmt.Sprintf("[self bot ts=%s]", m.TS)
	default:
		return fmt.Sprintf("<? ts=%s>", m.TS)
	}
}

// renderBody assembles the message-list body. omittedCount tracks how many
// messages were dropped at the head (from the N-message cap, before any byte
// truncation).
func renderBody(msgs []Message, omittedHead int, fmtr formatter) string {
	parts := make([]string, 0, len(msgs)+1)
	if len(msgs) > 0 {
		parts = append(parts, fmtr.formatOne(msgs[0]))
	}
	if omittedHead > 0 && len(msgs) > 1 {
		parts = append(parts, fmtr.omittedMarker(omittedHead))
	}
	for i := 1; i < len(msgs); i++ {
		parts = append(parts, fmtr.formatOne(msgs[i]))
	}
	return strings.Join(parts, fmtr.joiner())
}

// shrinkToFit handles the case where the rendered body exceeds maxBytes. The
// strategy: keep parent, walk tail messages from the end accepting whichever
// fit. If even parent alone is too big, truncate parent at a line boundary
// (rune boundary as last resort) and tag the result with [truncated].
//
// omittedHead represents messages already dropped by capMessages (the N
// cap); we render that gap with one omitted marker. A second marker reports
// any further messages dropped here between parent and the kept tail.
func shrinkToFit(msgs []Message, omittedHead int, fmtr formatter, maxBytes int) string {
	parent := msgs[0]
	parentStr := fmtr.formatOne(parent)
	envelopeOverhead := len(wrap(""))
	joiner := fmtr.joiner()

	// Parent alone (with envelope) doesn't fit → truncate parent body.
	if envelopeOverhead+len(parentStr) > maxBytes {
		const truncMarker = "\n... [truncated]"
		budget := maxBytes - envelopeOverhead - len(truncMarker)
		if budget < 0 {
			budget = 0
		}
		return wrap(truncateText(parentStr, budget) + truncMarker)
	}

	// renderWith builds the candidate body for the given tail size, choosing
	// whether to emit a "gap" omitted marker between parent and tail. We try
	// the largest tail and shrink until it fits.
	renderWith := func(tailCount int) string {
		parts := []string{parentStr}
		if omittedHead > 0 {
			parts = append(parts, fmtr.omittedMarker(omittedHead))
		}
		totalTail := len(msgs) - 1
		gap := totalTail - tailCount
		if gap > 0 {
			parts = append(parts, fmtr.omittedMarker(gap))
		}
		for i := len(msgs) - tailCount; i < len(msgs); i++ {
			parts = append(parts, fmtr.formatOne(msgs[i]))
		}
		return strings.Join(parts, joiner)
	}

	totalTail := len(msgs) - 1
	for k := totalTail; k >= 0; k-- {
		body := renderWith(k)
		if envelopeOverhead+len(body) <= maxBytes {
			return wrap(body)
		}
	}
	// Unreachable in practice: parent alone fit (checked above), so k=0
	// should always succeed. Defensive return.
	return wrap(parentStr)
}

// truncateText shortens s to at most maxBytes, preferring a newline boundary
// when one exists in the last 25% of the budget. Falls back to a rune
// boundary so we never split a UTF-8 sequence.
func truncateText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	cut := maxBytes
	// Try line boundary in the last quarter of the window.
	minLine := maxBytes - maxBytes/4
	if i := strings.LastIndexByte(s[:maxBytes], '\n'); i >= minLine {
		return s[:i]
	}
	// Fall back to rune boundary.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
