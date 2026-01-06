import os
import qbittorrentapi
from typing import Dict, List, Optional
from util.util import format_bytes, format_bytes_per_second
from logger import get_logger

logger = get_logger('QBittorrentMonitor')


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
        self.host = host or os.getenv('QBITTORRENT_HOST', 'localhost')
        self.port = port or int(os.getenv('QBITTORRENT_PORT', '8080'))
        self.username = username or os.getenv('QBITTORRENT_USERNAME', 'admin')
        self.password = password or os.getenv('QBITTORRENT_PASSWORD', '')
        
        self.client = None
        self._connected = False
        self._last_error = None
        
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
            except Exception as e:
                logger.debug(f"Failed to create qBittorrent client: {e}")
                self._last_error = str(e)
                return None
        
        return self.client
    
    def _ensure_connected(self) -> bool:
        """
        Ensure connection to qBittorrent Web UI is established.
        
        Returns:
            True if connected, False otherwise
        """
        if self._connected and self.client:
            try:
                self.client.app_version()
                return True
            except (qbittorrentapi.exceptions.APIConnectionError, 
                    qbittorrentapi.exceptions.LoginRequired):
                self._connected = False
                self.client = None
        
        client = self._get_client()
        if not client:
            return False
        
        try:
            client.auth_log_in()
            self._connected = True
            self._last_error = None
            return True
        except qbittorrentapi.exceptions.LoginFailed as e:
            logger.debug(f"qBittorrent login failed: {e}")
            self._last_error = f"Login failed: {e}"
            self._connected = False
            return False
        except qbittorrentapi.exceptions.APIConnectionError as e:
            logger.debug(f"qBittorrent connection error: {e}")
            self._last_error = f"Connection error: {e}"
            self._connected = False
            return False
        except Exception as e:
            logger.debug(f"qBittorrent error: {e}")
            self._last_error = str(e)
            self._connected = False
            return False
    
    def is_available(self) -> bool:
        """
        Check if qBittorrent Web UI is available and accessible.
        
        Returns:
            True if available, False otherwise
        """
        return self._ensure_connected()
    
    def get_all_torrents(self) -> List[Dict]:
        """
        Get all torrents from qBittorrent.
        
        Returns:
            List of dictionaries containing torrent information
        """
        if not self._ensure_connected():
            return []
        
        try:
            torrents = self.client.torrents_info()
            result = []
            
            for torrent in torrents:
                torrent_dict = {
                    'hash': torrent.hash,
                    'name': torrent.name,
                    'size': torrent.size,
                    'progress': torrent.progress,
                    'progress_percent': torrent.progress * 100,
                    'state': torrent.state,
                    'state_enum': torrent.state_enum,
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
                }
                result.append(torrent_dict)
            
            return result
        except Exception as e:
            logger.debug(f"Failed to get torrents: {e}")
            self._last_error = str(e)
            return []
    
    def get_downloading_torrents(self) -> List[Dict]:
        """
        Get all currently downloading torrents.
        
        Returns:
            List of dictionaries containing downloading torrent information
        """
        all_torrents = self.get_all_torrents()
        downloading = []
        
        for torrent in all_torrents:
            state = torrent.get('state', '').lower()
            if state in ['downloading', 'stalleddl', 'queueddl', 'checkingdl', 'pauseddl']:
                downloading.append(torrent)
        
        return downloading
    
    def get_seeding_torrents(self) -> List[Dict]:
        """
        Get all currently seeding torrents.
        
        Returns:
            List of dictionaries containing seeding torrent information
        """
        all_torrents = self.get_all_torrents()
        seeding = []
        
        for torrent in all_torrents:
            state = torrent.get('state', '').lower()
            if state in ['uploading', 'stalledup', 'queuedup', 'checkingup', 'pausedup']:
                seeding.append(torrent)
        
        return seeding
    
    def get_torrent_summary(self) -> Dict:
        """
        Get a summary of all torrents.
        
        Returns:
            Dictionary containing summary statistics
        """
        all_torrents = self.get_all_torrents()
        
        downloading = self.get_downloading_torrents()
        seeding = self.get_seeding_torrents()
        
        total_download_speed = sum(t.get('dlspeed', 0) for t in downloading)
        total_upload_speed = sum(t.get('upspeed', 0) for t in seeding)
        
        total_downloaded = sum(t.get('downloaded', 0) for t in all_torrents)
        total_uploaded = sum(t.get('uploaded', 0) for t in all_torrents)
        
        return {
            'total': len(all_torrents),
            'downloading': len(downloading),
            'seeding': len(seeding),
            'total_download_speed': total_download_speed,
            'total_upload_speed': total_upload_speed,
            'total_downloaded': total_downloaded,
            'total_uploaded': total_uploaded,
            'available': self._connected,
            'last_error': self._last_error,
        }
    
    def get_torrent_summary_human(self) -> Dict:
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
    
    def get_downloading_torrents_human(self) -> List[Dict]:
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
    
    def get_seeding_torrents_human(self) -> List[Dict]:
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

