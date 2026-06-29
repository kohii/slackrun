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
      command: ["program", "arg"] # argv (no shell). NO `{{var}}` allowed here.
      timeout_ms: 600000          # required (milliseconds)
      env: { KEY: value }         # optional, extra env for the spawned process
      expose_slack_token: false   # optional; opt-in to forward SLACK_BOT_TOKEN
      stdin:                      # optional; piped to child's stdin
        parts:
          - text: "static instructions"
          - slack_thread:                    # fetches conversations.replies
              max_messages: 50               # cap on messages (default 50)
              max_bytes: 65536               # cap on rendered bytes (default 64 KiB)
              format: text                   # "text" (default) | "jsonl"
              on_fetch_error: fail           # "fail" (default) | "fallback_event"
          - template: "user: {{user}}"       # template variables expanded here
```

`action.env` cannot set reserved keys: `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`,
`ALLOWED_USER_IDS`, `SLACKRUN_CHANNEL`, `SLACKRUN_TS`, `SLACKRUN_THREAD_TS`,
`SLACKRUN_USER`. The first three are managed by `expose_slack_token`; the
`SLACKRUN_*` ones are injected automatically with the triggering event's
coordinates so the child can call `slackrun post|react|upload` without
parsing arguments.

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
| `{{permalink}}` | `chat.getPermalink` for the triggering message. Resolved lazily — only when a template part references it. |
| `{{text}}` | For mentions: body with `<@…>` stripped and whitespace collapsed. For messages: the raw text. |
| `{{rest}}` | For mentions: `text` minus the matched keyword. Equal to `text` for default mentions. |
| `{{channel}}` | Slack channel ID. |
| `{{user}}` | Slack user ID of the event author. |

`{{...}}` is **only allowed inside `action.stdin.parts[].template`**. The
rules loader rejects template tokens in `action.command` because expanded
argv leaks to other processes via `ps aux` (and untrusted Slack content
should never end up there).

## `action.stdin`

`stdin` is the structured recipe slackrun uses to build the byte stream
piped to the child's stdin. Each part is exactly one of:

- `text:` — literal string (no expansion).
- `template:` — string with `{{var}}` expansion (variables above).
- `slack_thread:` — declarative pointer to "the Slack thread this event lives
  in". slackrun calls `conversations.replies` before spawn and renders the
  result wrapped in `<UNTRUSTED_SLACK_THREAD>` / `</UNTRUSTED_SLACK_THREAD>`
  tags. Self bot messages (slackrun's own prior posts) are labelled
  `[self bot ts=…]` so the AI does not confuse them with user input.

Parts are concatenated in document order without separators — emit explicit
newlines in `text:` if you need spacing.

`slack_thread` sub-fields:

| Field | Default | Notes |
|---|---|---|
| `max_messages` | 50 | Hard cap on messages kept. Parent stays; tail wins on truncation. |
| `max_bytes` | 65536 | Cap on the rendered byte length of the part. |
| `format` | `text` | `text` (human-readable speaker tags) or `jsonl` (one JSON object per line). |
| `on_fetch_error` | `fail` | `fail` aborts the spawn with `❌ Thread fetch failed`. `fallback_event` synthesizes a single-message thread from the triggering event. |

### Rate limit

`conversations.replies` for personal / internal Slack apps is Tier 3 (50+
req/min) which is plenty for individual use. Externally-distributed apps
created after 2025-05-29 face a stricter 1 req/min limit; that mode is out
of scope for slackrun.

### YAML block scalar gotcha

Prefer `|` (preserve trailing newline) over `>` (folded) when authoring
multi-line `text:` / `template:` strings. Folded scalars collapse newlines
into spaces and break prompt formatting in subtle ways.

## Argv (`action.command`)

The argv is passed to `execve` directly — `;`, `$(...)`, backticks, etc. are
inert. If you need shell behaviour, wrap explicitly: `["bash", "-c", "..."]`.

`{{...}}` tokens are forbidden in argv elements. Use `stdin.parts[].template`
to feed variable content via stdin where it cannot leak to `ps aux`.

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
