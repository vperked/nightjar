# Uptime checks

Uptime checks watch websites, apps and devices from this machine, record every result, and alert you when one goes down and again when it recovers.

## Add a check

Go to **[Services](/services)** and press **Add check**. Pick a type and a target:

| Type      | Target                       | Up when                                                                                      |
| --------- | ---------------------------- | -------------------------------------------------------------------------------------------- |
| HTTP      | `https://example.com`        | it answers with a status under 400, or one you list, and contains the keyword if you set one |
| TCP       | `nas.local:22`               | the port accepts a connection                                                                |
| Ping      | `192.168.0.1`                | it replies to ping                                                                           |
| TLS       | `example.com:443`            | its certificate is valid and not about to expire                                             |
| Docker    | `sonarr`                     | the container is running, not unhealthy, and not restarting over and over                    |
| Heartbeat | none: the job pings Nightjar | the job reports in on schedule. See [Heartbeats](/docs/uptime-checks#heartbeats)             |

Saving tries it once first. If it fails, you see why and can fix it, or **Save anyway** if the target is down for now.

Each check also has:

- **Interval**: how often it runs.
- **Timeout**: how long one try may take.
- **Retries**: how many extra failures in a row before it counts as down. A single blip is not an outage.
- For HTTP, **expected status codes** and a **keyword** the page must contain. For TLS, how many days ahead to warn about expiry.

## Heartbeats

Some jobs never answer a request: a nightly backup, a cron job, a sync. A **heartbeat** check waits for the job to report in instead. Give it the job's schedule and Nightjar makes a secret ping URL for it, shown on the check's page with commands to copy:

- `/ping/<token>` when the job finished well.
- `/ping/<token>/start` when it begins. Nightjar then times each run, and with **May run for** set, alerts when a run hangs.
- `/ping/<token>/fail`, or `/ping/<token>/<exit code>`, when it went wrong. Anything posted with a ping, such as the job's last lines of output, is kept and shown on the check's page and in the alert.

The schedule is a cron line (`0 3 * * *` runs at 3:00 every night), a shortcut (`@hourly`, `@daily`, `@weekly`), or `every 6h`, in the server's own time zone. The check is down when a run is missed by more than the **grace** (10 minutes unless you change it), when the job reports a failure, or when it runs too long. It is judged against the schedule, so a nightly backup counts as late at ten past three, not a day after its last ping.

The ping URL needs no sign-in: its token is the credential, so keep it like a password. Anyone who has it can report runs for that check, and nothing else.

## Containers

A **Docker** check watches a container by name, picked from the ones Docker has. It is down when the container stopped (and says why: its exit code, or that it was killed for using too much memory), when its own health check says unhealthy, or when it restarted three times or more in ten minutes, even if it is up between them. Nightjar talks to Docker at `DOCKER_SOCKET` (`/var/run/docker.sock` unless you change it).

A crashed container counts in [incidents](/docs/incidents) too: when the `sonarr` container stops and the `sonarr` web check goes down a moment later, the incident says the container took the check with it.

## Repairs

A check can repair what it watches when it goes down. Under **When it goes down**, choose:

- **Restart a service**: a systemd service, such as `plexmediaserver.service` behind a Plex check.
- **Restart a container**: a Docker container, such as `sonarr`.
- **Run a script**: an executable from the repairs folder (`REPAIRS_DIR`, `repairs` next to the database unless you change it). The script gets `NIGHTJAR_CHECK`, `NIGHTJAR_TARGET`, `NIGHTJAR_DETAIL` (why the check failed) and `NIGHTJAR_ATTEMPT` in its environment, and may run for two minutes.

Each outage gets a few attempts (two unless you change it), two minutes apart. After each one Nightjar checks again at once. You still get the down alert, then a note saying what Nightjar tried. When the check comes back after a repair, the recovery alert says what fixed it.

Every attempt is kept on the check's page, with the script's output and whether the check came back after it. **Try it now** runs the repair once, to test it.

Scripts run as Nightjar, often as root, so Nightjar only runs a script that belongs to root (or to Nightjar's own user) and that no other user can change, in a folder no other user can change. The same rule applies to plugins. A script that breaks the rule is listed in the check's form with the fix, such as `chmod go-w`. Scripts can only be added from the machine itself, never from the dashboard.

## What you see

Each check shows its status, its last checks as small bars, response time and uptime over 24 hours, 7 days and 30 days. Open one for its response-time chart over the last hour, 24 hours, 7 days or 30 days. Pause a check to stop it without losing its history.

## Alerts

A check alerts when it goes down, and again when it is back up. Repeats while it stays down follow the alert cooldowns under **[Settings → Monitoring](/settings)**. You can mute one check from its page. See [Notifications](/docs/notifications).

## Why a check is down

A down check's page explains its last error in plain words under **Why it is down**: _connection refused_ means nothing listens on that port, _HTTP 502_ means a proxy cannot reach the app behind it, an _x509_ error is a certificate problem, and so on, each with what to do. Down alerts carry the same line as **Likely cause**.

Down alerts also carry **Why** when something else on the machine points to the cause, such as the kernel stopping a program for memory or the service behind the check crashing a moment before. The whole story is on the [Incidents](/docs/incidents) page.

## History

Results are kept for `HISTORY_RETENTION_DAYS` (90 by default) in the monitor's database.

> **Tip:** a check can belong to a server you watch with the agent: pick it under **Server** in the check's form, and that server's page lists the check under **Services on it**. To see inside a machine (CPU, temperatures, disks), add it under [Servers](/docs/servers) with the agent.
