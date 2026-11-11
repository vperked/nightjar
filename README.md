# Nightjar

Host health and service uptime monitoring with Discord alerts, a React dashboard and a public status page.

*A nightjar is a bird that comes alive after dark. This one keeps watch over your servers through the night, and wakes you when a disk fills at 3 am.*

**How to use it** is in the dashboard itself, under **Help and docs** in the account menu (or `/docs`,
which opens before signing in too): guides for every feature, searchable. The same guides are Markdown
in [`frontend/src/docs`](frontend/src/docs), readable here on GitHub; add a page there and list it in
`frontend/src/docs/index.ts`. A test checks that every link in them leads somewhere real.

The monitor is split into two parts:

```
backend/                      Go service: monitoring loop, alerts, JSON API
├── cmd/nightjar/        Entry point (-test, -install-systemd, -env)
├── cmd/nightjar-agent/  The agent for watching other servers (install, uninstall)
└── internal/
    ├── config/               .env loading and validated settings
    ├── collect/              CPU, memory, disks, network, GPU (nvidia-smi), sensors, processes
    ├── monitor/              The loop and its handlers: resource, disk, log, kernel, freeze,
    │                         torrent, service and update checks, plus the watchdog
    ├── services/             HTTP(S) / TCP / ping / TLS-expiry checks with retries
    ├── history/              SQLite check history (uptime %, response-time series)
    ├── alerts/               Escalating cooldowns and flap suppression
    ├── discord/              Webhook client (rate limit, circuit breaker, fallback log) and embeds
    ├── cleanup/              Log trimming, rotated-log removal, temp and cache cleanup
    ├── kernel/               Crash, hardware-error, stability and reboot detection
    ├── qbittorrent/          qBittorrent Web API client and cleanup policy
    ├── plex/                 Plex Media Server client: streams, transcodes, libraries, updates
    ├── api/                  HTTP API, auth, and static serving of the built frontend
    └── systemd/              Service installation
backend/test/                 Go tests, one folder per package (black-box: public APIs only)
frontend/                     React + TypeScript (Vite); builds into backend/internal/web/dist (embedded)
├── src/
│   ├── pages/                Dashboard, service detail (response-time chart), Plex, public status page
│   ├── components/           Cards, meters, sparklines, uptime bars, latency chart
│   └── api/                  Typed API client
└── test/                     Vitest tests
```

## Quick start

You need Go 1.26+ and Node 20.19+. (`go.mod` pins the patched Go 1.27.3 toolchain; with the default `GOTOOLCHAIN=auto` Go downloads it on first build.)

```bash
cp env.recommended .env        # set WEBHOOK, thresholds, etc.
make                           # builds the UI and one self-contained binary: bin/nightjar
./bin/nightjar            # dashboard on WEB_HOST:WEB_PORT (default http://127.0.0.1:8080)
```

### The admin account

The dashboard shows nothing until you create its admin account. The first time you open it, it asks for
a username, a password (10 characters or more) and a **setup code**: a one-time code the monitor writes
to its log at startup, so whoever reaches a fresh install first cannot claim it. Find it with

```bash
journalctl -u nightjar | grep setup_code | tail -1
```

(or in `LOG_FILE` when not running under systemd). After that, sign in with the account; a browser stays
signed in for 30 days of use. Change the password under **Settings → Account**, which signs out every
other browser. Forgot it? `./bin/nightjar -reset-admin` deletes the account; restart the monitor and
create it again with the new setup code.

To let someone look without changing anything, add a read-only **viewer** under **Settings → Viewers**.
Viewers see the dashboard but cannot make changes, and never see settings, backups or keys.

The React app is embedded in the binary, so `bin/nightjar` is all you deploy. Without make,
`./build.sh` does the same. Other targets (`make help` lists them all):

```bash
make dev                       # Go backend + Vite hot reload on :5173 (proxies /api to :8080)
make test                      # go vet + go test, tsc + vitest
make release                   # static linux/amd64 and linux/arm64 binaries in dist/
make docker                    # container image (see Dockerfile)
make install-service           # build, then install and enable the systemd unit (sudo)
make ui                        # put a new UI build live with no restart (see below)
./bin/nightjar -test      # self-test: collectors, services, webhooks
./bin/nightjar -version
```

To change the UI on a running install without restarting it, set
`FRONTEND_DIR=/path/to/serverMonitor/web/current` in `.env` and restart once. From then on `make ui`
(`./deploy-ui.sh`) builds the UI into `web/releases/` and switches `web/current` to it in one step;
open tabs pick it up on their next reload. Changes to the Go side still need `make build` and a restart.

Docker needs the host's process and filesystem view for host metrics:

```bash
docker run -d --name nightjar --pid=host --network=host \
  -v /:/host:ro -e HOST_PROC=/host/proc -e HOST_SYS=/host/sys -e HOST_ROOT=/host \
  -v $PWD/data:/data --env-file .env nightjar:dev
```

## Service monitoring

Add, edit, pause and delete checks on the dashboard's **services** page; changes apply immediately.
Each check has a type (HTTP, TCP, ping, TLS expiry, DNS, PostgreSQL, MySQL, Redis, Docker, plugin or heartbeat), a target, an interval, a timeout and a number of
retries; HTTP checks can also require status codes and a keyword, TLS checks warn ahead of expiry, and
ping checks set how many pings to send and how far apart (default 3, 0.2s apart; up when any is answered).
Checks and their results are stored in `HISTORY_DB` (SQLite); results are kept for `HISTORY_RETENTION_DAYS`.

An existing `services.json` (see `services.example.json`) is imported once, the first time the database has
no checks; after that the dashboard is the place to change them.

A service is marked down only after `retries` consecutive failures beyond the first. While it is failing
but not yet down, it is re-checked after its retry interval (default 20s, never longer than its interval)
rather than its full interval, so an outage is confirmed quickly. A Discord alert
goes out when it goes down (repeats use the normal alert cooldowns) and again when it recovers.

## Plex monitoring

Set `PLEX_MONITOR=true`, `PLEX_URL` and `PLEX_TOKEN`. The monitor polls Plex every `PLEX_CHECK_INTERVAL`
seconds and the dashboard gets a **plex** page:

- **Server state:** up or down, response time, version and whether a server update is available.
- **Now playing:** who is watching what, on which player, and how it is delivered (direct play, direct stream, or transcode, with hardware transcoding and throttling flagged). Also progress, quality, and bandwidth split by local and remote.
- **Charts:** concurrent streams (direct vs transcode) and total bandwidth.
- **Libraries:** item counts per library, including episode and track counts.
- **Recently finished:** streams that ended since the monitor started.

Discord alerts go to `PLEX_WEBHOOK` (or the main webhook):
- Plex goes down after more than `PLEX_RETRIES` failed polls, and when it recovers.
- `PLEX_TRANSCODE_ALERT` streams are transcoding at once.
- Optionally, every new stream (`PLEX_NOTIFY_STREAMS=true`).

`nightjar -test` checks the Plex connection and token.

## Updates

The **updates** page keeps the box's software current. On first start the monitor detects what it can
update; **detect apps** looks again later.

| Kind | Found automatically | Checked with | Updated with |
| --- | --- | --- | --- |
| OS packages | apt, dnf, yum or pacman | the package manager, after refreshing its lists | `apt-get upgrade` (keeps changed config files) |
| Snaps | `snap` with non-base snaps | `snap refresh --list` | `snap refresh` |
| apt package | Plex Media Server | `apt-cache policy` | `apt-get install --only-upgrade` |
| Sonarr-style app | Sonarr, Radarr, Prowlarr, Lidarr, Readarr, Whisparr (URL and API key read from their `config.xml`) | their `/update` API | their built-in updater, then waits for the new version |
| git checkout | this monitor, Tautulli | `git fetch`, commits behind upstream | `git pull --ff-only`, then an optional build command (`make build` for the monitor, which then restarts itself) |
| GitHub release | AdGuard Home, autobrr, Bazarr, Seerr, qBittorrent, Uptime Kuma | a version command against the latest release (or tag) | an update command, if it has one (`AdGuardHome --update`, `box upgrade autobrr`); the rest are check-only |

Checks run every `SYSTEM_UPDATE_INTERVAL_SECONDS` while `SYSTEM_UPDATE_ENABLED=true`, and **check now**
runs them at once. Each row has an **auto** switch: automatic updates start only between
`UPDATES_WINDOW_START` and `UPDATES_WINDOW_END` (local hours, default 04–06), after a fresh check. Each
version is tried once, so a failing update is not retried every cycle. After an update, the row's
systemd unit must be running (seen active twice), or a 🔴 alert says it is down. Successful updates send 🟢,
failures 🟠, and with `UPDATES_NOTIFY` updates waiting for you send one 🔵 summary per new version. Git
checkouts run as the directory's owner, so pulled files keep their owner, except the monitor's own.

Commands and paths run as root, and only the signed-in admin can change them. Detected targets bring their own commands, and
toggling auto-update or pausing works either way. `GITHUB_TOKEN` raises GitHub's limit of 60 release
checks an hour. `SYSTEM_UPDATE_APPLY=true` from older versions turns on auto-update for OS packages once,
when targets are first detected.

## Alerts

Every alert has a severity, shown the same way everywhere: 🔴 critical (down, crashed, out of space),
🟠 warning (a threshold crossed), 🟢 recovered, 🔵 info (reports, routine cleanup). Discord alerts are
Components V2 messages: a card coloured to match, with what happened as its heading, the details, the
host and when in small print and, with `DASHBOARD_URL`, an **Open Nightjar** button. A webhook that
refuses components gets classic embeds instead.

Alerts go to Discord (`WEBHOOK`, with optional per-category webhooks) and to any of these, all set on the
**settings** page under *Alert channels*:

| Channel | Settings | Notes |
| --- | --- | --- |
| ntfy | `NTFY_URL`, `NTFY_TOKEN` | Phone push; severity sets the ntfy priority |
| Telegram | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Info alerts arrive silently |
| Slack | `SLACK_WEBHOOK` | Incoming webhook, coloured attachment |
| Email | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_TO` | Port 465 uses TLS, others STARTTLS; plain text and HTML |
| JSON webhook | `ALERT_WEBHOOK_URL` | `{title, description, severity, fields, host, category, dashboard_url, time}` |

`ALERT_CHANNELS_MIN_SEVERITY` keeps routine alerts off noisy channels (e.g. `WARNING` for phone push);
a recovery always follows its outage. **Send test alert** on the settings page (and `nightjar -test`)
sends a test to every channel and reports which ones failed and why.

While the dashboard is open it also shows a toast when a check goes down or recovers.

### Notifications

Every alert is kept for 90 days, whether or not it went anywhere. The **bell** in the header counts unread
warnings and criticals and shows the latest few; **Notifications** lists them all, searchable and filtered
by severity and area, each with where it was sent (or why not) and a link to what it is about.

**Rules** (on the same page, saved without a restart) choose what each channel hears, per area: uptime
checks, system services, resources, disks and drives, the monitor itself, updates, qBittorrent and Plex. Each
cell is *everything*, *warnings and up*, *critical only* or *off*. Until changed, Discord hears everything
(qBittorrent only with its own webhook) and other channels hear `ALERT_CHANNELS_MIN_SEVERITY` and up.

**Quiet hours** hold alerts back overnight (critical ones can still go straight out) and send one summary
per channel when they end. **Mute** a check, system service or drive from its page, or a whole area from
the rules, for 1, 4 or 24 hours: its alerts are still kept but go nowhere.

## API

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /health` | public (details need signing in) | Health probe: 200 ok, 503 degraded, 500 error |
| `GET /api/config` | public | Whether the admin account exists and you are signed in; which pages are enabled |
| `GET /api/stats` | signed in | Host metrics for the dashboard |
| `GET /api/services` | signed in | Every service's status, uptime (24h/7d/30d) and last 60 checks |
| `GET /api/services/history?name=&hours=` | signed in | Stored results for one service (1–2160 hours) |
| `GET /api/plex` | signed in | Plex server state, active streams, libraries, stream history |
| `GET /api/status` | public, when `STATUS_PAGE_ENABLED=true` | Names, status and uptime only |
| `GET /api/checks` | signed in | Every check's definition |
| `POST /api/checks`, `PUT /api/checks/{name}`, `DELETE /api/checks/{name}` | signed in | Add, change (or rename) and delete checks |
| `GET /api/settings` | signed in | Every setting with its value and where it comes from (secrets only say whether they are set) |
| `POST /api/alerts/test` | signed in | Send a test alert to every channel; returns each channel's result |
| `GET /api/updates` | signed in | Every update target with installed and latest versions, pending items and recent runs |
| `POST /api/updates/check`, `POST /api/updates/{id}/check` | signed in | Queue a check of everything, or of one target |
| `POST /api/updates/{id}/update` | signed in | Queue an update of one target |
| `POST /api/updates`, `PUT /api/updates/{id}`, `DELETE /api/updates/{id}` | signed in | Add, change and remove targets |
| `POST /api/updates/detect` | signed in | Add newly detected targets |
| `PUT /api/settings` | signed in | `{"values": {"KEY": "value"}}`; `""` restores the default. Live settings apply on the next cycle; others restart the monitor (`restarting`, `restart_for`) |
| `POST /api/settings/test` | signed in | `{"target": "plex"\|"qbittorrent"\|"alerts", "values": {...}}`: try a connection with unsaved values |
| `GET /api/setup` | signed in | What is set up, and what was found on this machine to offer |
| `GET /api/notifications?q=&severity=&area=&unread=1&before=` | signed in | Alert history, newest first, 50 at a time, with unread counts |
| `GET /api/notifications/summary` | signed in | Unread counts, mutes and whether quiet hours are on (the bell polls this) |
| `POST /api/notifications/read` | signed in | `{"ids": [...]}` or `{"all": true}`, with `"unread": true` to mark them unread |
| `GET /api/notifications/rules`, `PUT /api/notifications/rules` | signed in | Routing rules (`{"rules": {area: {channel: level}}, "quiet": {...}}`) and quiet hours |
| `POST /api/notifications/mute` | signed in | `{"subject": "check:Plex", "label": "Plex", "minutes": 60}`; `0` unmutes |

The dashboard signs in with the admin account and keeps an HttpOnly, SameSite=Strict session cookie
(Secure over HTTPS, including behind a proxy that sends `X-Forwarded-Proto: https`). Scripts can skip
signing in with `WEB_AUTH_TOKEN`, sent as `Authorization: Bearer <token>` or `X-Auth-Token`. Ten wrong
passwords, setup codes or tokens a minute lock that client out for a minute; behind a reverse proxy on the
same machine, the client is taken from `X-Real-IP` or the last `X-Forwarded-For` hop. Have the proxy set
`X-Real-IP`, or overwrite or append to `X-Forwarded-For` (nginx: `proxy_set_header X-Real-IP $remote_addr;`
and `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`), so a client cannot choose its own
address for those limits. Changes must be JSON and, when the
browser sends an `Origin`, come from the dashboard's own origin, so another site cannot make them.

| Route | Auth | Purpose |
| --- | --- | --- |
| `POST /api/auth/setup` | setup code | `{"username", "password", "setup_code"}`: create the admin account (once) and sign in |
| `POST /api/auth/login`, `POST /api/auth/logout` | public | Sign in (`{"username", "password"}`) and out |
| `POST /api/auth/password` | signed in | `{"current", "new"}`: change the password and sign out other browsers |

## Configuration

The quickest start is the **setup guide** (`/setup`, linked from the overview until alerts and checks
exist). It finds what runs on the machine and offers it, one step at a time:

- **Alerts:** a Discord webhook, with a test alert before saving.
- **Plex:** a local server is found by its `/identity`. Its own sign-in token can be used straight from
  its `Preferences.xml`; the token is read on the server and never sent to the browser.
- **qBittorrent:** the Web UI port and user come from each user's `qBittorrent.conf`, checked against the
  Web API so another program on the port is not mistaken for it.
- **Uptime checks:** suggested for local apps that answer (Sonarr and friends via `/ping`, Plex,
  qBittorrent, Tautulli, Seerr, Bazarr, Uptime Kuma).
- **Updates:** checks switched on.

Connections are tested before they are saved.

Every setting can also be changed on the **settings** page, grouped and explained there. About 45
essential ones show by default; **advanced** shows the tuning knobs, as does searching. **Test connection**
(Plex, qBittorrent) and **Send test alert** use the values on screen, saved or not.

Most settings (thresholds, intervals, the update window, qBittorrent rules, the status page) apply from
the next monitoring cycle without a restart. Settings the monitor builds connections or timers from at
startup (webhooks, alert channels, Plex and qBittorrent connections, log files, the watchdog) restart it
in place: same process ID, so systemd keeps tracking it. The page says which kind a change is before you
save. Settings saved in the dashboard override the environment; **reset** returns one to the `.env` value
or the default. Webhooks, tokens and passwords are never sent back to the browser.

The environment (`.env`, or the file given with `-env`) supplies the starting values, and alone controls
the settings that decide how the dashboard itself is reached and stored: `WEB_HOST`, `WEB_PORT`,
`WEB_AUTH_TOKEN`, `HISTORY_DB` and the log files. `env.recommended` documents them all; copy it to
`.env` to start. Values in `.env` must not carry inline comments when systemd reads the file
(`EnvironmentFile=`), since systemd keeps them as part of the value.

## What it watches

- **Resources:** CPU, memory, GPU (nvidia-smi), disks and partitions, temperature (with a likely-cause
  analysis), network bandwidth and individual processes, each against its threshold.
- **Disk pressure:** removes old temp files, caches and rotated logs as usage climbs, more aggressively
  past the emergency threshold, and trims oversized log files.
- **Kernel:** panics, oopses, hardware errors, memory pressure, unexpected reboots, and a stability risk score.
- **Freezes:** a watchdog notices a stalled monitoring loop and can restart the service.
- **Optional:** media directory size, qBittorrent (stalled and finished torrents), Plex, service uptime
  checks, and updates for OS packages, snaps and self-hosted apps (see [Updates](#updates)).

Alerts for the same condition are rate-limited with escalating cooldowns, and flapping conditions are
suppressed. Discord calls are capped at 30 per minute behind a circuit breaker; anything that cannot
be delivered is written to `ALERT_LOG_PATH`.

## Moving from Server Monitor

Nightjar was called Server Monitor, and every name changed with it: the program (`bin/nightjar`), the agent (`nightjar-agent`), the systemd unit (`nightjar.service`) and the default files. A machine that ran the old one moves over by stopping it, renaming its files, and installing the new unit. Checks, history, settings, notifications and the admin account all carry over; browsers sign in again.

```bash
cd /path/to/the/checkout
make build
sudo systemctl disable --now server-monitor
sqlite3 server_monitor_history.db 'PRAGMA wal_checkpoint(TRUNCATE);'
mv server_monitor_history.db nightjar.db && rm -f server_monitor_history.db-wal server_monitor_history.db-shm
mv ~/.servermonitor_kernel_tracking.json ~/.nightjar_kernel_tracking.json 2>/dev/null
sudo mv /var/log/server-monitor-alerts.log /var/log/nightjar-alerts.log 2>/dev/null
sudo ./bin/nightjar -install-systemd
sudo rm /etc/systemd/system/server-monitor.service && sudo systemctl daemon-reload
sudo systemctl start nightjar
```

If `.env` sets `HISTORY_DB`, `LOG_FILE`, `ALERT_LOG_PATH`, `KERNEL_TRACKING_FILE` or `SYSTEMD_SERVICE_NAME` itself, those paths are kept as they are, and only the files it doesn't name need moving. The old log, `server_monitor.log`, can stay or go; new lines go to `nightjar.log`.

Agents installed before the rename keep running under their old names, but they pin a key the new dashboard no longer expects, so reinstall each with **Servers → Add a server** (and remove the old one with `sudo servermonitor-agent uninstall`). Apps from the app store keep running; the store lists them again once their manifests move: `sudo mv /etc/servermonitor /etc/nightjar`.

## Development

```bash
make dev                       # backend + Vite hot reload
make demo                      # UI only, on demo data: no Go or backend needed
make test                      # go vet, go test, tsc, vitest
make format                    # gofmt the Go code, Prettier the frontend
make lint                      # check formatting, go vet and tsc, changing nothing
cd backend && go test -race ./...
```

Frontend work does not need the backend: `npm run dev` (also `npm run demo`, `make demo`) serves the UI
on generated demo data (`frontend/src/api/demo.ts`). A banner says so, edits to checks and settings last
for the browser tab, and production builds leave the demo data out entirely. Sign in as `demo` with the
password `demo`; signing out and in again works as it does on a real monitor. To work against a real
backend instead, `npm run dev:live` proxies the API to it (`BACKEND_URL`, by default
http://127.0.0.1:8080) and falls back to demo data when none answers; `make dev` starts both.

### Where things are

| Path | What it holds |
| --- | --- |
| `backend/cmd/nightjar` | Startup: reads settings, wires every part together, restarts in place |
| `backend/internal/monitor` | The monitoring loop and its alert handlers, one file per area |
| `backend/internal/api` | The HTTP API, one file per area (`checks.go`, `settings.go`, `updates.go`, `network.go`…) |
| `backend/internal/config` | Every setting, declared once in `settings.go` |
| `backend/internal/collect`, `netload`, `smart`, `units` | Reading the machine: resources, live network, drive health, systemd |
| `backend/internal/services`, `plex`, `qbittorrent`, `updates` | Uptime checks and the integrations |
| `backend/internal/apps` | The app store: its catalog, install and remove scripts, and jobs |
| `backend/internal/analytics` | Every server's minutes, and the summaries, outages and disk forecasts made from them |
| `backend/internal/agent`, `agentbin`, `remote` | Watching other servers: the agent (sampling, serving, installing), its builds carried inside the monitor, and the dashboard's poller and alerts |
| `backend/internal/accounts` | The admin account, its setup code, password hashing (PBKDF2) and sessions |
| `backend/internal/discord`, `notify`, `alerts` | Alert delivery, channels and cooldowns; `notify/hub.go` keeps, routes, holds and mutes every alert |
| `backend/internal/storage` | The SQLite database |
| `frontend/src/pages` | One file per page; larger pages keep their parts in a folder beside them (`overview/`, `qbittorrent/`) |
| `frontend/src/components` | Pieces shared between pages (charts, dropdown, finder, toasts) |
| `frontend/src/api` | API client, types, shared polls, and the demo data |
| `frontend/src/styles` | One stylesheet per area; `styles.css` imports them in cascade order |

All tests live in `backend/test/` and `frontend/test/`. The Go tests only use each package's public API;
alert scenarios feed synthetic snapshots through `monitor.Loop.Evaluate`, and an end-to-end test runs real
monitoring cycles and reads them back through the API (`backend/test/api/e2e_test.go`). Fake Discord,
Plex, qBittorrent and service targets are built on `httptest`.

## App store

**Apps → App store** installs torrent and media apps from their official releases: qBittorrent,
Prowlarr, Jackett, Sonarr, Radarr, Lidarr, autobrr, Unpackerr and an ntfy push server. Each gets
`/opt/<app>` for the program, `/var/lib/<app>` for its settings, and its own `<app>.service`, running as
swizzin's main user when there is one (so it shares downloads with the rest), else `media`. The install
log streams live; an uptime check can be added for it, and ntfy can be made the monitor's alert channel
in one click.

Apps already installed by swizzin or a package show as installed and are never touched. Store apps can
be reinstalled (fetching the latest release, keeping settings) or removed, optionally with their data.
Installs run as transient systemd units, so they work even though the monitor's own service is locked
down. `GET /api/apps`, `POST /api/apps/{id}/install` (`{"port", "check"}`), `POST /api/apps/{id}/remove`
(`{"purge"}`) and `GET /api/apps/job?after=` drive it.

## Watching other servers

**Servers** shows other machines next to this one, each either through just an SSH login (whether
its SSH port answers, its checks, test and reboot) or through a small agent installed on it
(`nightjar-agent`, about 7 MB, no dependencies) that only reports and changes nothing on the machine:
CPU, memory, CPU temperature, fans, GPU, disks, SMART drive health and temperatures, network and uptime,
refreshed every 10 seconds with the last hour charted.

Something that only needs to answer (a router's page, a port, a ping) is an uptime check under
**Services**. A check can name the server it runs on, and that server's page lists it under
**Services on it**. Servers added as "ping only" before this became uptime checks, history included, the
first time this version started. Each server can also have an SSH login (a key Nightjar made, or your
own pasted in) to test, restart the agent or reboot it when the agent can't help.

Adding an agent:

1. In **Servers → Add a server**, enter the server's address and download the agent: both builds
   (x86-64, and ARM64 for a Raspberry Pi 4 or 5) are inside the monitor, so they always match its
   version. `make build` puts them there; `make agent` alone also leaves them in `bin/`.
2. Drag the downloaded file onto the server, into any folder (with WinSCP, FileZilla or Cyberduck, a
   shared folder, or a USB stick). The sheet makes a token and shows the install command to run in a
   terminal on the server, in that folder, ready to copy:

   ```bash
   chmod +x nightjar-agent-linux-amd64 && sudo ./nightjar-agent-linux-amd64 install -token <token>
   ```

   `install` copies it to `/usr/local/bin`, puts the token in `/etc/nightjar-agent.env` (readable by
   root only) and starts `nightjar-agent.service`, sandboxed so it can read the machine but write
   nothing. Drive readings need smartmontools on the server. `-listen :9200` picks another port, and
   `-tls=false` serves plain HTTP instead of HTTPS.
3. Press **Connect**: the dashboard checks that the agent answers with that token before saving it.

The dashboard does the asking; servers never connect to it, so it can stay on 127.0.0.1. Each agent's
port (9101) only needs to be reachable from this machine, over the LAN or a VPN such as Tailscale.

Agents serve HTTPS by default, with a key derived from their token: the dashboard, holding the same token,
accepts only that key, so a machine without the token cannot pose as the agent, and there is no
certificate to copy or renew. With `-tls=false` (and an `http://` address in the dashboard) the readings
and token cross the network unencrypted, which is only for a network you trust. Tokens are stored in the
database and never sent to the browser.

Alerts go through the notification rules under **Other servers**: offline after three missed polls (30
seconds) for both kinds, and for agents, CPU temperature over `TEMP_THRESHOLD` for two polls, memory over `MEMORY_THRESHOLD` for three, a
disk over `DISK_THRESHOLD`, and a drive whose SMART health turns to warning or failing; each clears with a
recovery message once it is back a little under the limit (5° or 5 points under; 2 for disks), so it does not flap. A server can be
muted from its page. `sudo nightjar-agent uninstall` removes the agent.

`GET /api/hosts`, `GET /api/hosts/{id}`, `POST /api/hosts`, `PUT /api/hosts/{id}`, `DELETE /api/hosts/{id}`
`POST /api/hosts/token` (a fresh token) and `GET /api/agent/download?arch=amd64|arm64` drive it. An agent is `{"name", "address", "token"}` (an empty
token keeps the saved one on `PUT`); a ping-only server is `{"kind": "check", "name", "check": {"type":
"http"|"tcp"|"ping", "target", "self_signed"}}`.

## Analytics

**Servers → Analytics** covers every server, this one included, over the last 24 hours, 7 days or 30 days:

- Headline tiles: uptime across the watched servers with its outages and downtime, the hottest and the
  busiest server, and the data moved.
- A table with each server's uptime, average and peak CPU and temperature (with sparklines), average
  memory, its fullest disk and when that disk fills at the rate it is filling ("full in ~9 days"), its
  average response time when it is checked without an agent, and the data it moved.
- Trends: one metric (CPU, temperature, memory, fullest disk or response time) for every server side by
  side, on the same scale.
- Outages, newest first: when each server stopped answering for most of a minute's checks, and for how
  long.

Each server's page also switches its charts between the live hour and the last 24 hours, 7 days or 30 days.

Readings are kept one row per server per minute (means, peaks, and the share of checks answered), in the
`samples` table, for `HISTORY_RETENTION_DAYS` like the uptime checks. A range reads back as about 300
points; a stretch with no readings shows as a gap. This server has no uptime figure: it records only while
it runs. `GET /api/analytics?range=1h|24h|7d|30d` returns every server, `GET /api/analytics/{id}` one
(`local` for this one).

## Sections

**Settings → Sections** turns parts of the dashboard off: the Servers, Network, Media and Apps tabs, the
notification bell, and each Overview card. Monitoring and alerts carry on; the dashboard only leaves
them out (`DASHBOARD_HIDDEN`, applied at once).

## Logging

Each log entry is one aligned line: level, component, message, then `key=value` details. Secrets
(tokens, passwords, webhook URLs) are masked before anything is written.

```
2026-11-02 04:13:03.175  WARN   web         Failed sign-in  ip=203.0.113.9
```

- **Under systemd** lines carry their syslog priority and no timestamp (journald adds one), so
  `journalctl -u nightjar -p warning` shows only problems and journalctl colours them.
- **In a terminal** lines are coloured with a short clock (`NO_COLOR` turns colour off).
- **`LOG_FILE`** gets full timestamps, rotated at `LOG_MAX_BYTES`; `LOG_FORMAT=json` writes JSON lines
  everywhere for log shippers.
- **`LOG_LEVEL`** applies at once from Settings. Every cycle's readings are logged at `DEBUG`, and once
  every 15 minutes at `INFO`.
- **Monitor log** (account menu, or Settings → Storage and logging) shows the last 3,000 entries live,
  filtered by level, component or text, and downloads them. `GET /api/logs?after=&level=&component=&q=`
  serves the same entries (signed in).

## Troubleshooting

- **No Discord alerts:** check `WEBHOOK`, then run `./bin/nightjar -test`, which sends a test alert
  to each configured webhook. Alerts that failed to send are in `ALERT_LOG_PATH`.
- **Dashboard unreachable:** the default `WEB_HOST=127.0.0.1` only listens locally. Reach it over an SSH
  tunnel, a VPN, or a reverse proxy with HTTPS (the docs' *Account and security* page has an nginx
  example). `WEB_HOST=0.0.0.0` listens everywhere; everyone must still sign in.
- **No temperatures:** the host must expose sensors (`/sys/class/hwmon`; install `lm-sensors` on Linux).
- **Kernel checks find nothing:** the monitor needs to read `journalctl`, `dmesg` or `/var/log/kern.log`;
  run it as root or add the user to the `adm` / `systemd-journal` groups.
- **Cleanup does nothing:** the monitor needs write access to the directories it cleans; check the log
  for permission errors.
- **qBittorrent or Plex unreachable:** `-test` reports the connection result; check the host, port and
  credentials or token.
