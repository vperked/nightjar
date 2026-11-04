# Alert channels

Alerts can go to Discord and to any of the channels below at once. Set them under **[Settings → Alert channels](/settings)**.

| Channel      | Settings                                                                           | Notes                                                                                                                                                                                                                                     |
| ------------ | ---------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Discord      | `WEBHOOK`                                                                          | A card coloured by severity, with the host's name, when it happened and, with `DASHBOARD_URL`, an **Open Nightjar** button. Optional extra webhooks split out disks (`DISK_WEBHOOK`), the kernel (`CRASH_WEBHOOK`), qBittorrent and Plex. |
| ntfy         | `NTFY_URL`, `NTFY_TOKEN`                                                           | Push to phones. Severity sets the priority.                                                                                                                                                                                               |
| Telegram     | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`                                           | Info alerts arrive silently.                                                                                                                                                                                                              |
| Slack        | `SLACK_WEBHOOK`                                                                    | An incoming webhook.                                                                                                                                                                                                                      |
| Email        | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TO` | Port 465 uses TLS, others STARTTLS.                                                                                                                                                                                                       |
| JSON webhook | `ALERT_WEBHOOK_URL`                                                                | Each alert POSTed as JSON, for home automation or your own tools.                                                                                                                                                                         |

**Send test alert** on the settings page sends one to every channel and says which ones failed, and why. It uses the values on screen, saved or not.

## Discord buttons

A Discord **bot** posts alerts with buttons to act on them, straight from Discord: **Check again** and **Run its repair** under a check, **Restart** under a system service, **Mute 1 hour** under either, and **Keep** under titles the media cleanup is about to remove.

1. In the [Discord developer portal](https://discord.com/developers/applications), make an application and add a bot to it. Invite it to your server with permission to send messages.
2. Under **Settings → Alert channels**, set `DISCORD_BOT_TOKEN`, the channel to post in (`DISCORD_CHANNEL_ID`), and the application's public key (`DISCORD_PUBLIC_KEY`).
3. Set the application's **Interactions Endpoint URL** to this dashboard's address followed by `/discord/interactions`. Discord must be able to reach it, so the dashboard needs a public address (through your reverse proxy).
4. List who may press the buttons in `DISCORD_ALLOWED_USERS`, as Discord user ids. Anyone else is told no.

Discord signs every press, and Nightjar checks the signature, so no one else can send them. Use the bot instead of the Discord webhook, not as well as it, or alerts arrive twice. Each press is kept in the [activity log](/docs/security#the-activity-log).

## Severities

Every alert has one, shown the same way everywhere:

- 🔴 **Critical**: down, crashed, out of space.
- 🟠 **Warning**: a threshold crossed.
- 🟢 **Recovered**: back to normal.
- 🔵 **Info**: reports and routine cleanup.

`ALERT_CHANNELS_MIN_SEVERITY` keeps routine alerts off the channels other than Discord, for example `WARNING` so your phone only buzzes for problems. A recovery always follows the problem it ends. For finer control per channel and per kind of alert, use [notification rules](/docs/notifications).

Set `DASHBOARD_URL` to this dashboard's address and alerts link back to it.

## Your own push server

The [app store](/docs/app-store) can install **ntfy** on this machine, so alerts reach your phone without a third-party service. After installing it, **Use it for alerts** makes it the monitor's ntfy channel.

## When an alert can't be delivered

Discord is capped at 30 messages a minute behind a circuit breaker, so a storm can't get the webhook blocked. Anything that can't be delivered is written to `ALERT_LOG_PATH`, and every alert is kept in the [notifications](/docs/notifications) history whether or not it went anywhere.
