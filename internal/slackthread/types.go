// Package slackthread builds, formats, and fetches Slack thread context that
// can be fed to spawned commands. Thread content is treated as untrusted
// user-generated data: the formatter wraps it in explicit BEGIN/END tags
// (with a per-spawn random suffix on the tag name) so prompt-injection by
// other users in the same thread cannot escape the wrapper.
package slackthread

// Source classifies the origin of a Message for formatting and downstream
// parsing. The formatter renders a different speaker tag per source.
type Source int

const (
	// SourceUser is a human Slack user (`U`/`W` ID present).
	SourceUser Source = iota
	// SourceBot is any non-self bot, app, or webhook.
	SourceBot
	// SourceSelf is slackrun's own posting (progress messages, prior replies).
	// Tagged distinctly so downstream consumers do not treat the bot's own
	// past output as user input.
	SourceSelf
	// SourceUnknown is the fallback when no identity could be determined.
	SourceUnknown
)

// Message is the normalized thread element. The same shape is used for
// API-fetched messages and for the synthesized trigger-message render path.
type Message struct {
	TS     string
	Source Source
	// User is the Slack user ID, set only when Source == SourceUser.
	User string
	// Bot is a human-readable bot/app name (username → bot_profile.name →
	// bot_profile.app_id → bot_id), set only when Source == SourceBot.
	Bot string
	// Text is the rendered message body. Callers are expected to have
	// already flattened Block Kit / legacy attachments into this string
	// where appropriate (dispatch.MessageBody does this for the trigger
	// message; thread messages from conversations.replies use the bare
	// message body).
	Text   string
	Edited bool
	// Files lists file attachments. Rendered only when RenderOptions.Files
	// is FilesLink; otherwise ignored.
	Files []File
}

// File is one Slack file upload reference.
type File struct {
	Name string
	URL  string
}
