// Package slackthread builds, formats, and fetches Slack thread context that
// can be fed to spawned commands (typically AI CLIs). Thread content is
// treated as untrusted user-generated data: the formatter wraps it in
// explicit BEGIN/END tags so prompt-injection by other users in the same
// thread is at least visible.
package slackthread

// Source classifies the origin of a Message for formatting and AI parsing
// purposes. The formatter renders a different speaker tag per source.
type Source int

const (
	// SourceUser is a human Slack user (`U`/`W` ID present).
	SourceUser Source = iota
	// SourceBot is any non-self bot, app, or webhook.
	SourceBot
	// SourceSelf is slackrun's own posting (progress messages, prior replies).
	// Tagged distinctly so the AI does not treat its own past output as user input.
	SourceSelf
	// SourceUnknown is the fallback when no identity could be determined.
	SourceUnknown
)

// Message is the normalized thread element. The same shape is used for
// API-fetched messages and for the no-thread fallback (a single synthesized
// element built from the triggering event).
type Message struct {
	TS     string
	Source Source
	// User is the Slack user ID, set only when Source == SourceUser.
	User string
	// Bot is a human-readable bot/app name (username → bot_profile.name →
	// bot_profile.app_id → bot_id), set only when Source == SourceBot.
	Bot string
	// Text is the raw message body. The formatter does not redact it; that
	// responsibility belongs to upstream redaction (Slack does not consent to
	// thread fetch carrying PII, but PII-bearing messages may exist).
	Text   string
	Edited bool
}
