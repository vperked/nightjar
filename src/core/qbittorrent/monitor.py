import os
import qbittorrentapi
from typing import Optional
from core.format import format_bytes, format_bytes_per_second
from core.circuitBreaker import CircuitBreaker, CircuitBreakerOpenError
from core.observability import Component, get_logger

logger = get_logger(Component.QBITTORRENT)

SEEDING_ACTIVE_STATES = frozenset({
    'uploading',
    'stalledup',
    'queuedup',
    'forcedup',
})
ACTIVE_DOWNLOAD_STATES = frozenset({
    'downloading',
    'stalleddl',
    'queueddl',
    'checkingdl',
    'forceddl',
    'metadl',
    'allocating',
})
DOWNLOADING_STATES = frozenset({
    *ACTIVE_DOWNLOAD_STATES,
    'pauseddl',
    'stoppeddl',
})
INACTIVE_TORRENT_STATES = frozenset({
    'pausedup',
    'stoppedup',
    'pauseddl',
    'stoppeddl',
})


def is_download_stalled(torrent: dict) -> bool:
    """
    Return True when a torrent is stuck downloading with no progress.

    Idle seeding (stalledup) is normal and is not treated as stalled here.
    """
    state = str(torrent.get('state', '')).lower()
    dlspeed = torrent.get('dlspeed', 0) or 0
    progress = torrent.get('progress', 0.0) or 0.0
    return state == 'stalleddl' and dlspeed == 0 and progress < 1.0


_NO_STALL_ACTION = {'recover': False, 'alert': False, 'abandon': False}


def stalled_action_plan(
    tracked: dict,
    now: float,
    threshold: float,
    cooldown: float,
    max_recovery_attempts: int,
) -> dict[str, bool]:
    """
    Decide recover/alert for a torrent that is currently stalled.

    Discord is notified once per stall episode. Recovery is attempted at most
    max_recovery_attempts times, then abandoned until the stall clears.
    """
    stalled_since = tracked.get('stalled_since')
    if stalled_since is None:
        return _NO_STALL_ACTION

    attempts = int(tracked.get('stalled_recovery_attempts') or 0)
    notified = bool(tracked.get('notified_stalled'))
    if notified and attempts >= max_recovery_attempts:
        return _NO_STALL_ACTION

    if (now - stalled_since) < threshold:
        return _NO_STALL_ACTION

    last_action = tracked.get('last_stalled_action')
    if last_action is not None and (now - float(last_action)) < cooldown:
        return _NO_STALL_ACTION

    recover = attempts < max_recovery_attempts
    return {
        'recover': recover,
        'alert': not notified,
        'abandon': not recover,
    }


def is_inactive_torrent(torrent: dict) -> bool:
    """
    Return True when a torrent is not actively downloading or seeding.

    Covers paused/stopped downloads and seeds. Error and missing-files states are
    deliberately excluded: they are usually transient (an unmounted drive or an
    unreachable NAS) and removing the torrent loses its progress and ratio.
    """
    state = str(torrent.get('state', '')).lower()
    return state in INACTIVE_TORRENT_STATES


def is_seeding_ratio_met(torrent: dict, min_ratio: float = 1.0) -> bool:
    """
    Return True when a completed torrent is actively seeding at or above min_ratio.

    Paused/stopped torrents are handled by inactive cleanup instead. Torrents
    still checking are excluded.
    """
    if min_ratio <= 0:
        return False
    state = str(torrent.get('state', '')).lower()
    if state not in SEEDING_ACTIVE_STATES:
        return False
    progress = torrent.get('progress', 0.0) or 0.0
    ratio = torrent.get('ratio', 0.0) or 0.0
    return progress >= 1.0 and ratio >= min_ratio


def should_remove_torrent(torrent: dict, min_ratio: float = 1.0) -> Optional[str]:
    """
    Return a removal reason when a torrent should be deleted, else None.

    Reasons:
      - inactive: not downloading or seeding
      - ratio: actively seeding at or above min_ratio
    """
    state = str(torrent.get('state', '')).lower()
    if state in INACTIVE_TORRENT_STATES:
        return 'inactive'
    if min_ratio <= 0 or state not in SEEDING_ACTIVE_STATES:
        return None
    progress = torrent.get('progress', 0.0) or 0.0
    ratio = torrent.get('ratio', 0.0) or 0.0
    if progress >= 1.0 and ratio >= min_ratio:
        return 'ratio'
    return None


class QBittorrentMonitor:
    """Monitor qBittorrent torrents using the Web API."""

    def __init__(self, host: Optional[str] = None, port: Optional[int] = None,
                 username: Optional[str] = None, password: Optional[str] = None):
        """
        Initialize the qBittorrent monitor.

        Args:
            host: qBittorrent Web UI host (default: from QBITTORRENT_HOST env or 'localhost')
            port: qBittorrent Web UI port (default: from QBITTORRENT_PORT env or 8080)
            username: qBittorrent Web UI username (default: from QBITTORRENT_USERNAME env or 'admin')
            password: qBittorrent Web UI password (default: from QBITTORRENT_PASSWORD env)
        """
        self.host = host or os.getenv('QBITTORRENT_HOST', 'localhost') or 'localhost'
        port_env = os.getenv('QBITTORRENT_PORT', '8080')
        try:
            self.port = port or (int(port_env) if port_env and port_env.strip() else 8080)
        except (ValueError, TypeError):
            self.port = 8080

        self.username = username or os.getenv('QBITTORRENT_USERNAME')
        self.password = password or os.getenv('QBITTORRENT_PASSWORD')

        if not self.username:
            raise ValueError(
                "qBittorrent username not provided. Set QBITTORRENT_USERNAME environment variable "
                "or pass username parameter."
            )

        if not self.password:
            raise ValueError(
                "qBittorrent password not provided. Set QBITTORRENT_PASSWORD environment variable "
                "or pass password parameter. Empty passwords are not allowed for security."
            )

        self.client = None
        self._connected = False
        self._last_error = None
        self._connection_logged = False
        self.last_fetch_ok = True
        self.circuit_breaker = CircuitBreaker(failure_threshold=5, timeout=60.0, success_threshold=2)

        logger.debug(f"qBittorrent monitor: {self.host}:{self.port} ({self.username})")
        if not self.password:
            logger.warning("qBittorrent password not configured. Set QBITTORRENT_PASSWORD environment variable.")

    def _get_client(self) -> Optional[qbittorrentapi.Client]:
        """
        Get or create qBittorrent client connection.

        Returns:
            qBittorrent client instance, or None if connection fails
        """
        if self.client is None:
            try:
                self.client = qbittorrentapi.Client(
                    host=self.host,
                    port=self.port,
                    username=self.username,
                    password=self.password
                )
            except (qbittorrentapi.exceptions.APIConnectionError,
                    qbittorrentapi.exceptions.LoginFailed,
                    ConnectionError, OSError) as e:
                error_msg = str(e)
                error_type = type(e).__name__
                logger.debug(f"qBittorrent client creation failed: {error_msg}")
                self._last_error = f"{error_type}: {error_msg}"
                return None
            except Exception as e:
                error_msg = str(e)
                error_type = type(e).__name__
                logger.warning(f"Unexpected error creating qBittorrent client ({error_type}): {error_msg}")
                self._last_error = f"{error_type}: {error_msg}"
                return None

        return self.client

    def _ensure_connected(self) -> bool:
        """
        Ensure connection to qBittorrent Web UI is established.
        Uses circuit breaker to prevent cascading failures.

        Returns:
            True if connected, False otherwise
        """
        if self.circuit_breaker.is_open():
            logger.debug("qBittorrent circuit breaker open, skipping connection")
            return False

        def _connect_internal() -> bool:
            """Internal connection logic wrapped by circuit breaker."""
            if self._connected and self.client:
                try:
                    self.client.app_version()
                    return True
                except (qbittorrentapi.exceptions.APIConnectionError,
                        qbittorrentapi.exceptions.LoginRequired):
                    logger.debug("qBittorrent session expired, reconnecting")
                    self._connected = False
                    self.client = None
                    self._connection_logged = False

            logger.debug(f"Connecting to qBittorrent at {self.host}:{self.port}")
            client = self._get_client()
            if not client:
                logger.warning(f"Failed to create qBittorrent client: {self._last_error}")
                return False

            client.auth_log_in()
            version = client.app_version()
            if not self._connection_logged:
                logger.info(f"qBittorrent connected successfully (version: {version})")
                self._connection_logged = True
            else:
                logger.debug(f"qBittorrent reconnected (version: {version})")
            self._connected = True
            self._last_error = None
            return True

        try:
            return self.circuit_breaker.call(_connect_internal)
        except CircuitBreakerOpenError:
            logger.debug("qBittorrent circuit breaker open, service unavailable")
            return False
        except (qbittorrentapi.exceptions.LoginFailed,
                qbittorrentapi.exceptions.APIConnectionError,
                ConnectionError, OSError) as e:
            error_msg = str(e)
            error_type = type(e).__name__
            logger.debug(f"qBittorrent connection failed: {error_msg}")
            self._last_error = f"{error_type}: {error_msg}"
            self._connected = False
            return False
        except Exception as e:
            error_msg = str(e)
            error_type = type(e).__name__
            logger.warning(f"qBittorrent unexpected error ({error_type}): {error_msg}")
            self._last_error = f"{error_type}: {error_msg}"
            self._connected = False
            return False

    def is_available(self) -> bool:
        """
        Check if qBittorrent Web UI is available and accessible.

        Returns:
            True if available, False otherwise
        """
        if not self.password:
            logger.debug("qBittorrent not available: password not configured")
            return False
        return self._ensure_connected()

    def get_all_torrents(self) -> list[dict]:
        """
        Get all torrents from qBittorrent.

        Returns:
            List of dictionaries containing torrent information
        """
        self.last_fetch_ok = False
        if not self._ensure_connected():
            return []

        try:
            def _get_torrents():
                return self.client.torrents_info()

            try:
                torrents = self.circuit_breaker.call(_get_torrents)
            except CircuitBreakerOpenError:
                logger.debug("qBittorrent circuit breaker open, returning empty list")
                return []
            except Exception as e:
                logger.debug(f"Torrent list failed: {e}")
                return []
            result = []

            for torrent in torrents:
                progress = torrent.progress
                result.append({
                    'hash': torrent.hash,
                    'name': torrent.name,
                    'size': torrent.size,
                    'progress': progress,
                    'progress_percent': progress * 100,
                    'state': str(torrent.state).lower(),
                    'dlspeed': torrent.dlspeed,
                    'upspeed': torrent.upspeed,
                    'ratio': torrent.ratio,
                    'eta': torrent.eta,
                    'seeds': torrent.num_seeds,
                    'peers': torrent.num_leechs,
                    'added_on': torrent.added_on,
                    'completion_on': torrent.completion_on,
                    'category': torrent.category,
                    'save_path': torrent.save_path,
                    'downloaded': torrent.downloaded,
                    'uploaded': torrent.uploaded,
                })

            self.last_fetch_ok = True
            return result
        except (qbittorrentapi.exceptions.APIConnectionError,
                qbittorrentapi.exceptions.LoginFailed,
                qbittorrentapi.exceptions.APIError) as e:
            logger.debug(f"Torrent list failed: {e}")
            self._last_error = str(e)
            return []
        except (AttributeError, TypeError) as e:
            logger.debug(f"Torrent data processing failed: {e}")
            self._last_error = str(e)
            return []
        except Exception as e:
            logger.warning(f"Unexpected error getting torrents: {e}")
            self._last_error = str(e)
            return []

    def _torrent_source(self, torrents: Optional[list[dict]] = None) -> list[dict]:
        """Return provided torrents or fetch the current list once."""
        if torrents is not None:
            return torrents
        return self.get_all_torrents()

    def get_downloading_torrents(self, torrents: Optional[list[dict]] = None) -> list[dict]:
        """
        Get all currently downloading torrents.

        Args:
            torrents: Optional pre-fetched torrent list from get_all_torrents()

        Returns:
            List of dictionaries containing downloading torrent information
        """
        return [
            torrent for torrent in self._torrent_source(torrents)
            if str(torrent.get('state', '')).lower() in DOWNLOADING_STATES
        ]

    def get_seeding_torrents(self, torrents: Optional[list[dict]] = None) -> list[dict]:
        """
        Get all currently seeding torrents.

        Args:
            torrents: Optional pre-fetched torrent list from get_all_torrents()

        Returns:
            List of dictionaries containing seeding torrent information
        """
        return [
            torrent for torrent in self._torrent_source(torrents)
            if str(torrent.get('state', '')).lower() in SEEDING_ACTIVE_STATES
        ]

    def get_torrent_summary(self) -> dict:
        """
        Get a summary of all torrents.

        Returns:
            Dictionary containing summary statistics
        """
        all_torrents = self.get_all_torrents()
        downloading = 0
        seeding = 0
        total_download_speed = 0
        total_upload_speed = 0
        total_downloaded = 0
        total_uploaded = 0

        for torrent in all_torrents:
            state = str(torrent.get('state', '')).lower()
            total_downloaded += torrent.get('downloaded', 0) or 0
            total_uploaded += torrent.get('uploaded', 0) or 0
            if state in DOWNLOADING_STATES:
                downloading += 1
                total_download_speed += torrent.get('dlspeed', 0) or 0
            elif state in SEEDING_ACTIVE_STATES:
                seeding += 1
                total_upload_speed += torrent.get('upspeed', 0) or 0

        return {
            'total': len(all_torrents),
            'downloading': downloading,
            'seeding': seeding,
            'total_download_speed': total_download_speed,
            'total_upload_speed': total_upload_speed,
            'total_downloaded': total_downloaded,
            'total_uploaded': total_uploaded,
            'available': self._connected,
            'last_error': self._last_error,
        }

    def get_torrent_summary_human(self) -> dict:
        """
        Get torrent summary in human-readable format.

        Returns:
            Dictionary with formatted values
        """
        summary = self.get_torrent_summary()

        return {
            'total': summary['total'],
            'downloading': summary['downloading'],
            'seeding': summary['seeding'],
            'total_download_speed': format_bytes_per_second(summary['total_download_speed']),
            'total_upload_speed': format_bytes_per_second(summary['total_upload_speed']),
            'total_downloaded': format_bytes(summary['total_downloaded']),
            'total_uploaded': format_bytes(summary['total_uploaded']),
            'available': summary['available'],
            'last_error': summary['last_error'],
        }

    def get_downloading_torrents_human(self) -> list[dict]:
        """
        Get downloading torrents with human-readable values.

        Returns:
            List of dictionaries with formatted values
        """
        downloading = self.get_downloading_torrents()
        result = []

        for torrent in downloading:
            result.append({
                'name': torrent['name'],
                'size': format_bytes(torrent['size']),
                'progress': f"{torrent['progress_percent']:.1f}%",
                'state': torrent['state'],
                'download_speed': format_bytes_per_second(torrent['dlspeed']),
                'eta': torrent['eta'] if torrent['eta'] >= 0 else 'N/A',
                'seeds': torrent['seeds'],
                'peers': torrent['peers'],
                'save_path': torrent['save_path'],
            })

        return result

    def get_seeding_torrents_human(self) -> list[dict]:
        """
        Get seeding torrents with human-readable values.

        Returns:
            List of dictionaries with formatted values
        """
        seeding = self.get_seeding_torrents()
        result = []

        for torrent in seeding:
            result.append({
                'name': torrent['name'],
                'size': format_bytes(torrent['size']),
                'ratio': f"{torrent['ratio']:.2f}",
                'state': torrent['state'],
                'upload_speed': format_bytes_per_second(torrent['upspeed']),
                'uploaded': format_bytes(torrent['uploaded']),
                'seeds': torrent['seeds'],
                'peers': torrent['peers'],
                'save_path': torrent['save_path'],
            })

        return result

    def get_stalled_torrents(self, torrents: Optional[list[dict]] = None) -> list[dict]:
        """
        Get torrents that are stalled while downloading.

        Idle seeding (stalledup) is excluded because it is expected when there
        are no connected peers downloading from us.

        Args:
            torrents: Optional pre-fetched torrent list from get_all_torrents()

        Returns:
            List of dictionaries containing stalled download torrent information
        """
        return [torrent for torrent in self._torrent_source(torrents) if is_download_stalled(torrent)]

    def _run_torrent_action(self, torrent_hash: str, action_name: str, api_call) -> bool:
        """
        Run a qBittorrent torrent API action behind the circuit breaker.

        Args:
            torrent_hash: Torrent infohash
            action_name: Short action label used in log messages
            api_call: Zero-argument callable that performs the API request

        Returns:
            True if the API call succeeded, False otherwise
        """
        if not torrent_hash or not self._ensure_connected():
            return False

        try:
            self.circuit_breaker.call(api_call)
            return True
        except CircuitBreakerOpenError:
            logger.debug(f"qBittorrent circuit breaker open, skipping {action_name}")
            return False
        except (qbittorrentapi.exceptions.APIConnectionError,
                qbittorrentapi.exceptions.LoginFailed,
                qbittorrentapi.exceptions.APIError) as e:
            logger.debug(f"qBittorrent {action_name} failed: {e}")
            return False
        except Exception as e:
            logger.warning(f"Unexpected error during {action_name} for torrent {torrent_hash[:8]}: {e}")
            return False

    def reannounce_torrent(self, torrent_hash: str) -> bool:
        """
        Force-reannounce a torrent to its trackers.

        Args:
            torrent_hash: Torrent infohash

        Returns:
            True if the API call succeeded, False otherwise
        """
        return self._run_torrent_action(
            torrent_hash,
            'reannounce',
            lambda: self.client.torrents_reannounce(torrent_hashes=torrent_hash),
        )

    def resume_torrent(self, torrent_hash: str) -> bool:
        """
        Resume a torrent.

        Args:
            torrent_hash: Torrent infohash

        Returns:
            True if the API call succeeded, False otherwise
        """
        return self._run_torrent_action(
            torrent_hash,
            'resume',
            lambda: self.client.torrents_resume(torrent_hashes=torrent_hash),
        )

    def pause_torrent(self, torrent_hash: str) -> bool:
        """
        Pause a torrent.

        Args:
            torrent_hash: Torrent infohash

        Returns:
            True if the API call succeeded, False otherwise
        """
        return self._run_torrent_action(
            torrent_hash,
            'pause',
            lambda: self.client.torrents_pause(torrent_hashes=torrent_hash),
        )

    def delete_torrent(self, torrent_hash: str, delete_files: bool = False) -> bool:
        """
        Remove a torrent from qBittorrent.

        Args:
            torrent_hash: Torrent infohash
            delete_files: When True, also delete downloaded content on disk

        Returns:
            True if the API call succeeded, False otherwise
        """
        return self._run_torrent_action(
            torrent_hash,
            'delete',
            lambda: self.client.torrents_delete(
                torrent_hashes=torrent_hash,
                delete_files=delete_files,
            ),
        )

    def recover_stalled_torrent(self, torrent_hash: str) -> dict:
        """
        Attempt to unstick a stalled download via reannounce and resume.

        Args:
            torrent_hash: Torrent infohash

        Returns:
            Dictionary with success flag and list of actions that succeeded
        """
        actions = []
        if self.reannounce_torrent(torrent_hash):
            actions.append('reannounce')
        if self.resume_torrent(torrent_hash):
            actions.append('resume')

        return {
            'success': len(actions) > 0,
            'actions': actions,
        }

    def handle_ratio_complete_torrent(
        self,
        torrent_hash: str,
        delete_files: bool = False,
    ) -> dict:
        """
        Remove a torrent under cleanup policy (inactive or ratio-complete).

        Args:
            torrent_hash: Torrent infohash
            delete_files: When True, also delete downloaded content on disk

        Returns:
            Dictionary with success flag and list of actions that succeeded
        """
        actions = []
        if self.delete_torrent(torrent_hash, delete_files=delete_files):
            actions.append('delete_files' if delete_files else 'delete')

        return {
            'success': len(actions) > 0,
            'actions': actions,
        }

    def get_inactive_torrents(self, torrents: Optional[list[dict]] = None) -> list[dict]:
        """
        Get torrents that are not actively downloading or seeding.

        Args:
            torrents: Optional pre-fetched torrent list from get_all_torrents()

        Returns:
            List of matching torrent dictionaries
        """
        return [
            torrent
            for torrent in self._torrent_source(torrents)
            if is_inactive_torrent(torrent)
        ]

    def get_ratio_complete_torrents(
        self,
        min_ratio: float = 1.0,
        torrents: Optional[list[dict]] = None,
    ) -> list[dict]:
        """
        Get completed seeding torrents at or above the ratio target.

        Args:
            min_ratio: Minimum share ratio required
            torrents: Optional pre-fetched torrent list from get_all_torrents()

        Returns:
            List of matching torrent dictionaries
        """
        return [
            torrent
            for torrent in self._torrent_source(torrents)
            if is_seeding_ratio_met(torrent, min_ratio=min_ratio)
        ]

