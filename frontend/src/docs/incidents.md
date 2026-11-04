# Incidents

An uptime tool that checks from outside can tell you a service is down. Nightjar runs on the machine itself, so it can also tell you why. **[Incidents](/incidents)** gathers each stretch of trouble into one place: what went wrong, what happened on the machine in the minutes before and during it, what Nightjar did about it, and the likely cause.

## What makes an incident

Every warning and critical alert is part of an incident. Alerts within 15 minutes of each other belong to the same one, so a crash that takes three checks down reads as one incident, not three. An incident stays open while anything in it is still down, and ends when the last of it recovers.

Each incident shows:

- **The likely cause**, with how sure Nightjar is (_likely_ or _possible_), and other causes it noticed.
- **What was affected**: each check, service or drive, and how long until it came back.
- **The timeline**: the 15 minutes before it, each problem, Nightjar's own repairs, and each recovery, with their times from the start.
- **The machine around it**: memory and processor from half an hour before to ten minutes after, with the peaks.

## How the cause is found

Nightjar looks at the alerts from the 15 minutes before the incident to two minutes after it began, and at the machine's minute-by-minute readings. The strongest sign wins:

| Cause             | What points to it                                                              |
| ----------------- | ------------------------------------------------------------------------------ |
| Hardware fault    | a hardware, I/O or failing-drive alert from the kernel or a drive              |
| Machine restarted | an unexpected reboot, or a freeze the watchdog caught                          |
| Out of memory     | the kernel stopping a program for memory, or memory 95% full just before       |
| Disk full         | a disk alert that it was full, or a disk 97% full just before                  |
| Service crashed   | a systemd service failing just before a check with a matching name went down   |
| Network           | three or more checks failing within two minutes, or checks timing out together |
| After a change    | an update, install or restart in the 15 minutes before                         |
| Overloaded        | the processor at 95% or more just before                                       |

When none of these fits, the incident names what failed first, and the check's own error, explained as on its page.

The same reasoning runs as each check goes down, so the Discord alert carries a **Why** line while it is happening. The Overview shows the newest incident of the last three days at the top.

## Teach it

Under each incident, **Was that the cause?** takes a yes or no, what it really was if you know, and a note for later. Nightjar weighs what you say when it explains later incidents of the same checks and services: a cause you confirmed counts for more next time, one you rejected for less. When you have named the same cause for something twice, Nightjar offers it even when nothing else points there, marked _from your feedback_: "On this machine, plex going down has usually been a full disk."

## Tell visitors

When an incident affected checks on the [status page](/docs/status-page), **Tell visitors on the status page** opens a notice drafted for visitors: the names the page uses, how long, the cause in plain words ("a disk on the server filled up"), and whether it was fixed automatically. Nothing internal goes in it: no addresses, services or numbers. Edit it, then **Publish**. Published notices show under **Recent incidents** on the status page for 30 days, and an open incident's notice says when it ended once it has.

## Planned maintenance

Under **Incidents → [Maintenance](/incidents/maintenance)**, plan downtime before you update or move something: once (Saturday 02:00 to 03:30) or every week (every night 03:00 to 03:30). Choose the checks it covers, or **Everything**.

While a window is on, the checks it covers do not run: they do not alert, nothing repairs them, and the time does not count against their uptime. They show as _Maintenance_. A window over everything also holds back every other alert; those are kept in the [notification history](/docs/notifications), marked as raised during maintenance, and never open an incident. Turn on **Show it to visitors** to list the window on the status page from a week before, with a note.

## The weekly report

Every Monday at 09:00, Nightjar sends a summary of the week to Discord and wherever system alerts go: uptime and the lowest checks, the longest incidents and their causes, what its repairs fixed, what raised the most alerts, the week on Plex (streams, hours, who watched, the top show, the most at once, and what was added), and what is coming (a disk on course to fill within three months, a certificate expiring within 30 days, a heartbeat gone quiet). **Incidents → [Weekly report](/incidents/report)** shows the same thing at any time, and **Send it now** sends it. Change the day and time, or turn it off, with `WEEKLY_REPORT_AT` and `WEEKLY_REPORT` under **Settings → Dashboard**.

## Where it comes from

Incidents are built from the [notification history](/docs/notifications) and the [analytics](/docs/analytics) readings, so they reach back as far as those are kept. Muted and held alerts still count: muting stops the message, not the record.
