// Package clidoc holds the human-readable CLI usage snippets that several
// callers need verbatim: `slackrun -h`, the `slackrun_help` stdin part that
// injects the write-CLI summary into a child's prompt, and tests pinning
// the canonical wording.
//
// Kept in its own package so cmd/slackrun and internal/slackapp can share
// the strings without importing internal/cli (which would create a cycle —
// internal/cli depends on internal/slackapp).
package clidoc

// WriteUsage is the help block describing the subcommands a spawned child
// can call back into slackrun with. It is the literal text that the
// `slackrun_help` stdin part injects into the child's prompt.
//
// Keep this string self-contained: it ships unchanged into AI prompts,
// where surrounding instructions or examples cannot be assumed.
const WriteUsage = `Write to Slack (requires expose_slack_token: true on the rule):
  slackrun post   [--channel C...] [--thread-ts T] --text TEXT    (--text - reads stdin)
  slackrun react  [--channel C...] [--ts T] --emoji NAME
  slackrun upload [--channel C...] [--thread-ts T] --file PATH [--title T] [--initial-comment T]
  Channel/ts/thread_ts default to SLACKRUN_CHANNEL / SLACKRUN_TS / SLACKRUN_THREAD_TS,
  which slackrun injects on every spawn.
`

// MainUsage is the full `slackrun -h` block. WriteUsage is interpolated so
// the two stay in sync.
const MainUsage = `slackrun — dispatch Slack events to local commands

Dispatch:
  slackrun start [<rules.yaml>]                 Run the bot
  slackrun check <rules.yaml>                   Validate the rules file
  slackrun dry-run <rules.yaml> --event <file>  Show what would match (no spawn)

` + WriteUsage + `
Misc:
  slackrun version                              Print version
`
