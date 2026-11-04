# App store

**[Apps → App store](/apps)** installs torrent and media apps from their official releases, each as its own systemd service:

- **Download clients**: qBittorrent.
- **Indexers**: Prowlarr, Jackett.
- **Automation**: Sonarr, Radarr, Lidarr, autobrr, Unpackerr.
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

## ntfy

After installing ntfy, its panel shows the address to add in the ntfy phone app and a private topic to subscribe to. **Use it for alerts** makes it the monitor's ntfy channel. It relays a wake-up through ntfy.sh, which iPhones need for instant delivery.
