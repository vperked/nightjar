import os
import platform
import subprocess
from pathlib import Path
from typing import Dict, List, Optional
from util.util import format_bytes
from util.security import safe_subprocess_path
from logger import LOG_FILE, LOG_MAX_BYTES, get_logger

logger = get_logger()


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
    
    def get_log_size(self, log_path: str) -> Optional[Dict]:
        """
        Get size information for a specific log file.
        
        Args:
            log_path: Path to the log file
            
        Returns:
            Dictionary containing log file information, or None if file doesn't exist or can't be accessed
        """
        try:
            path = Path(log_path)
            if not path.exists():
                return None
            
            size_bytes = path.stat().st_size
            
            return {
                'path': str(path),
                'size_bytes': size_bytes,
                'size_formatted': format_bytes(size_bytes),
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
            logger.debug(f"Error scanning {log_dir} for log files: {e}")
        
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
        
        exceeded_logs = []
        
        if app_log and self.check_log_threshold(app_log):
            exceeded_logs.append(app_log)
        
        for log_info in system_logs:
            if self.check_log_threshold(log_info):
                exceeded_logs.append(log_info)
        
        if journal_log and self.check_log_threshold(journal_log):
            exceeded_logs.append(journal_log)
        
        return {
            'application_log': app_log,
            'system_logs': system_logs,
            'journal_log': journal_log,
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
        Truncate a regular log file to threshold size, keeping the most recent data.
        Tries multiple methods: truncate command, then file operations.
        
        Args:
            log_path: Path to the log file
            force: If True, clean log even if below threshold (for emergency cleanup)
            
        Returns:
            True if successful, False otherwise
        """
        try:
            if not log_path.exists():
                logger.warning(f"Log file does not exist: {log_path}")
                return False
            
            current_size = log_path.stat().st_size
            
            if force and current_size <= self.threshold_bytes:
                if current_size > 0:
                    try:
                        if self.is_linux:
                            log_path_str = str(log_path)
                            is_system_log = any(log_path_str.startswith(sys_path) for sys_path in self.system_log_paths)
                            
                            if is_system_log:
                                safe_path = safe_subprocess_path(log_path_str)
                                if safe_path:
                                    result = subprocess.run(
                                        ['truncate', '-s', '0', safe_path],
                                        capture_output=True,
                                        text=True,
                                        timeout=60,
                                        check=False
                                    )
                                    if result.returncode == 0:
                                        logger.info(f"Emergency: Cleared small log {log_path} ({format_bytes(current_size)})")
                                        return True
                        
                        with open(log_path, 'wb') as f:
                            f.truncate(0)
                        logger.info(f"Emergency: Cleared small log {log_path} ({format_bytes(current_size)})")
                        return True
                    except (OSError, PermissionError, subprocess.SubprocessError) as e:
                        logger.debug(f"Could not clear small log {log_path}: {e}")
                        return False
                return True
            
            if not force and current_size <= self.threshold_bytes:
                return True
            
            if force:
                if self.is_linux:
                    log_path_str = str(log_path)
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
                                new_size = log_path.stat().st_size
                                logger.info(f"Aggressive cleanup: Cleared log {log_path} from {format_bytes(current_size)} to {format_bytes(new_size)}")
                                return True
                        except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                            logger.debug(f"truncate command failed: {e}, using file operations")
                
                try:
                    with open(log_path, 'wb') as f:
                        f.truncate(0)
                    new_size = log_path.stat().st_size
                    logger.info(f"Aggressive cleanup: Cleared log {log_path} from {format_bytes(current_size)} to {format_bytes(new_size)}")
                    return True
                except (OSError, IOError) as e:
                    logger.error(f"File operation failed for {log_path}: {e}")
                    return False
            
            excess_bytes = current_size - self.threshold_bytes
            
            if self.is_linux:
                log_path_str = str(log_path)
                is_system_log = any(log_path_str.startswith(sys_path) for sys_path in self.system_log_paths)
                
                if is_system_log:
                    safe_path = safe_subprocess_path(log_path_str)
                    if not safe_path:
                        logger.warning(f"Unsafe path rejected for truncate command: {log_path_str}")
                    else:
                        try:
                            result = subprocess.run(
                                ['truncate', '-s', '0', safe_path],
                                capture_output=True,
                                text=True,
                                timeout=60,
                                check=False
                            )
                            if result.returncode == 0:
                                new_size = log_path.stat().st_size
                                logger.info(f"System log cleared: {log_path} from {format_bytes(current_size)} to {format_bytes(new_size)}")
                                return True
                            else:
                                logger.debug(f"truncate command failed, trying file operations: {result.stderr}")
                        except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                            logger.debug(f"truncate command not available or failed: {e}, using file operations")
            
            try:
                chunk_size = 1024 * 1024
                temp_file_path = str(log_path) + '.tmp'
                
                try:
                    with open(log_path, 'rb') as f_in:
                        f_in.seek(excess_bytes)
                        with open(temp_file_path, 'wb') as f_out:
                            while True:
                                chunk = f_in.read(chunk_size)
                                if not chunk:
                                    break
                                f_out.write(chunk)
                    
                    import shutil
                    shutil.move(temp_file_path, log_path)
                except (OSError, IOError) as e:
                    try:
                        import os
                        if os.path.exists(temp_file_path):
                            os.remove(temp_file_path)
                    except Exception:
                        pass
                    raise
                
                new_size = log_path.stat().st_size
                logger.info(f"Log truncated using file operations: {log_path} from {format_bytes(current_size)} to {format_bytes(new_size)}")
                return True
            except (OSError, IOError) as e:
                logger.error(f"File operation failed for {log_path}: {e}")
                return False
                
        except PermissionError as e:
            logger.error(f"Permission denied cleaning log {log_path}: {e}. Try running with sudo/root permissions.")
            return False
        except Exception as e:
            logger.error(f"Unexpected error cleaning log {log_path}: {e}")
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
            logger.debug(f"journalctl vacuum error: {e}")
            return False

