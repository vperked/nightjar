import os
import platform
import re
import subprocess
import time
from pathlib import Path
from typing import Dict, List, Optional
from core.format import format_bytes
from core.security import safe_subprocess_path
from core.config import DEFAULT_ROTATED_LOG_RETENTION_DAYS, get_env_float
from core.observability import Component, LOG_FILE, get_logger

logger = get_logger(Component.LOGS)

SECONDS_PER_DAY = 86400.0

ROTATED_LOG_PATTERN = re.compile(
    r"""
    (?:\.\d+(?:\.(?:gz|bz2|xz|zst|zip))?)$    # syslog.1, kern.log.2.gz
    | (?:\.(?:gz|bz2|xz|zst))$                # already-compressed archive
    | (?:\.old)$                              # daemon.log.old
    | (?:-\d{8})$                             # syslog-20240115
    """,
    re.IGNORECASE | re.VERBOSE,
)


class LogMonitor:
    """Monitor log file sizes to ensure logs don't grow unbounded."""

    def __init__(self, log_file: Optional[str] = None,
                 threshold_mb: Optional[float] = None,
                 system_log_paths: Optional[List[str]] = None):
        """
        Initialize the log monitor.

        Args:
            log_file: Path to application log file (default: from LOG_FILE env or logger.py)
            threshold_mb: Alert threshold in MB (default: 100 MB)
            system_log_paths: List of system log paths to monitor (default: common Linux paths)
        """
        self.log_file = log_file or LOG_FILE
        self.threshold_bytes = (threshold_mb or 100.0) * 1024 * 1024
        self.is_linux = platform.system() == 'Linux'

        if system_log_paths is None:
            self.system_log_paths = [
                '/var/log/syslog',
                '/var/log/messages',
                '/var/log/kern.log',
                '/var/log/auth.log',
                '/var/log/daemon.log',
            ]
        else:
            self.system_log_paths = system_log_paths

        self.journal_log_path = '/var/log/journal' if self.is_linux else None
        self.rotated_log_retention_days = get_env_float(
            'ROTATED_LOG_RETENTION_DAYS',
            DEFAULT_ROTATED_LOG_RETENTION_DAYS,
            min_val=0.0,
            max_val=365.0,
        )

    @staticmethod
    def is_rotated_log(log_path: str) -> bool:
        """
        Check whether a path looks like a rotated log archive.

        Rotated logs (syslog.1, kern.log.2.gz, syslog-20240115) are no longer written
        to by any daemon, so they can be deleted outright rather than trimmed.

        Args:
            log_path: Path to inspect

        Returns:
            True if the filename matches a known rotation suffix
        """
        return bool(ROTATED_LOG_PATTERN.search(Path(log_path).name))

    def get_rotated_log_files(self, log_dir: str = '/var/log') -> List[Dict]:
        """
        Discover rotated log archives under a directory.

        Args:
            log_dir: Directory to scan (default: /var/log)

        Returns:
            List of log info dictionaries with 'rotated' and 'age_days' populated
        """
        if not self.is_linux or not os.path.exists(log_dir):
            return []

        rotated_logs = []
        now = time.time()

        try:
            for root, dirs, files in os.walk(log_dir):
                dirs[:] = [d for d in dirs if d not in ('journal', 'private')]

                for filename in files:
                    if not self.is_rotated_log(filename):
                        continue

                    filepath = os.path.join(root, filename)
                    log_info = self.get_log_size(filepath)
                    if not log_info:
                        continue

                    mtime = log_info.get('mtime')
                    age_days = ((now - mtime) / SECONDS_PER_DAY) if mtime else 0.0

                    log_info['rotated'] = True
                    log_info['age_days'] = age_days
                    rotated_logs.append(log_info)
        except (OSError, PermissionError) as e:
            logger.debug(f"Rotated log scan failed: {e}")

        return rotated_logs

    def should_delete_rotated_log(self, log_info: Dict, force: bool = False) -> bool:
        """
        Decide whether a rotated log archive should be deleted.

        Args:
            log_info: Log info dictionary, ideally including 'age_days'
            force: If True, delete regardless of size and age

        Returns:
            True when the archive should be removed
        """
        if force:
            return True

        if log_info.get('size_bytes', 0) > self.threshold_bytes:
            return True

        age_days = log_info.get('age_days')
        if age_days is None:
            try:
                age_days = (time.time() - Path(log_info.get('path', '')).stat().st_mtime) / SECONDS_PER_DAY
            except (OSError, PermissionError, TypeError):
                return False

        return age_days >= self.rotated_log_retention_days

    def delete_rotated_log(self, log_path: str) -> bool:
        """
        Delete a rotated log archive.

        Args:
            log_path: Path to the rotated log

        Returns:
            True if the file was removed
        """
        try:
            path = Path(log_path)
            size_bytes = path.stat().st_size
            path.unlink()
            logger.info(f"Removed rotated log {log_path} ({format_bytes(size_bytes)})")
            return True
        except FileNotFoundError:
            return True
        except (OSError, PermissionError) as e:
            logger.error(f"Could not remove rotated log {log_path}: {e}")
            return False

    def cleanup_rotated_logs(self, force: bool = False, log_dir: str = '/var/log') -> Dict:
        """
        Delete rotated log archives that are oversized, aged out, or force-selected.

        Args:
            force: If True, remove every rotated archive found
            log_dir: Directory to scan (default: /var/log)

        Returns:
            Dictionary with files_removed, total_size_cleared and errors
        """
        stats = {
            'files_removed': 0,
            'total_size_cleared': 0,
            'errors': []
        }

        for log_info in self.get_rotated_log_files(log_dir):
            log_path = log_info.get('path', '')
            if not log_path:
                continue

            if not self.should_delete_rotated_log(log_info, force=force):
                continue

            size_bytes = log_info.get('size_bytes', 0)
            if self.delete_rotated_log(log_path):
                stats['files_removed'] += 1
                stats['total_size_cleared'] += size_bytes
            else:
                stats['errors'].append(f"Could not remove rotated log {log_path}")

        if stats['files_removed'] > 0:
            logger.info(
                f"[CLEANUP] Removed {stats['files_removed']} rotated log archive(s), "
                f"freed {format_bytes(stats['total_size_cleared'])}"
            )

        return stats

    def get_log_size(self, log_path: str) -> Optional[Dict]:
        """
        Get size information for a specific log file.

        Args:
            log_path: Path to the log file

        Returns:
            Dictionary containing log file information, or None if file doesn't exist or can't be accessed
        """
        try:
            st = os.stat(log_path)
            return {
                'path': log_path,
                'size_bytes': st.st_size,
                'size_formatted': format_bytes(st.st_size),
                'mtime': st.st_mtime,
                'exists': True
            }
        except (OSError, PermissionError):
            return None

    def get_application_log_size(self) -> Optional[Dict]:
        """
        Get size information for the application log file.

        Returns:
            Dictionary containing application log file information
        """
        return self.get_log_size(self.log_file)

    def get_system_log_sizes(self) -> List[Dict]:
        """
        Get size information for system log files.

        Returns:
            List of dictionaries containing system log file information
        """
        log_sizes = []

        for log_path in self.system_log_paths:
            log_info = self.get_log_size(log_path)
            if log_info:
                log_sizes.append(log_info)

        return log_sizes

    def get_journal_log_size(self) -> Optional[Dict]:
        """
        Get total size of journald logs directory.

        Returns:
            Dictionary containing journal log directory size information
        """
        if not self.journal_log_path or not self.is_linux:
            return None

        try:
            journal_path = Path(self.journal_log_path)
            if not journal_path.exists():
                return None

            total_size = 0
            file_count = 0

            for file_path in journal_path.rglob('*'):
                if file_path.is_file():
                    try:
                        total_size += file_path.stat().st_size
                        file_count += 1
                    except (OSError, PermissionError):
                        continue

            return {
                'path': self.journal_log_path,
                'size_bytes': total_size,
                'size_formatted': format_bytes(total_size),
                'file_count': file_count,
                'exists': True
            }
        except (OSError, PermissionError):
            return None

    def check_log_threshold(self, log_info: Dict) -> bool:
        """
        Check if a log file exceeds the threshold.

        Args:
            log_info: Dictionary containing log file information

        Returns:
            True if log exceeds threshold, False otherwise
        """
        if not log_info:
            return False

        size_bytes = log_info.get('size_bytes', 0)
        return size_bytes > self.threshold_bytes

    def get_all_log_files_in_directory(self, log_dir: str = '/var/log') -> List[Dict]:
        """
        Get all log files in a directory (e.g., /var/log).
        Discovers all .log files and other common log file patterns.

        Args:
            log_dir: Directory to search for log files (default: /var/log)

        Returns:
            List of dictionaries containing log file information
        """
        if not self.is_linux or not os.path.exists(log_dir):
            return []

        log_files = []
        log_extensions = ['.log', '.log.1', '.log.2', '.log.3', '.log.4', '.log.5', '.log.gz', '.log.bz2']
        log_keywords = ['log', 'syslog', 'messages', 'kern', 'auth', 'daemon', 'mail', 'mail.log', 'apache', 'nginx', 'mysql', 'postgresql']

        try:
            for root, dirs, files in os.walk(log_dir):
                if 'journal' in root:
                    continue

                dirs_to_skip = ['journal', 'private', 'apt', 'dist-upgrade']
                dirs[:] = [d for d in dirs if d not in dirs_to_skip]

                for filename in files:
                    filepath = os.path.join(root, filename)
                    filename_lower = filename.lower()

                    is_log_file = (
                        any(filename_lower.endswith(ext) for ext in log_extensions) or
                        any(keyword in filename_lower for keyword in log_keywords) or
                        '.log' in filename_lower
                    )

                    if is_log_file and not any(filename_lower.endswith(ext) for ext in ['.lock', '.pid', '.tmp', '.swp']):
                        log_info = self.get_log_size(filepath)
                        if log_info:
                            log_files.append(log_info)
        except (OSError, PermissionError) as e:
            logger.debug(f"Log file scan failed: {e}")

        return log_files

    def get_all_log_sizes(self) -> Dict:
        """
        Get size information for all monitored logs.

        Returns:
            Dictionary containing all log size information
        """
        app_log = self.get_application_log_size()
        system_logs = self.get_system_log_sizes()
        journal_log = self.get_journal_log_size()
        rotated_logs = self.get_rotated_log_files()

        exceeded_logs = []

        if app_log and self.check_log_threshold(app_log):
            exceeded_logs.append(app_log)

        for log_info in system_logs:
            if self.check_log_threshold(log_info):
                exceeded_logs.append(log_info)

        if journal_log and self.check_log_threshold(journal_log):
            exceeded_logs.append(journal_log)

        for log_info in rotated_logs:
            if self.should_delete_rotated_log(log_info):
                exceeded_logs.append(log_info)

        return {
            'application_log': app_log,
            'system_logs': system_logs,
            'journal_log': journal_log,
            'rotated_logs': rotated_logs,
            'rotated_total_bytes': sum(log.get('size_bytes', 0) for log in rotated_logs),
            'exceeded_logs': exceeded_logs,
            'threshold_bytes': self.threshold_bytes,
            'threshold_formatted': format_bytes(self.threshold_bytes)
        }

    def set_threshold(self, threshold_mb: float):
        """
        Set the log size threshold.

        Args:
            threshold_mb: Threshold in MB
        """
        self.threshold_bytes = threshold_mb * 1024 * 1024

    def cleanup_log(self, log_info: Dict, force: bool = False) -> bool:
        """
        Clean up a log file that exceeds the threshold.
        For regular files, truncates to threshold size keeping the most recent data.
        For journal logs, uses journalctl to vacuum to threshold size.

        Args:
            log_info: Dictionary containing log file information
            force: If True, clean log even if it doesn't exceed threshold (for emergency cleanup)

        Returns:
            True if cleanup was successful, False otherwise
        """
        if not log_info:
            return False

        log_path = log_info.get('path', '')
        size_bytes = log_info.get('size_bytes', 0)

        if self.is_rotated_log(log_path):
            if not self.should_delete_rotated_log(log_info, force=force):
                return True
            return self.delete_rotated_log(log_path)

        if not force and size_bytes <= self.threshold_bytes:
            return True

        try:
            if log_path == self.journal_log_path or (self.journal_log_path and log_path.startswith(self.journal_log_path)):
                return self._cleanup_journal_log(force=force)
            else:
                path = Path(log_path)
                return self._cleanup_regular_log(path, force=force)
        except PermissionError as e:
            logger.error(f"Permission denied cleaning log {log_path}: {e}. Try running with sudo/root permissions.")
            return False
        except (OSError, subprocess.SubprocessError) as e:
            logger.error(f"Error cleaning up log {log_path}: {e}")
            return False
        except Exception as e:
            logger.warning(f"Unexpected error cleaning up log {log_path}: {e}")
            return False

    def _cleanup_regular_log(self, log_path: Path, force: bool = False) -> bool:
        """
        Shrink a regular log file, always keeping the most recent data unless force is set.

        Normal mode keeps the newest threshold_bytes of the file and discards older
        content. Emptying a log is only performed in force (emergency) mode.

        Args:
            log_path: Path to the log file
            force: If True, empty the log completely (emergency cleanup only)

        Returns:
            True if successful, False otherwise
        """
        try:
            if not log_path.exists():
                logger.warning(f"Log file does not exist: {log_path}")
                return False

            current_size = log_path.stat().st_size

            if current_size == 0:
                return True

            if force:
                return self._empty_log(log_path, current_size)

            if current_size <= self.threshold_bytes:
                return True

            return self._retain_log_tail(log_path, current_size)

        except PermissionError as e:
            logger.error(f"Permission denied cleaning log {log_path}: {e}. Try running with sudo/root permissions.")
            return False
        except Exception as e:
            logger.error(f"Unexpected error cleaning log {log_path}: {e}")
            return False

    def _empty_log(self, log_path: Path, current_size: int) -> bool:
        """
        Empty a log file completely. Only used for emergency cleanup.

        Args:
            log_path: Path to the log file
            current_size: Current size of the file in bytes

        Returns:
            True if successful, False otherwise
        """
        log_path_str = str(log_path)

        if self.is_linux:
            safe_path = safe_subprocess_path(log_path_str)
            if safe_path:
                try:
                    result = subprocess.run(
                        ['truncate', '-s', '0', safe_path],
                        capture_output=True,
                        text=True,
                        timeout=60,
                        check=False
                    )
                    if result.returncode == 0:
                        logger.info(f"Emergency: Cleared log {log_path} ({format_bytes(current_size)})")
                        return True
                    logger.debug(f"truncate failed: {result.stderr}")
                except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                    logger.debug(f"truncate unavailable, using file operations: {e}")
            else:
                logger.warning(f"Unsafe path rejected for truncate command: {log_path_str}")

        try:
            with open(log_path, 'r+b') as f:
                f.truncate(0)
            logger.info(f"Emergency: Cleared log {log_path} ({format_bytes(current_size)})")
            return True
        except (OSError, PermissionError) as e:
            logger.error(f"Could not clear log {log_path}: {e}")
            return False

    def _retain_log_tail(self, log_path: Path, current_size: int) -> bool:
        """
        Shrink a log in place to the newest threshold_bytes, preserving the inode.

        Writing in place (rather than replacing the file) keeps file descriptors held
        by running daemons such as rsyslog valid after the cleanup.

        Args:
            log_path: Path to the log file
            current_size: Current size of the file in bytes

        Returns:
            True if successful, False otherwise
        """
        keep_bytes = int(self.threshold_bytes)
        excess_bytes = current_size - keep_bytes
        chunk_size = 1024 * 1024

        try:
            with open(log_path, 'r+b') as f:
                read_pos = excess_bytes
                write_pos = 0

                while True:
                    f.seek(read_pos)
                    chunk = f.read(chunk_size)
                    if not chunk:
                        break
                    f.seek(write_pos)
                    f.write(chunk)
                    read_pos += len(chunk)
                    write_pos += len(chunk)

                f.truncate(write_pos)
                f.flush()

            new_size = log_path.stat().st_size
            logger.info(
                f"Log trimmed: {log_path} from {format_bytes(current_size)} to {format_bytes(new_size)} "
                f"(kept most recent {format_bytes(keep_bytes)})"
            )
            return True
        except (OSError, PermissionError) as e:
            logger.error(f"Could not trim log {log_path}: {e}")
            return False

    def _cleanup_journal_log(self, force: bool = False) -> bool:
        """
        Clean up journald logs using journalctl vacuum command.

        Args:
            force: If True, aggressively clean journal logs (vacuum to smaller size in emergency)

        Returns:
            True if successful, False otherwise
        """
        if not self.is_linux:
            return False

        try:
            if force:
                vacuum_size = '100M'
            else:
                threshold_mb = self.threshold_bytes / (1024 * 1024)
                vacuum_size = f'{threshold_mb:.0f}M'

            result = subprocess.run(
                ['journalctl', '--vacuum-size', vacuum_size],
                capture_output=True,
                text=True,
                timeout=300,
                check=False
            )
            if result.returncode == 0:
                if force:
                    logger.info(f"Emergency: Aggressively cleaned journal logs to {vacuum_size}")
                return True
            else:
                logger.debug(f"journalctl vacuum failed: {result.stderr}")
                return False
        except (subprocess.TimeoutExpired, FileNotFoundError, subprocess.SubprocessError) as e:
            logger.debug(f"journalctl vacuum failed: {e}")
            return False

