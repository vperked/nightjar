# qBittorrent

Connect qBittorrent in the [setup guide](/setup), which finds its Web UI port and user, or set `QBITTORRENT_MONITOR`, `QBITTORRENT_HOST`, `QBITTORRENT_PORT`, `QBITTORRENT_USERNAME` and `QBITTORRENT_PASSWORD` under **[Settings → qBittorrent](/settings)**.

## The qBittorrent page

**[Media → qBittorrent](/qbittorrent)** lists your torrents, grouped by state (downloading, seeding, stalled, paused and so on), with their speed, progress and ratio. For a torrent you can **start**, **stop**, **reannounce**, **recheck** or **delete** it.

**Add torrents** takes magnet links or `.torrent` URLs, one per line, with an optional category and the choice to add them stopped. Find (<kbd>Ctrl</kbd> <kbd>K</kbd>) has an _Add a torrent_ shortcut.

## Stalled torrents

A torrent that stops moving for `STALLED_TORRENT_THRESHOLD` (5 minutes) is stalled. With `STALLED_TORRENT_AUTO_HANDLE` on, the monitor reannounces and resumes it, up to `STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS` times, waiting `STALLED_TORRENT_ACTION_COOLDOWN` between tries. It alerts when one stalls and when it recovers.

## Replacing stuck downloads

When recovery does not get a stalled download moving, Nightjar asks Sonarr or Radarr to replace it: the one that queued it removes it from qBittorrent, blocklists that release so it is not grabbed again, and searches for another. Discord is told. Turn it off with `STALLED_TORRENT_REPLACE`. Nightjar finds Sonarr and Radarr on this machine by their `config.xml`, or uses `SONARR_URL` and `RADARR_URL` with their API keys when they are set.

## Seeding limits

- `SEEDING_RATIO_LIMIT` removes torrents that reach that ratio (0 seeds forever), with their files if `SEEDING_RATIO_DELETE_FILES` is on.
- `INACTIVE_TORRENT_AUTO_REMOVE` removes paused torrents, keeping their files.

qBittorrent's alerts go to `QBITTORRENT_WEBHOOK`, and only there unless you change the [notification rules](/docs/notifications).
