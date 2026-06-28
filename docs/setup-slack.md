# Setup: Slack app

One-time setup to create the Slack app and gather the tokens slackrun needs.

## 1. Create the app from the manifest

1. Visit https://api.slack.com/apps?new_app=1 and choose **From an app manifest**.
2. Pick the workspace and paste `slack-app-manifest.yaml` from this repo.
3. Review the scopes / event subscriptions, then create the app.

The manifest enables:

- Bot scopes: `app_mentions:read`, `chat:write`, `channels:history`, `groups:history`, `files:write`
- Bot events: `app_mention`, `message.channels`, `message.groups`
- Socket Mode (so no public endpoint is required)

## 2. Tokens

- **Bot User OAuth Token** (`xoxb-…`): *Install App* tab, then copy the bot
  token. Goes into `SLACK_BOT_TOKEN`.
- **App-Level Token** (`xapp-…`): *Basic Information* → *App-Level Tokens* →
  create one with `connections:write`. Goes into `SLACK_APP_TOKEN`.

Both go into `~/.config/slackrun/.env` (`chmod 600`). See `docs/setup-local.md`.

## 3. Invite the bot into channels

The bot has to be a member of any channel it should observe.

```
/invite @slackrun
```

Do this in whatever channel a `type: message` rule references.

## 4. Identify a `type: message` sender

A `type: message` rule filters by either:

- `bot_user_ids`: the `U`-prefixed ID the sender posts as. Visible by clicking
  the sender's name in Slack → *View profile* → the URL contains the user ID.
- `app_ids`: the `A`-prefixed app ID. Visible on the app's config in
  *Manage* → *Apps* → click the app.
- `usernames`: matched case-insensitively. Less robust because incoming
  webhooks can pick arbitrary names — prefer ID-based matching when the
  source supports it.

If you're unsure which is delivering messages, run with `LOG_LEVEL=debug` and
watch the `dispatcher no-match` log lines for the event payload.

## 5. Allowed mention users

`ALLOWED_USER_IDS` (comma-separated) in `.env` lists the users whose `@bot`
mentions slackrun will act on. Anyone else is silently ignored (logged as
`unauthorized`).

Your own `U…` ID is visible from your Slack profile (⋮ menu → *Copy member ID*).
