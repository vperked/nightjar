# Notifications, rules and quiet hours

## The history

Every alert is kept for 90 days, whether or not it was sent anywhere. **The bell** in the header counts unread warnings and criticals and shows the latest few. **[Notifications](/notifications)** lists them all: search them, filter by severity, area or unread, and open one to see its details, where it was sent, or why it wasn't, and a link to what it is about.

## Rules: who hears what

**[Notifications → Rules](/notifications/rules)** decides, for each channel and each area, how much it hears:

- **Everything**
- **Warnings and up** (recoveries included)
- **Critical only** (recoveries included)
- **Off**

The areas are uptime checks, system services, CPU, memory and network, disks and drives, the monitor and kernel, updates, qBittorrent, Plex, and other servers. Until you change them, Discord hears everything (qBittorrent only with its own webhook) and other channels hear `ALERT_CHANNELS_MIN_SEVERITY` and up. Rules apply as soon as they are saved.

## Quiet hours

Quiet hours hold alerts back overnight, 23:00 to 07:00 by default, and send one summary to each channel when they end. Critical alerts can still go straight out. Held alerts are marked _held_ in the history until the summary goes.

## Mutes

Mute a check, a system service, a drive or another server from its page, or a whole area from the rules, for 1, 4 or 24 hours. Its alerts are still kept in the history but sent nowhere. The [Rules](/notifications/rules) page lists everything muted, with how long is left.
