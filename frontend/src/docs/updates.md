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

## Adding your own

**Add** a target with a version command, an update command and, optionally, the systemd service to watch afterwards. Commands run as root, so only the signed-in admin can change them.
