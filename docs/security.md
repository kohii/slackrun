# Security notes

slackrun executes local commands triggered by Slack events. The boundaries
below are the design.

## Trust boundaries

| Boundary | Enforced by |
|---|---|
| `cwd` is never derived from Slack input | rules.yaml is the only source; absolute paths required, existence checked at boot. |
| `command` argv is never derived from Slack input | The argv lives entirely in rules.yaml. `{{var}}` tokens are rejected in argv elements; variable content can only enter the child via `action.stdin`. |
| Only known senders trigger `type: message` rules | The schema makes `trigger.from` mandatory for `type: message`, with at least one of `user_ids` / `app_ids` / `usernames`, or an explicit `any: true`. |
| Only allowed users can `@bot` | Top-level `allowed_user_ids` in rules.yaml; everything else is logged `unauthorized` and dropped. Per-rule `trigger.from.user_ids` narrows the list further. |
| Self-loop prevention | `auth.test` resolves the bot's own user_id / bot_id; events matching either are skipped. |
| `event.text` does not leak to logs by default | The log layer strips `text` / `blocks` / `attachments` from any event passed in; only `ALLOW_RAW_EVENT_TEXT_LOG=true` (debug only) restores `text`, still after PII redaction. |

## Template expansion is text, not code

`{{event.*}}` metadata variables are allowed only inside `text:` parts of
`action.stdin`, and Slack-derived message bodies enter stdin only through
`trigger_message:` / `thread:` parts (which always XML-wrap their content).
slackrun writes the resulting bytes to the child's stdin pipe — never to
argv, never through a shell. There is no shell interpretation, no `ps aux`
exposure for the expanded body, no shell-quoting hazard.

A malicious mention saying

```
@bot run ; rm -rf ~
```

becomes a stdin payload like `run ; rm -rf ~` (inside
`<UNTRUSTED_SLACK_MESSAGE_…>` tags) and the child reads it as data, not
commands. If a rule explicitly opts into a shell via `["bash", "-c",
"..."]`, the shell script itself lives in rules.yaml (still no Slack input)
— the only way Slack content reaches that script is via stdin or
`SLACKRUN_*` env vars, both of which are safe under `"$VAR"`-quoted shell
reads.

## Slack message / thread context is untrusted

When a rule's `action.stdin` includes `trigger_message:` or `thread:`,
slackrun renders the triggering message and / or the result of
`conversations.replies` wrapped in:

```
<UNTRUSTED_SLACK_MESSAGE_ab12cd34>
...
</UNTRUSTED_SLACK_MESSAGE_ab12cd34>

<UNTRUSTED_SLACK_THREAD_ab12cd34>
...
</UNTRUSTED_SLACK_THREAD_ab12cd34>
```

The trailing `_ab12cd34` is a per-spawn random suffix on the tag name, so a
Slack body that writes a literal `</UNTRUSTED_SLACK_THREAD>` cannot escape
the wrapper.

Authorization (top-level `allowed_user_ids`) gates only the **trigger**
event, not the thread's history. Other users' (and bots') prior messages
in the same thread are still piped to the child. Treat them as untrusted
data and instruct the downstream AI to consider its system prompt the
authority, not anything inside the tags.

slackrun's own prior replies are labelled `[self bot ts=...]` so the AI can
distinguish them from genuine user input. Self-detection compares Slack's
`user` field against `auth.test`'s `user_id`, and `bot_id` against
`auth.test`'s `bot_id`. If the bot token does not expose a `bot_id` (some
user-token configurations), prior progress messages may be rendered as
generic bot output instead. Boot logs warn when `bot_id` is missing.

## PII redaction

`internal/util/redact.go` runs on every outbound string (Slack post, log line,
file upload, error message). It masks:

- Slack tokens (`xox*-…`, `xapp-…`)
- JWTs (`eyJ…`)
- `Bearer …` headers
- AWS access keys (`AKIA…` / `ASIA…`)
- GitHub tokens (`gh[pousr]_…`)
- Emails
- Query-string token params (`?access_token=`, `?api_key=`, etc.)

Phone numbers are **deliberately not** redacted by default — the obvious
pattern (`\d{3,4}-…`) also captures Slack IDs and timestamps, producing more
diagnostic noise than benefit.

A boot self-check verifies each pattern still strips a representative fixture
(`util.RunRedactSelfCheck`). Failures are logged but the bot keeps running so
missing masks do not block real alerts.

## Child CLI (writes: `post|update|ephemeral|react|unreact|upload`; reads: `history|replies|reactions|channel|channels|user|users|usergroups|file|me`)

When a rule sets `expose_slack_token: true`, the spawned child receives
`SLACK_BOT_TOKEN` and can call back into slackrun for both writes
(`post` / `update` / `ephemeral` / `react` / `unreact` / `upload`) and
reads (`history` / `replies` / `reactions` / `channel` / `channels` /
`user` / `users` / `usergroups` / `file` / `me`) — or, equivalently, call
the Slack API directly with `curl`. **The CLI's PII redaction and
operation allow-list are conveniences, not enforced boundaries.**
Anything the child can do with the token, slackrun can't prevent.

In practice that means: a child with `expose_slack_token: true` is a
**trusted child holding the full Bot scope**. Use the rule's `cwd` and the
program's own permissions (e.g. Claude Code's `.claude/settings.json`) to
limit what it can actually do.

Sanitisation that *is* applied when the child calls slackrun's CLI:

| Subcommand | Sanitised fields |
|---|---|
| `post` / `update` / `ephemeral` | `--text` body |
| `upload` | `--file` content, `--title`, `--initial-comment`, the filename Slack displays |
| `react` / `unreact` | nothing — emoji name and Slack IDs only, no free-form input |
| read subcommands (incl. `file --output`) | none — the JSON body / file bytes flowing to the child's stdout are unredacted. Treat them as untrusted Slack input on the child's side. |

slackrun also injects four read-only vars into the child's env so the CLI
calls can reference the triggering event without parsing rules:

- `SLACKRUN_CHANNEL`
- `SLACKRUN_TS`
- `SLACKRUN_THREAD_TS`
- `SLACKRUN_USER`

## Child environment

godotenv loads `.env` into the parent process's environment at startup, so
`SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN` are visible to slackrun itself.
`os.Environ()` would otherwise flow straight to the spawned command;
slackrun now filters that pass-through:

| Variable | Default child visibility |
|---|---|
| `SLACK_BOT_TOKEN` | hidden — set `expose_slack_token: true` on the rule to opt in |
| `SLACK_APP_TOKEN` | always stripped (Socket Mode is the parent's job) |
| `ALLOWED_USER_IDS` | always stripped (defence-in-depth; authorization lives in rules.yaml) |
| Everything else | passed through unchanged |

`action.env` can override any non-reserved variable. Writing a reserved key
(`SLACK_*`, `ALLOWED_USER_IDS`, `SLACKRUN_*`) into `action.env` is rejected at
load time; even if a caller smuggles one through `runner.Options.Env`, the
final strip pass in `buildEnv` drops it.

## Admin API (`slackrun runs` / `slackrun kill`)

`slackrun start` opens a UNIX-domain socket for its admin surface. Two
clients speak to it: `slackrun runs` (list in-flight children) and
`slackrun kill` (send SIGTERM). There is no TCP listener.

| Boundary | Enforced by |
|---|---|
| Only the same OS user can call the API | The socket file is `chmod 0600` in a per-user directory (`$XDG_RUNTIME_DIR/slackrun/` on Linux, `$TMPDIR` on macOS). Any other UID on the box gets EPERM. |
| The child-facing help (`slackrun_help` stdin part, `ChildUsage`) does **not** advertise `runs` / `kill`. | `internal/clidoc/usage.go` lists them only in `MainUsage`. |
| Kill requests are best-effort SIGTERM (then SIGKILL after 5s) on the child's process group | `runner.Handle.Kill` uses `-pgid` so shell wrappers are torn down too. |

Trust model: **any process running as the same OS user** — including
slackrun's own spawned children — can talk to the daemon. `runner.buildEnv`
strips `SLACKRUN_ADMIN_SOCKET` from the child's env so an untargeted
`slackrun kill` from a child doesn't just work "by accident", but a child
that resolves the default socket path can still reach the API. If that
matters for your rule set, run those children under a different UID (e.g.
sandboxed via macOS's `sandbox-exec` or Linux user namespaces) — the
socket's file permission is the enforcement, not the env var.

The socket path can be overridden with `SLACKRUN_ADMIN_SOCKET=/path/to/x.sock`
or disabled outright with `SLACKRUN_ADMIN_SOCKET=off`. Do not run slackrun
as a user that shares a UID with untrusted software.

## Dedupe and boot-time replay

`Dedupe` rejects events older than `bootTime - MIN_EVENT_AGE_MS_AT_BOOT` so a
Socket Mode reconnect after a long downtime does not flood live channels with
hours-old alerts. After that startup window the TTL map alone catches
duplicates.

The cutoff is a static timestamp computed once at boot. An event whose Slack
`ts` lands on either side of it within the same restart will be processed —
duplicate handling falls back to the TTL map. In practice the only way to
trigger duplicate-and-not-caught behaviour is a restart that lasts longer than
the TTL window plus the boot cutoff, which is well outside normal operation.
Tune `MIN_EVENT_AGE_MS_AT_BOOT` if you care.

## `.env` and rotation

- File lives at `~/.config/slackrun/.env`. `setup-launchagent.sh` enforces
  `chmod 600` on every run.
- Tokens are **not** written into the plist. godotenv loads them at startup;
  restart the LaunchAgent after rotation.
- godotenv defaults to non-override behaviour, so a value already in
  `process.env` (e.g. from the plist) wins. Don't put tokens there.

## Command-line argument exposure

The configured `command` is spawned as a child process. Its argv is visible
to other local users via `ps aux`. slackrun keeps Slack content **out of
argv** by forbidding `{{var}}` in `action.command` and routing variable
content through `action.stdin` instead.

Downstream programs that re-expose stdin content via their own argv (e.g. a
shell wrapper `["bash", "-c", "claude -p \"$SLACKRUN_THREAD\""]`) defeat
this protection — `claude`'s argv will hold the thread. Prefer stdin-aware
forms when you can:

```yaml
command: [claude, -p]                       # claude reads prompt from stdin
# OR
command: [bash, -c, 'claude -p "$(cat)"']   # cat reads stdin in the shell
```

**Don't run slackrun on a shared machine.**

## What slackrun does not protect against

- The downstream command itself: anything the spawned program can do, this
  bot can do. Use the program's own permission system (e.g. Claude Code's
  `.claude/settings.json`) to restrict tool / network access per workspace.
- Prompt injection from Slack messages: a malicious authorized user can ask
  the downstream program to do anything within its permissions. The
  mitigation is `allowed_user_ids` (= "only you").
- The command's downstream actions (Notion writes, GitHub PRs, etc.). Those
  are the command's responsibility; slackrun just hands it argv + cwd.
