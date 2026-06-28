# rules.yaml reference

`rules.yaml` is the single source of truth for what slackrun does when it sees
a Slack event. The strict YAML loader rejects unknown fields, so typos surface
at `check` time instead of being silently ignored.

## Shape

```yaml
rules:
  - name: <kebab-case>            # required, unique
    description: <free text>      # optional, shown in `check` output
    trigger:                      # required
      type: message | app_mention
      # — message —
      channel: C…                 # required for `type: message`
      from:                       # required for `type: message`
        bot_user_ids: [U…]
        app_ids: [A…]
        usernames: [SomeName]
      # — app_mention —
      keyword: <single token>     # optional; absent → default rule (max 1)
    action:                       # required
      cwd: /abs/path              # absolute paths only
      command: ["program", "arg", "{{text}}"]   # argv (no shell)
      timeout_ms: 600000          # required (milliseconds)
      env: { KEY: value }         # optional, extra env for the spawned process
```

## Matching

- Rules are evaluated top-to-bottom; **first match wins**.
- `type: app_mention` events match only `app_mention` rules;
  `type: message` events match only `message` rules.
- `app_mention` rules with `keyword`: the first non-mention token of the
  message body is compared **case-insensitively, exact match**. A keyword-less
  rule is the default and must be unique within the file.
- `type: message` rules trigger only if `trigger.from` matches one of:
  - the bot's `U`-prefixed user_id (= `event.user`, or `event.message.user`
    when subtype-nested)
  - the app's `A`-prefixed `app_id`
  - the bot's display `username`, compared case-insensitively
- `B`-prefixed `event.bot_id` is intentionally not cross-checked against
  `bot_user_ids`. If the bot only sends `bot_id`, use `app_ids` or `usernames`.

`trigger.from.usernames` is the weakest signal — any incoming webhook can pick
its own display name. Prefer `bot_user_ids` or `app_ids` when possible.

## Template variables

| Variable | Where it comes from |
|---|---|
| `{{permalink}}` | `chat.getPermalink` for the triggering message. Resolved lazily — only when the rule's command references it. |
| `{{text}}` | For mentions: body with `<@…>` stripped and whitespace collapsed. For messages: the raw text. |
| `{{rest}}` | For mentions: `text` minus the matched keyword. Equal to `text` for default mentions. |
| `{{channel}}` | Slack channel ID. |
| `{{user}}` | Slack user ID of the event author. |

Each argv element in `command` is template-expanded independently. The argv
is passed to `execve` directly — `;`, `$(...)`, backticks, etc. are inert.
If you need shell behaviour, wrap your command in `["sh", "-c", "..."]`.

## Validation

```sh
slackrun check ~/.config/slackrun/rules.yaml
```

Checks performed:

- Schema (strict YAML — typos and stray fields are rejected)
- Duplicate rule names
- Duplicate `keyword`s on `app_mention` (case-insensitive)
- Multiple keyword-less default mention rules (max one)
- Slack ID format (`C…` / `U…` / `A…`)
- `cwd` is absolute and exists
- `command` is a non-empty argv
- `timeout_ms` is positive
- Multiple `message` rules sharing a channel (warning — first match wins)

## Dry run

```sh
slackrun dry-run ~/.config/slackrun/rules.yaml \
  --event /path/to/sample-event.json \
  --self-user-id U… \
  --allowed-user-ids U…,U…
```

Prints a JSON report with the match kind (`matched` / `skip` / `unauthorized`
/ `no-match`), the resolved command argv, cwd, and the expanded template
variables. Spawns nothing. Useful for confirming a new rule before reloading.

## Hot reload

Not implemented. Restart instead:

```sh
launchctl kickstart -k gui/$(id -u)/com.slackrun.slackrun
```
