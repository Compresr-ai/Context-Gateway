# Slack Notifications Setup

Get notified on Slack when Claude Code needs your input.

## Quick Setup (Recommended)

Run `gateway` and enable Slack notifications in the config wizard — it opens your browser and guides you through the setup automatically.

## Manual Setup

### Option A: Webhook (Simplest)

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**
2. Name it `Claude Notify`, select workspace
3. Click **Incoming Webhooks** → Turn **ON**
4. Click **Add New Webhook to Workspace** → Select channel or DM
5. Copy the Webhook URL

Add to `.env` or `~/.config/context-gateway/.env`:
```bash
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T.../B.../xxx
```

### Option B: Bot Token (More Flexible)

1. Go to [api.slack.com/apps](https://api.slack.com/apps) → **Create New App** → **From scratch**
2. Go to **OAuth & Permissions** → Add `chat:write` scope
3. Click **Install to Workspace** → Copy the Bot Token (`xoxb-...`)
4. Find your User/Channel ID in Slack

Add to `.env` or `~/.config/context-gateway/.env`:
```bash
SLACK_BOT_TOKEN=xoxb-your-token
SLACK_CHANNEL_ID=U01ABC123
```

## How It Works

The hook script listens to Claude Code events:
- **`Stop`** — Claude finished, your turn
- **`Notification`** — Claude needs approval

## Requirements

- `jq` — `brew install jq` / `apt install jq`
