# Analytics

**[Servers → Analytics](/servers/analytics)** looks back over the last 24 hours, 7 days or 30 days for every server, this one included.

## What's on it

- **Uptime** across your other servers, with how many outages there were and how long they lasted.
- **Hottest** and **busiest**: the server with the highest temperature and the highest average CPU.
- **Data moved**: everything sent and received.
- **A table** with each server's uptime, average and peak CPU and temperature with trend lines, average memory, its fullest disk, and the data it moved.
- **Disk forecasts**: when a disk keeps filling, the table says when it will be full at that rate, such as _full in ~9 days_. It needs at least half a day of readings to say.
- **Trends**: pick CPU, temperature, memory, fullest disk or response time to see every server's chart side by side, on the same scale, so they compare at a glance.
- **Outages**, newest first: when each server stopped answering, and for how long. An outage is a minute in which a server missed most of its checks.

Each [server's page](/docs/servers) also switches its own charts between the live hour and the same ranges.

## How it is kept

Every server is recorded once a minute: its averages, its peaks, and how many of that minute's checks it answered. Minutes are kept for `HISTORY_RETENTION_DAYS` (90 by default), like the uptime checks. A range is shown as about 300 points; a stretch with no readings shows as a gap.

This machine has no uptime figure, because it can only record while it runs.

A server added recently says so in the table: _readings for 4% of the range_.
