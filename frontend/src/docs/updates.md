# Updates

**[Apps → Updates](/updates)** keeps the machine's software current. On first start the monitor finds what it can update; **detect apps** looks again later.

| Kind                                                | Found                                                          | Updated with                                                     |
| --------------------------------------------------- | -------------------------------------------------------------- | ---------------------------------------------------------------- |
| OS packages                                         | apt, dnf, yum or pacman                                        | the package manager (apt keeps changed config files)             |
| Snaps                                               | snaps other than base ones                                     | `snap refresh`                                                   |
| Plex Media Server                                   | its apt package                                                | `apt-get install --only-upgrade`                                 |
| Sonarr, Radarr, Prowlarr, Lidarr, Readarr, Whisparr | their `config.xml`                                             | their own updater                                                |
| git checkouts                                       | this monitor, Tautulli                                         | `git pull --ff-only`, then a build command                       |
| GitHub releases                                     | AdGuard Home, autobrr, Bazarr, Seerr, qBittorrent, Uptime Kuma | an update command, where there is one; the rest are checked only |

## Checking

Turn on `SYSTEM_UPDATE_ENABLED` (or **Updates** in the [setup guide](/setup)). Updates are then checked every 6 hours (`SYSTEM_UPDATE_INTERVAL_SECONDS`), and **check now** checks at once. With `UPDATES_NOTIFY` on, you get one alert per new version waiting for you.

`GITHUB_TOKEN` raises GitHub's limit of 60 release checks an hour, if you have many.

## Updating

Press **Update** on a row to update it now. Switch on **auto** to let it update itself: automatic updates only start between `UPDATES_WINDOW_START` and `UPDATES_WINDOW_END` (4 to 6 in the morning by default), after a fresh check. Each version is tried once, so a failing update isn't retried every cycle.

After an update, the app's service must be running again, or you get an alert that it is down. Successful updates send a 🟢 alert and failures a 🟠 one.

## Update all

**Update all** lists everything with an update, each ticked; untick what you want to leave. A **Major version** is marked: it can change how an app works or what it needs, so read its release notes first. Nightjar itself goes last, since updating it restarts the monitor.

Then a panel at the top follows the run, one update at a time: how many are done, the one running with its log as it goes, those waiting, and how each went, with its log. It keeps following after a reload, and **Dismiss** clears it once everything is through.

Sonarr, Radarr, Prowlarr and Lidarr install a major version only when asked to, as their own **Install** button does. Nightjar asks when you press **Update** or **Update all**; automatic updates leave major versions to you.

## Apps swizzin installed

- **qBittorrent**: swizzin builds it from source and only up to 5.1, so Nightjar updates it with the official static build instead: it downloads the newest, checks it runs, and swaps it in for `/usr/bin/qbittorrent-nox` before restarting it. swizzin's package record keeps its old version, and running `box upgrade qbittorrent` later puts swizzin's own build back.
- **Seerr** in a git checkout: an update checks out the new release's tag, installs its packages and builds it with the Node its service runs (a few minutes), then restarts it.
- **Bazarr** updates itself, with its own updater, unless you turned that off in its settings.

Targets found before Nightjar knew these get their update command when it next starts. A command you set up yourself is never replaced.

## Watching an update

While something updates, its row shows what the update is doing as it does it, without opening anything: each command it runs and what that prints, and the steps between, such as asking Sonarr to update itself, waiting while it restarts, and checking it's running again. A progress bar shows only its latest state. The page refreshes every two seconds while an update runs.

Once it's done, the update's **History** keeps the last 200 lines under **Log**, for a failure as much as a success, so you can see why it failed.

If an update or a check says Nightjar **lost the right to switch users (CAP_SETUID)**, restart Nightjar. apt and git need that right, and Nightjar watches its own permissions, logs the moment it loses one and alerts you.

## Adding your own

**Add** a target with a version command, an update command and, optionally, the systemd service to watch afterwards. Commands run as root, so only the signed-in admin can change them. The same goes for what decides what runs: the paths, where a release is downloaded from, the service restarted afterwards and an app's address. On a dashboard without sign-in, those can be changed only with `WEB_AUTH_TOKEN` set.

When another program is using the package manager (unattended-upgrades, say, or an `apt` someone left at a prompt), Nightjar waits up to 10 minutes for it to finish rather than stopping it, then tries once more. If it is still busy, the update fails and says which program has it.
