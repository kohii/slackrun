# Security notes

slackrun executes local commands triggered by Slack events. The boundaries
below are the design.

## Trust boundaries

| Boundary | Enforced by |
|---|---|
| `cwd` is never derived from Slack input | rules.yaml is the only source; absolute paths required, existence checked at boot. |
| `command` argv is never derived from Slack input (only template variables) | The argv lives entirely in rules.yaml. Template vars (`{{text}}` etc.) expand inside each arg but the array structure is fixed. |
| Only known senders trigger `type: message` rules | The schema makes `trigger.from` mandatory for `type: message`, with at least one of `bot_user_ids` / `app_ids` / `usernames`. |
| Only allowed users can `@bot` | `ALLOWED_USER_IDS` env var; everything else is logged `unauthorized` and dropped. |
| Self-loop prevention | `auth.test` resolves the bot's own user_id / bot_id; events matching either are skipped. |
| `event.text` does not leak to logs by default | The log layer strips `text` / `blocks` / `attachments` from any event passed in; only `ALLOW_RAW_EVENT_TEXT_LOG=true` (debug only) restores `text`, still after PII redaction. |

## Template expansion is text, not code

Each `command` array element is treated as a single argv string after
template expansion. There is no shell interpretation — `;`, `$(...)`,
backticks, redirection operators in expanded text are passed literally to the
program as part of its argument. A malicious mention saying

```
@bot run ; rm -rf ~
```

becomes argv `["claude", "-p", "run ; rm -rf ~"]`, not two shell commands.
If a rule explicitly opts into a shell with `["sh", "-c", "..."]`, then
shell-injection rules apply — keep template variables out of the script in
that case, or pass them as positional args (`["sh", "-c", "echo \"$1\"", "_", "{{text}}"]`).

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

The configured `command` is spawned as a child process. Its argv — including
any template-expanded text — is visible to other local users via `ps aux`.
**Don't run slackrun on a shared machine.** If you need to, choose programs
that read prompts over stdin and configure them accordingly.

## What slackrun does not protect against

- The downstream command itself: anything the spawned program can do, this
  bot can do. Use the program's own permission system (e.g. Claude Code's
  `.claude/settings.json`) to restrict tool / network access per workspace.
- Prompt injection from Slack messages: a malicious authorized user can ask
  the downstream program to do anything within its permissions. The
  mitigation is `ALLOWED_USER_IDS` (= "only you").
- The command's downstream actions (Notion writes, GitHub PRs, etc.). Those
  are the command's responsibility; slackrun just hands it argv + cwd.
