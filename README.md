# slackrun

Dispatches Slack events (`#channel` posts, `@bot` mentions) to local commands
based on a `rules.yaml` file. Single binary, macOS-friendly, Socket Mode only
(no public endpoint required).

The original use case was routing Sentry alerts and `@bot` mentions to local
Claude / Codex CLIs, but slackrun does not know anything about AI: any program
that prints to stdout can be wired up.

## How it works

1. A Slack event arrives over Socket Mode (`message` or `app_mention`).
2. slackrun matches the event against the rules in `~/.config/slackrun/rules.yaml`.
3. The configured command is spawned with the rule's `cwd`, environment, and
   template-expanded argv.
4. A `⏳ Working…` placeholder lands in a thread and updates every ~5s with
   elapsed time.
5. On exit, the placeholder is overwritten with stdout (chunked across
   multiple posts, or attached as a file, depending on length).

The `cwd` and `command` cannot be supplied from a Slack message — only a
matched rule can pick them. That is the main security boundary; see
`docs/security.md`.

## Write-back from the child

Rules can set `expose_slack_token: true` to forward `SLACK_BOT_TOKEN` to the
spawned process, which can then call back:

```sh
# inside the spawned command
slackrun post   --channel "$SLACKRUN_CHANNEL" --thread-ts "$SLACKRUN_THREAD_TS" --text "investigating…"
slackrun react  --channel "$SLACKRUN_CHANNEL" --ts "$SLACKRUN_TS" --emoji eyes
slackrun upload --channel "$SLACKRUN_CHANNEL" --thread-ts "$SLACKRUN_THREAD_TS" --file ./report.txt
```

`SLACKRUN_CHANNEL`, `SLACKRUN_TS`, `SLACKRUN_THREAD_TS`, `SLACKRUN_USER` are
injected on every spawn so the child does not have to parse anything. Read
`docs/security.md` before opting in — a child with the token can do anything
the Bot scope allows.

## Setup

```sh
# Build
go build -o slackrun ./cmd/slackrun

# Slack app + tokens
$EDITOR docs/setup-slack.md   # follow the steps

# Local config
mkdir -p ~/.config/slackrun
cp .env.example ~/.config/slackrun/.env
chmod 600 ~/.config/slackrun/.env
cp config/rules.yaml.example ~/.config/slackrun/rules.yaml
$EDITOR ~/.config/slackrun/.env ~/.config/slackrun/rules.yaml

./slackrun check ~/.config/slackrun/rules.yaml

# Foreground run (development)
./slackrun start

# Background (LaunchAgent)
./scripts/setup-launchagent.sh
```

## Docs

- `docs/setup-slack.md` — create the Slack app, scopes, sender detection
- `docs/setup-local.md` — dotenv layout, LaunchAgent, build notes
- `docs/rules.md` — `rules.yaml` reference, template variables, `check` / `dry-run`
- `docs/security.md` — trust boundaries, redact patterns, `.env` handling

## Repository layout

```
cmd/slackrun/      entrypoint (subcommand router)
internal/
  config/          env + rules loader (strict YAML)
  dispatch/        pure matcher + template expansion
  slackapp/        Socket Mode app, progress, reply, dedupe, jobs
  runner/          exec wrapper (SIGTERM→SIGKILL) + FIFO semaphore
  util/            redact, sanitize, chunk, time
  logging/         JSON stderr logger with PII scrub
  cli/             start / check / dry-run implementations
config/            rules.yaml.example
docs/              setup + operations docs
scripts/           setup-launchagent.sh
slack-app-manifest.yaml
```

## Operations

| Need to | Run |
|---|---|
| Validate config | `./slackrun check ~/.config/slackrun/rules.yaml` |
| See which rule matches an event | `./slackrun dry-run <rules.yaml> --event event.json` |
| Restart after config edit | `launchctl kickstart -k gui/$(id -u)/com.slackrun.slackrun` |
| Tail the log | `tail -f ~/Library/Logs/slackrun.log` |
| Stop | `launchctl bootout gui/$(id -u)/com.slackrun.slackrun` |
