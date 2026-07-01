// Package clidoc holds the human-readable CLI usage snippets that several
// callers need verbatim: `slackrun -h`, the `slackrun_help` stdin part that
// injects the child-facing subcommand summary into a prompt, and tests
// pinning the canonical wording.
//
// Kept in its own package so cmd/slackrun and internal/slackapp can share
// the strings without importing internal/cli (which would create a cycle —
// internal/cli depends on internal/slackapp).
package clidoc

// ChildUsage is the help block describing every subcommand a spawned
// child can call back into slackrun with (both reads and writes). It is
// the literal text that the `slackrun_help` stdin part injects into the
// child's prompt.
//
// Keep this string self-contained: it ships unchanged into AI prompts,
// where surrounding instructions or examples cannot be assumed.
const ChildUsage = `Slack CLI callable from spawned children (requires expose_slack_token: true on the rule):

  Write:
    slackrun post      [--channel C...] [--thread-ts T] --text TEXT       (--text - reads stdin)
    slackrun update    [--channel C...] --ts T --text TEXT                (edit a prior message)
    slackrun ephemeral [--channel C...] [--user U...] --text TEXT [--thread-ts T]
    slackrun react     [--channel C...] [--ts T] --emoji NAME
    slackrun unreact   [--channel C...] [--ts T] --emoji NAME
    slackrun upload    [--channel C...] [--thread-ts T] --file PATH [--title T] [--initial-comment T]

  Read (JSON to stdout):
    slackrun history    [--channel C...] [--limit N] [--cursor CUR] [--oldest TS] [--latest TS]
    slackrun replies    [--channel C...] [--thread-ts T] [--limit N] [--cursor CUR]
    slackrun reactions  [--channel C...] [--ts T] [--full]
    slackrun channel    [--channel C...] [--include-num-members]
    slackrun channels   [--types public_channel,private_channel,im,mpim] [--exclude-archived] [--limit N] [--cursor CUR]
    slackrun user       [--user U...]                        (users.info)
    slackrun user       --email x@y.com                       (users.lookupByEmail — needs users:read.email)
    slackrun users      [--limit N] [--presence] [--team-id T]
    slackrun usergroups [--include-users] [--include-disabled] [--team-id T]
    slackrun file       --file F... [--output PATH]         (JSON metadata; --output downloads body)
    slackrun me                                              (this bot's identity from auth.test)

  Channel/ts/thread_ts/user default to SLACKRUN_CHANNEL / SLACKRUN_TS /
  SLACKRUN_THREAD_TS / SLACKRUN_USER, which slackrun injects on every spawn.
`

// MainUsage is the full ` + "`slackrun -h`" + ` block. ChildUsage is interpolated so
// the two stay in sync.
const MainUsage = `slackrun — dispatch Slack events to local commands

Dispatch:
  slackrun start [<rules.yaml>]                 Run the bot
  slackrun check <rules.yaml>                   Validate the rules file
  slackrun dry-run <rules.yaml> --event <file>  Show what would match (no spawn)
  slackrun replay <rules.yaml> --permalink URL  Replay one past message through the pipeline

` + ChildUsage + `
Misc:
  slackrun version                              Print version
`
