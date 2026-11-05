# App store

**[Apps → App store](/apps)** installs torrent, media and home-server apps from their official releases, each as its own systemd service:

- **Download clients**: qBittorrent.
- **Indexers**: Prowlarr, Jackett.
- **Automation**: Sonarr, Radarr, Lidarr, autobrr, Unpackerr.
- **Media**: Navidrome, your own music streaming server; Ombi, where the people you share Plex with request shows and films.
- **Files and sync**: Syncthing, to keep folders in sync with your computers and phone; File Browser, the server's files in any browser.
- **Network**: AdGuard Home, ad and tracker blocking for every device, as your network's DNS server.
- **Self-hosting**: Gitea, your own git hosting; Memos, quick private notes.
- **Notifications**: ntfy, your own push server for the monitor's alerts.

The strip along the top shows the usual torrent pipeline (Prowlarr, then Sonarr and Radarr, then qBittorrent, then Unpackerr) and which pieces you already have.

## Installing

Press **Install** on an app, choose its web port, and leave **add an uptime check for it** ticked to have it watched. The install log streams live in the side panel.

Each app gets:

- `/opt/<app>` for the program,
- `/var/lib/<app>` for its settings and database,
- its own `<app>.service`, running as your swizzin user when there is one (so it shares downloads with your other apps), else as `media`.

When it finishes, the panel says how to sign in the first time, with a button to open the app.

## Apps you already have

Apps installed by swizzin, a package or by hand show as installed, and the store never touches them. It also spots an app that is running under another name, such as in Docker.

## Reinstalling and removing

Open an app the store installed (**manage**) to **reinstall** it, which fetches the latest release and keeps its settings and port, or to **remove** it, optionally deleting its settings and data too.

## Home-server apps

A few need a step after installing, which each app's panel also says:

- **Navidrome** plays the music in `/var/lib/navidrome/music` until you point `MusicFolder` in `/var/lib/navidrome/navidrome.toml` at your library and restart it. Install ffmpeg if you want it to transcode for phones.
- **Syncthing** asks for a username and password under **Actions → Settings → GUI**; until then anyone on your network can open it. Nightjar keeps it up to date, so it doesn't replace itself.
- **File Browser** shows the home folder of the account the apps run as. It starts with the user `admin` and a generated password, printed at the end of the install log.
- **AdGuard Home** is set up in its wizard. It may serve DNS on port 53 (its service is given just that right). On Ubuntu, systemd-resolved already listens on 127.0.0.53, so have AdGuard listen on this server's network address, not on all interfaces, then point your router's DNS at this server.
- **Gitea** finishes on its install page; SQLite is enough for a home server. Nightjar installs git if it's missing.

## ntfy

After installing ntfy, its panel shows the address to add in the ntfy phone app and a private topic to subscribe to. **Use it for alerts** makes it the monitor's ntfy channel. It relays a wake-up through ntfy.sh, which iPhones need for instant delivery.
