"""
qBittorrent client monitoring over the Web API.
"""

from core.qbittorrent.monitor import (
    QBittorrentMonitor,
    ACTIVE_DOWNLOAD_STATES,
    DOWNLOADING_STATES,
    INACTIVE_TORRENT_STATES,
    SEEDING_ACTIVE_STATES,
    is_download_stalled,
    is_inactive_torrent,
    is_seeding_ratio_met,
    should_remove_torrent,
)

__all__ = [
    'QBittorrentMonitor',
    'ACTIVE_DOWNLOAD_STATES',
    'DOWNLOADING_STATES',
    'INACTIVE_TORRENT_STATES',
    'SEEDING_ACTIVE_STATES',
    'is_download_stalled',
    'is_inactive_torrent',
    'is_seeding_ratio_met',
    'should_remove_torrent',
]
