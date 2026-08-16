"""
qBittorrent client monitoring over the Web API.
"""

from core.qbittorrent.monitor import (
    QBittorrentMonitor,
    DOWNLOADING_STATES,
    SEEDING_ACTIVE_STATES,
    is_download_stalled,
    is_seeding_ratio_met,
)

__all__ = [
    'QBittorrentMonitor',
    'DOWNLOADING_STATES',
    'SEEDING_ACTIVE_STATES',
    'is_download_stalled',
    'is_seeding_ratio_met',
]
