// Package clidoc holds the human-readable CLI usage snippets that several
// callers need verbatim: `slackrun -h`, the `slackrun_help` stdin part that
// injects the child-facing subcommand reference into a prompt, and tests
// pinning the canonical wording.
//
// Kept in its own package so cmd/slackrun and internal/slackapp can share
// the strings without importing internal/cli (which would create a cycle —
// internal/cli depends on internal/slackapp).
package clidoc

// subcommandTable lists every child-side subcommand with its signature.
// Shared verbatim between ChildUsage (agent prompt) and MainUsage
// (`slackrun -h`) so the two never drift. No standalone header: each
// caller supplies its own preamble.
const subcommandTable = `  Write (prints {"channel","ts"}; react/unreact print nothing):
    slackrun post      [--channel C...] [--thread-ts T] --text TEXT       (--text - reads stdin)
    slackrun update    [--channel C...] --ts T --text TEXT
    slackrun ephemeral [--channel C...] [--user U...] --text TEXT [--thread-ts T]
    slackrun react     [--channel C...] [--ts T] --emoji NAME
    slackrun unreact   [--channel C...] [--ts T] --emoji NAME
    slackrun upload    [--channel C...] [--thread-ts T] --file PATH [--title T] [--initial-comment T]

  Read (prints one JSON line):
    slackrun history    [--channel C...] [--limit N] [--cursor CUR] [--oldest TS] [--latest TS]
    slackrun replies    [--channel C...] [--thread-ts T] [--limit N] [--cursor CUR]
    slackrun reactions  [--channel C...] [--ts T] [--full]
    slackrun channel    [--channel C...] [--include-num-members]
    slackrun channels   [--types public_channel,private_channel,im,mpim] [--exclude-archived] [--limit N] [--cursor CUR]
    slackrun user       [--user U...]
    slackrun user       --email x@y.com
    slackrun users      [--limit N] [--presence] [--team-id T]
    slackrun usergroups [--include-users] [--include-disabled] [--team-id T]
    slackrun file       --file F... [--output PATH]
    slackrun me
`

// ChildUsage is the help block that the `slackrun_help` stdin part injects
// directly into a spawned child's prompt. Kept tight: the reader is the
// child (typically an LLM), and rule-author concerns (spawn semantics,
// `expose_slack_token`, SLACKRUN_* injection mechanics) are irrelevant to
// what it can do — they would only pad the prompt.
//
// Keep this string self-contained: it ships unchanged into AI prompts,
// where surrounding instructions or examples cannot be assumed.
const ChildUsage = "Call `slackrun` to interact with Slack. --channel / --ts / --thread-ts / --user default to the triggering event; pass them to target a different message.\n\n" + subcommandTable

// MainUsage is the full `slackrun -h` block. Addressed to the operator
// running the CLI, so it names host-level concerns (`expose_slack_token`
// gating, spawn-time env injection) that the child-facing ChildUsage
// intentionally leaves out.
const MainUsage = `slackrun — dispatch Slack events to local commands

Dispatch:
  slackrun start [<rules.yaml>]                 Run the bot
  slackrun check <rules.yaml>                   Validate the rules file
  slackrun dry-run <rules.yaml> --event <file>  Show what would match (no spawn)
  slackrun replay <rules.yaml> --permalink URL  Replay one past message through the pipeline

Child-side CLI (available inside a spawned process when the matched rule sets ` + "`expose_slack_token: true`" + ` so SLACK_BOT_TOKEN reaches the child):

` + subcommandTable + `
  Channel/ts/thread_ts/user default to SLACKRUN_CHANNEL / SLACKRUN_TS /
  SLACKRUN_THREAD_TS / SLACKRUN_USER, which slackrun injects on every spawn.

Misc:
  slackrun version                              Print version
`
