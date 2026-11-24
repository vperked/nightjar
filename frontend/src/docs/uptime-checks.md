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
| DNS       | `example.com`                | the name has records of the type you pick, and one contains the expected answer if you set one |
| PostgreSQL | `db.lan` or `db.lan:5432`   | the server is accepting connections. See [Databases](/docs/uptime-checks#databases)          |
| MySQL     | `db.lan` or `db.lan:3306`    | the server (MySQL or MariaDB) is accepting connections                                       |
| Redis     | `cache.lan` or `cache.lan:6379` | it answers PING                                                                           |
| Docker    | `sonarr`                     | the container is running, not unhealthy, and not restarting over and over                    |
| Heartbeat | none: the job pings Nightjar | the job reports in on schedule. See [Heartbeats](/docs/uptime-checks#heartbeats)             |

Saving tries it once first. If it fails, you see why and can fix it, or **Save anyway** if the target is down for now.

Each check also has:

- **Interval**: how often it runs.
- **Timeout**: how long one try may take.
- **Retries**: how many extra failures in a row before it counts as down. A single blip is not an outage.
- For HTTP, **expected status codes** and a **keyword** the page must contain. For TLS, how many days ahead to warn about expiry.
- For DNS, the **record type** (A, AAAA, CNAME, MX, TXT or NS), a **DNS server** to ask (empty uses this machine's resolver; set `1.1.1.1` or your own server to check that one), and an **expected answer**, such as the IP address the name should point to.

## Databases

The PostgreSQL, MySQL and Redis checks tell a database that is taking connections from one that is up but refusing them, without signing in, so they need no password:

- **PostgreSQL** sends the opening message a client sends, as `pg_isready` does. Being asked for a password, or turned away for an unknown user, counts as up. Starting up, shutting down, recovering, or _too many clients_ is down. The server may log a failed login for the user `nightjar`; that is the check.
- **MySQL and MariaDB** read the greeting the server sends on connect, and show its version. _Too many connections_ or a blocked host is down.
- **Redis** sends `PING`. `PONG` is up, and so is being asked for a password. `LOADING` (still reading its data after a restart) and other errors are down. A Redis with a password hides `LOADING` from a check without one, so give the check its **password** to catch that: it is sent with `AUTH` first, a refused password is down, and the password is kept on the server and never shown in the dashboard or in a backup without secrets.

The default port is used when the target has none.

## Heartbeats

Some jobs never answer a request: a nightly backup, a cron job, a sync. A **heartbeat** check waits for the job to report in instead. Give it the job's schedule and Nightjar makes a secret ping URL for it, shown on the check's page with commands to copy:

- `/ping/<token>` when the job finished well.
- `/ping/<token>/start` when it begins. Nightjar then times each run, and with **May run for** set, alerts when a run hangs.
- `/ping/<token>/fail`, or `/ping/<token>/<exit code>`, when it went wrong. Anything posted with a ping, such as the job's last lines of output, is kept and shown on the check's page and in the alert.

The schedule is a cron line (`0 3 * * *` runs at 3:00 every night), a shortcut (`@hourly`, `@daily`, `@weekly`), or `every 6h`, in the server's own time zone. The check is down when a run is missed by more than the **grace** (10 minutes unless you change it), when the job reports a failure, or when it runs too long. It is judged against the schedule, so a nightly backup counts as late at ten past three, not a day after its last ping. A run due while Nightjar was off is not counted as missed: its ping had nowhere to go.

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

Nightjar's own downtime is not counted against your checks. Nothing is checked while it is off, so that time is simply missing from the history rather than down. While the machine Nightjar runs on is starting up (and for 5 minutes after it boots) or shutting down, a failing check is not counted either: it is most likely failing because of that, not because the service is down. It is checked again soon, and counts as usual once the machine has settled.

## Alerts

A check alerts when it goes down, and again when it is back up. Repeats while it stays down follow the alert cooldowns under **[Settings → Monitoring](/settings)**. You can mute one check from its page. See [Notifications](/docs/notifications).

## Why a check is down

A down check's page explains its last error in plain words under **Why it is down**: _connection refused_ means nothing listens on that port, _HTTP 502_ means a proxy cannot reach the app behind it, an _x509_ error is a certificate problem, and so on, each with what to do. Down alerts carry the same line as **Likely cause**.

Down alerts also carry **Why** when something else on the machine points to the cause, such as the kernel stopping a program for memory or the service behind the check crashing a moment before. The whole story is on the [Incidents](/docs/incidents) page.

## History

Results are kept for `HISTORY_RETENTION_DAYS` (90 by default) in the monitor's database.

> **Tip:** a check can belong to a server you watch with the agent: pick it under **Server** in the check's form, and that server's page lists the check under **Services on it**. To see inside a machine (CPU, temperatures, disks), add it under [Servers](/docs/servers) with the agent.
