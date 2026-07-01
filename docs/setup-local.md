# Setup: local runtime

macOS only. Assumes Go 1.25+ to build (see `go.mod`) and at least one of the
programs your rules reference already installed in PATH.

## 1. Build

```sh
go build -o slackrun ./cmd/slackrun
go test ./...
```

A single binary lands at `./slackrun`. Move it wherever you like — the
LaunchAgent script honours `SLACKRUN_BIN` if it's not next to the script.

## 2. Configure

slackrun reads two files from `~/.config/slackrun/`:

| File | Purpose |
|---|---|
| `.env` | Slack tokens and optional knobs (`MAX_CONCURRENT`, `LOG_LEVEL`, …). Permissions must be `600`. |
| `rules.yaml` | Event-to-command routing, plus top-level `allowed_user_ids` for @mention authorization. Validate with `slackrun check`. |

```sh
mkdir -p ~/.config/slackrun
cp .env.example ~/.config/slackrun/.env
chmod 600 ~/.config/slackrun/.env
$EDITOR ~/.config/slackrun/.env

cp config/rules.yaml.example ~/.config/slackrun/rules.yaml
$EDITOR ~/.config/slackrun/rules.yaml

./slackrun check ~/.config/slackrun/rules.yaml
```

For development you can keep the files inside the repo and override via
`SLACKRUN_ENV_PATH=./.env SLACKRUN_CONFIG_PATH=./config/rules.yaml ./slackrun start`.

## 3. Try it in foreground

```sh
./slackrun start
```

`auth.test` runs on boot — a bad `SLACK_BOT_TOKEN` exits fast with a clear
error. Once you see `bot ready` in the JSON log, mention the bot in Slack from
an allowed user and watch the logs on stderr.

## 4. LaunchAgent (background)

```sh
./scripts/setup-launchagent.sh
```

The script bakes a deduped PATH into the plist's `EnvironmentVariables` and
loads the agent. Re-running unloads the previous version first. Override the
binary path with `SLACKRUN_BIN=/abs/path/to/slackrun ./scripts/setup-launchagent.sh`.

Loaded files:

- Plist: `~/Library/LaunchAgents/com.slackrun.slackrun.plist`
- Log: `~/Library/Logs/slackrun.log` (both stdout and stderr — slackrun itself
  only writes JSON to stderr)

Inspect:

```sh
launchctl print gui/$(id -u)/com.slackrun.slackrun
tail -f ~/Library/Logs/slackrun.log
```

Force-restart after an env change:

```sh
launchctl kickstart -k gui/$(id -u)/com.slackrun.slackrun
```

Stop:

```sh
launchctl bootout gui/$(id -u)/com.slackrun.slackrun
```

## 5. Health check

If a crash leaves an orphaned child process around, the dispatcher's two-stage
kill (`SIGTERM → SIGKILL` after 5s) usually cleans up. On hard kernel kills
you may have to do it by hand:

```sh
pgrep -lf claude | grep -v slackrun
```

(Substitute whatever program your rules invoke.)

## 6. macOS sleep

`caffeinate -s` keeps the process alive while on AC power. On battery the
system can still suspend Socket Mode; Slack does not guarantee replay of
events delivered during that window. If an alert is critical, keep a
redundant notification path (email, PagerDuty).
