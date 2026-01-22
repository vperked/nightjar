import psutil
import time
import os
import platform
import shutil
import glob
import subprocess
from pathlib import Path
from typing import Dict, List, Optional, Callable
from util.util import format_bytes
from util.security import validate_path
from logger import get_logger

logger = get_logger()

PERMISSION_CHECK_TIMEOUT = 5


class DiskMonitor:
    """Monitor disk usage and I/O statistics."""

    def __init__(self):
        """Initialize the disk monitor."""
        pass

    def get_disk_usage(self, path: str = '/') -> Optional[Dict]:
        """
        Get disk usage for a specific path.

        Args:
            path: Path to check (default: root '/')

        Returns:
            Dictionary containing disk usage information
        """
        try:
            usage = psutil.disk_usage(path)
            return {
                'path': path,
                'total': usage.total,
                'used': usage.used,
                'free': usage.free,
                'percent': usage.percent,
            }
        except PermissionError:
            return None
        except OSError:
            return None

    def _is_nvme_device(self, device: str) -> bool:
        """
        Check if a device is an NVMe drive.

        Args:
            device: Device path (e.g., '/dev/nvme0n1p1', 'C:')

        Returns:
            True if device appears to be NVMe, False otherwise
        """
        device_lower = device.lower()
        if platform.system() == 'Linux':
            return 'nvme' in device_lower
        elif platform.system() == 'Windows':
            return False
        return False

    def _should_monitor_partition(self, partition: Dict) -> bool:
        """
        Check if a partition should be monitored.
        Filters out snap mounts, loop devices, and other special filesystems.

        Args:
            partition: Partition dictionary

        Returns:
            True if partition should be monitored, False otherwise
        """
        mountpoint = partition.get('mountpoint', '')
        device = partition.get('device', '')
        fstype = partition.get('fstype', '')
        opts = partition.get('opts', '')

        mountpoint_lower = mountpoint.lower()
        device_lower = device.lower()
        fstype_lower = fstype.lower()
        opts_lower = opts.lower()

        if '/snap/' in mountpoint_lower:
            return False

        if 'loop' in device_lower:
            return False

        if 'ro' in opts_lower and 'rw' not in opts_lower:
            return False

        excluded_fstypes = ['squashfs', 'tmpfs', 'devtmpfs', 'proc', 'sysfs', 'devpts',
                           'cgroup', 'cgroup2', 'pstore', 'bpf', 'tracefs', 'debugfs',
                           'securityfs', 'configfs', 'fusectl', 'hugetlbfs', 'mqueue',
                           'overlay', 'rpc_pipefs', 'systemd-1']
        if fstype_lower in excluded_fstypes:
            return False

        total = partition.get('total', 0)
        if total > 0 and total < 100 * 1024 * 1024:
            return False

        return True

    def get_all_partitions(self, filter_monitorable: bool = True) -> List[Dict]:
        """
        Get information about all disk partitions.

        Args:
            filter_monitorable: If True, filter out partitions that shouldn't be monitored

        Returns:
            List of dictionaries containing partition information
        """
        partitions = []
        for partition in psutil.disk_partitions():
            try:
                usage = psutil.disk_usage(partition.mountpoint)
                is_nvme = self._is_nvme_device(partition.device)
                partition_info = {
                    'device': partition.device,
                    'mountpoint': partition.mountpoint,
                    'fstype': partition.fstype,
                    'opts': partition.opts,
                    'total': usage.total,
                    'used': usage.used,
                    'free': usage.free,
                    'percent': usage.percent,
                    'is_nvme': is_nvme,
                }

                if filter_monitorable and not self._should_monitor_partition(partition_info):
                    continue

                partitions.append(partition_info)
            except (PermissionError, OSError):
                continue
        return partitions

    def get_nvme_partitions(self) -> List[Dict]:
        """
        Get all NVMe partitions.

        Returns:
            List of dictionaries containing NVMe partition information
        """
        all_partitions = self.get_all_partitions()
        return [p for p in all_partitions if p.get('is_nvme', False)]

    def get_partitions_by_usage(self, min_percent: float = 0.0, max_percent: float = 100.0) -> List[Dict]:
        """
        Get partitions filtered by usage percentage.

        Args:
            min_percent: Minimum usage percentage (inclusive)
            max_percent: Maximum usage percentage (inclusive)

        Returns:
            List of partitions within the specified usage range
        """
        all_partitions = self.get_all_partitions()
        return [p for p in all_partitions if min_percent <= p['percent'] <= max_percent]

    def get_disk_usage_human(self, path: str = '/') -> Optional[Dict[str, str]]:
        """
        Get disk usage in human-readable format.

        Args:
            path: Path to check (default: root '/')

        Returns:
            Dictionary with formatted disk usage values, or None if unavailable
        """
        usage = self.get_disk_usage(path)
        if usage is None:
            return None

        return {
            'path': path,
            'total': format_bytes(usage['total']),
            'used': format_bytes(usage['used']),
            'free': format_bytes(usage['free']),
            'percent': f"{usage['percent']:.1f}%",
        }

    def get_total_disk_usage(self) -> Dict:
        """
        Get total disk usage across all partitions.

        Returns:
            Dictionary containing total disk usage
        """
        partitions = self.get_all_partitions()

        total = {
            'total': 0,
            'used': 0,
            'free': 0,
            'partitions': len(partitions),
        }

        for partition in partitions:
            total['total'] += partition['total']
            total['used'] += partition['used']
            total['free'] += partition['free']

        if total['total'] > 0:
            total['percent'] = (total['used'] / total['total']) * 100
        else:
            total['percent'] = 0.0

        return total

    def get_disk_io_counters(self, perdisk: bool = False) -> Dict:
        """
        Get disk I/O statistics.

        Args:
            perdisk: If True, return per-disk statistics; if False, return system-wide totals

        Returns:
            Dictionary containing disk I/O statistics
        """
        if perdisk:
            io_counters = psutil.disk_io_counters(perdisk=True)
            if io_counters is None:
                return {}

            result = {}
            for disk_name, io in io_counters.items():
                result[disk_name] = {
                    'read_count': io.read_count,
                    'write_count': io.write_count,
                    'read_bytes': io.read_bytes,
                    'write_bytes': io.write_bytes,
                    'read_time': io.read_time,
                    'write_time': io.write_time,
                }
            return result
        else:
            io = psutil.disk_io_counters()
            if io is None:
                return {}

            return {
                'read_count': io.read_count,
                'write_count': io.write_count,
                'read_bytes': io.read_bytes,
                'write_bytes': io.write_bytes,
                'read_time': io.read_time,
                'write_time': io.write_time,
            }

    def get_disk_io_human(self, perdisk: bool = False) -> Dict:
        """
        Get disk I/O statistics in human-readable format.

        Args:
            perdisk: If True, return per-disk statistics; if False, return system-wide totals

        Returns:
            Dictionary with formatted I/O statistics
        """
        io_data = self.get_disk_io_counters(perdisk=perdisk)

        if perdisk:
            result = {}
            for disk_name, io in io_data.items():
                result[disk_name] = {
                    'read_count': io['read_count'],
                    'write_count': io['write_count'],
                    'read_bytes': format_bytes(io['read_bytes']),
                    'write_bytes': format_bytes(io['write_bytes']),
                    'read_time': f"{io['read_time']} ms",
                    'write_time': f"{io['write_time']} ms",
                }
            return result
        else:
            return {
                'read_count': io_data['read_count'],
                'write_count': io_data['write_count'],
                'read_bytes': format_bytes(io_data['read_bytes']),
                'write_bytes': format_bytes(io_data['write_bytes']),
                'read_time': f"{io_data['read_time']} ms",
                'write_time': f"{io_data['write_time']} ms",
            }

    def monitor_disk_usage(self, path: str = '/', interval: float = 1.0,
                          duration: Optional[float] = None,
                          callback: Optional[Callable[[Dict], None]] = None) -> List[Dict]:
        """
        Continuously monitor disk usage for a specific path.

        Args:
            path: Path to monitor
            interval: Time interval between measurements in seconds
            duration: Total duration to monitor in seconds (None for infinite)
            callback: Optional callback function to call with each measurement

        Returns:
            List of disk usage measurements
        """
        measurements = []
        start_time = time.time()

        while True:
            if duration and (time.time() - start_time) >= duration:
                break

            usage = self.get_disk_usage(path)
            measurement = {
                'timestamp': time.time(),
                'path': path,
                'usage': usage,
            }

            measurements.append(measurement)

            if callback:
                callback(measurement)

            time.sleep(interval)

        return measurements

    def get_disk_stats(self) -> Dict:
        """
        Get comprehensive disk statistics.

        Returns:
            Dictionary containing all disk information
        """
        return {
            'partitions': self.get_all_partitions(),
            'total_usage': self.get_total_disk_usage(),
            'io_counters': self.get_disk_io_counters(perdisk=False),
            'io_counters_per_disk': self.get_disk_io_counters(perdisk=True),
            'io_human': self.get_disk_io_human(perdisk=False),
        }

    def find_partitions_by_mountpoint(self, mountpoint: str) -> List[Dict]:
        """
        Find partitions by mountpoint.

        Args:
            mountpoint: Mountpoint to search for (partial match)

        Returns:
            List of matching partition dictionaries
        """
        all_partitions = self.get_all_partitions()
        matching = []

        for partition in all_partitions:
            if mountpoint.lower() in partition['mountpoint'].lower():
                matching.append(partition)

        return matching

    def get_partition_by_device(self, device: str) -> Optional[Dict]:
        """
        Get partition information by device name.

        Args:
            device: Device name (e.g., 'C:', '/dev/sda1')

        Returns:
            Partition dictionary if found, None otherwise
        """
        all_partitions = self.get_all_partitions()

        for partition in all_partitions:
            if partition['device'] == device:
                return partition

        return None

    def get_directory_size(self, path: str, allowed_base_paths: Optional[List[str]] = None,
                          timeout: Optional[float] = None, max_depth: Optional[int] = None) -> Optional[Dict]:
        """
        Get the total size of a directory and all its contents recursively.

        Args:
            path: Directory path to check. Must be a non-empty string.
            allowed_base_paths: Optional list of allowed base paths for security validation.
                              If None, defaults to safe predefined paths: ['/home', '/var', '/tmp', '/usr/local'].
            timeout: Optional timeout in seconds. If exceeded, returns partial results.
            max_depth: Optional maximum depth to traverse. None for unlimited.

        Returns:
            Dictionary containing directory size information, or None if path doesn't exist or is invalid.
            If timeout is exceeded, returns partial results with 'timed_out': True flag.
        """
        if not path or not isinstance(path, str):
            logger.warning(f"[SECURITY] Path validation failed: path must be a non-empty string")
            return None

        if allowed_base_paths is None:
            allowed_base_paths = ['/home', '/var', '/tmp', '/usr/local']
            logger.debug(f"[SECURITY] Using default safe base paths: {allowed_base_paths}")

        is_valid, resolved_path, error = validate_path(path, allowed_base_paths=allowed_base_paths, allow_symlinks=False)
        if not is_valid:
            logger.warning(f"[SECURITY] Path validation failed for '{path}': {error}")
            return None

        if not resolved_path:
            logger.warning(f"[SECURITY] Path validation returned no resolved path for '{path}'")
            return None

        path = resolved_path
        path_obj = Path(path)

        if not path_obj.exists():
            return None

        if not path_obj.is_dir():
            try:
                size = path_obj.stat().st_size
                return {
                    'path': path,
                    'size': size,
                    'is_file': True,
                    'is_directory': False,
                }
            except (OSError, PermissionError):
                return None

        total_size = 0
        file_count = 0
        dir_count = 0
        timed_out = False
        start_time = time.time() if timeout and timeout > 0 else None

        try:
            for dirpath, dirnames, filenames in os.walk(path, topdown=True):
                if timeout and (time.time() - start_time) > timeout:
                    logger.warning(f"Directory size calculation timed out after {timeout}s for {path}")
                    timed_out = True
                    break

                if max_depth is not None:
                    try:
                        rel_path = Path(dirpath).relative_to(path)
                        current_depth = len(rel_path.parts) - 1
                    except (ValueError, TypeError):
                        current_depth = 0
                    if current_depth >= max_depth:
                        dirnames.clear()

                dir_count += len(dirnames)

                for filename in filenames:
                    filepath = Path(dirpath) / filename
                    try:
                        total_size += filepath.stat().st_size
                        file_count += 1
                    except (OSError, PermissionError):
                        continue

                    if timeout and file_count % 100 == 0:
                        if (time.time() - start_time) > timeout:
                            logger.warning(f"Directory size calculation timed out after {timeout}s for {path}")
                            timed_out = True
                            break

                if timed_out:
                    break

        except (OSError, PermissionError) as e:
            logger.debug(f"Permission error calculating directory size for {path}: {e}")
            if file_count > 0 or dir_count > 0:
                pass
            else:
                return None

        result = {
            'path': path,
            'size': total_size,
            'is_file': False,
            'is_directory': True,
            'file_count': file_count,
            'dir_count': dir_count,
        }

        if timed_out:
            result['timed_out'] = True
            result['partial'] = True

        return result

    def get_directory_size_human(self, path: str) -> Optional[Dict]:
        """
        Get directory size in human-readable format.

        Args:
            path: Directory path to check

        Returns:
            Dictionary with formatted size values, or None if unavailable
        """
        size_info = self.get_directory_size(path)
        if size_info is None:
            return None

        return {
            'path': path,
            'size': format_bytes(size_info['size']),
            'size_bytes': size_info['size'],
            'is_file': size_info.get('is_file', False),
            'is_directory': size_info.get('is_directory', False),
            'file_count': size_info.get('file_count', 0),
            'dir_count': size_info.get('dir_count', 0),
        }

    def get_subdirectories_sizes(self, parent_path: str) -> List[Dict]:
        """
        Get sizes of all immediate subdirectories within a parent directory.

        Args:
            parent_path: Parent directory path

        Returns:
            List of dictionaries containing subdirectory size information
        """
        parent_path_obj = Path(parent_path)
        if not parent_path_obj.exists() or not parent_path_obj.is_dir():
            return []

        subdirs = []
        try:
            for item in os.listdir(parent_path):
                item_path = parent_path_obj / item
                if item_path.is_dir():
                    size_info = self.get_directory_size(str(item_path))
                    if size_info:
                        subdirs.append(size_info)
        except (OSError, PermissionError):
            return []

        subdirs.sort(key=lambda x: x['size'], reverse=True)
        return subdirs

    def get_subdirectories_sizes_human(self, parent_path: str) -> List[Dict]:
        """
        Get sizes of all immediate subdirectories in human-readable format.

        Args:
            parent_path: Parent directory path

        Returns:
            List of dictionaries with formatted subdirectory sizes
        """
        subdirs = self.get_subdirectories_sizes(parent_path)
        return [self.get_directory_size_human(subdir['path']) for subdir in subdirs if subdir]

    def get_largest_directories(self, path: str = '/', limit: int = 10) -> List[Dict]:
        """
        Get the largest directories on a given path (typically root of a partition).

        Args:
            path: Root path to analyze (default: '/')
            limit: Maximum number of directories to return (default: 10)

        Returns:
            List of dictionaries containing directory size information, sorted by size (largest first)
        """
        path_obj = Path(path)
        if not path_obj.exists() or not path_obj.is_dir():
            return []

        subdirs = self.get_subdirectories_sizes(path)
        subdirs.sort(key=lambda x: x['size'], reverse=True)
        return subdirs[:limit]

    def get_largest_directories_human(self, path: str = '/', limit: int = 10) -> List[Dict]:
        """
        Get the largest directories in human-readable format.

        Args:
            path: Root path to analyze (default: '/')
            limit: Maximum number of directories to return (default: 10)

        Returns:
            List of dictionaries with formatted directory sizes
        """
        largest = self.get_largest_directories(path, limit)
        result = []
        for dir_info in largest:
            human = self.get_directory_size_human(dir_info['path'])
            if human:
                result.append(human)
        return result

    def cleanup_temporary_files(self, mountpoint: str = '/', max_age_days: int = 7) -> Dict:
        """
        Clean up temporary files to free disk space.
        Cleans /tmp, /var/tmp, and other temporary directories.

        Args:
            mountpoint: Mount point to clean (default: '/')
            max_age_days: Maximum age in days for files to keep (default: 7)

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'files_removed': 0,
            'dirs_removed': 0,
            'total_size_cleared': 0,
            'errors': []
        }

        current_time = time.time()
        max_age_seconds = max_age_days * 24 * 60 * 60

        temp_dirs_env = os.getenv('TEMP_DIRS', None)
        if temp_dirs_env:
            temp_dirs_to_check = temp_dirs_env.split(':')
        elif platform.system() == 'Linux':
            temp_dirs_to_check = ['/tmp', '/var/tmp']
        else:
            temp_dirs_to_check = []

        temp_dirs = []
        for temp_dir in temp_dirs_to_check:
            temp_dir_obj = Path(temp_dir)
            if not temp_dir_obj.exists() or not temp_dir_obj.is_dir():
                continue
            try:
                is_valid, resolved_path, error = validate_path(temp_dir, allow_symlinks=False)
                if is_valid:
                    temp_dirs.append(resolved_path)
            except Exception:
                pass

        for temp_dir in temp_dirs:
            temp_dir_obj = Path(temp_dir)
            if not temp_dir_obj.exists() or not temp_dir_obj.is_dir():
                continue

            try:
                is_valid, resolved_path, error = validate_path(temp_dir, allow_symlinks=False)
                if not is_valid:
                    stats['errors'].append(f"Invalid path: {temp_dir} - {error}")
                    continue

                temp_dir = resolved_path

                for root, dirs, files in os.walk(temp_dir, topdown=False):
                    for filename in files:
                        filepath = Path(root) / filename
                        try:
                            file_stat = filepath.stat()
                            file_age = current_time - file_stat.st_mtime

                            if file_age > max_age_seconds:
                                file_size = file_stat.st_size
                                filepath.unlink()
                                stats['files_removed'] += 1
                                stats['total_size_cleared'] += file_size
                                logger.debug(f"Removed old temp file: {filepath} ({format_bytes(file_size)})")
                        except (OSError, PermissionError) as e:
                            continue
                        except Exception as e:
                            logger.error(f"Unexpected error removing file {filepath}: {e}", exc_info=True)
                            stats['errors'].append(f"Error removing {filepath}: {e}")

                    for dirname in dirs:
                        dirpath = Path(root) / dirname
                        try:
                            if str(dirpath) != temp_dir:
                                if not any(dirpath.iterdir()):
                                    dirpath.rmdir()
                                    stats['dirs_removed'] += 1
                                    logger.debug(f"Removed empty temp directory: {dirpath}")
                        except (OSError, PermissionError):
                            continue
                        except Exception as e:
                            logger.error(f"Unexpected error removing directory {dirpath}: {e}", exc_info=True)
                            stats['errors'].append(f"Error removing directory {dirpath}: {e}")

                temp_patterns = [
                    str(Path(temp_dir) / 'tmp*'),
                    str(Path(temp_dir) / '*.tmp'),
                    str(Path(temp_dir) / '*.log'),
                ]

                for pattern in temp_patterns:
                    try:
                        for filepath in glob.glob(pattern):
                            filepath_obj = Path(filepath)
                            try:
                                file_stat = filepath_obj.stat()
                                file_age = current_time - file_stat.st_mtime

                                if file_age > max_age_seconds:
                                    file_size = file_stat.st_size
                                    filepath_obj.unlink()
                                    stats['files_removed'] += 1
                                    stats['total_size_cleared'] += file_size
                            except (OSError, PermissionError):
                                continue
                            except Exception as e:
                                logger.error(f"Unexpected error removing file {filepath}: {e}", exc_info=True)
                                stats['errors'].append(f"Error removing {filepath}: {e}")
                    except Exception as e:
                        logger.error(f"Unexpected error processing pattern {pattern}: {e}", exc_info=True)
                        stats['errors'].append(f"Error with pattern {pattern}: {e}")

            except PermissionError as e:
                stats['errors'].append(f"Permission denied cleaning {temp_dir}: {e}")
            except Exception as e:
                logger.error(f"Unexpected error cleaning {temp_dir}: {e}", exc_info=True)
                stats['errors'].append(f"Error cleaning {temp_dir}: {e}")

        return stats

    def _perform_log_cleanup(self, log_monitor, aggressive: bool = False, mountpoint: str = '/') -> Dict:
        """
        Perform log cleanup with optional aggressive mode.

        Args:
            log_monitor: LogMonitor instance to clean logs
            aggressive: If True, clean all logs regardless of threshold
            mountpoint: Mount point to check disk usage

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'logs_cleaned': 0,
            'total_size_cleared': 0,
            'errors': []
        }

        if not log_monitor:
            return stats

        try:
            all_log_files = log_monitor.get_all_log_files_in_directory('/var/log')
            all_log_sizes = log_monitor.get_all_log_sizes()
            monitored_logs = []
            if all_log_sizes.get('application_log'):
                monitored_logs.append(all_log_sizes['application_log'])
            monitored_logs.extend(all_log_sizes.get('system_logs', []))
            if all_log_sizes.get('journal_log'):
                monitored_logs.append(all_log_sizes['journal_log'])

            all_logs_dict = {}
            for log_info in all_log_files + monitored_logs:
                log_path = log_info.get('path', '')
                if log_path:
                    if log_path not in all_logs_dict or log_info.get('size_bytes', 0) > all_logs_dict[log_path].get('size_bytes', 0):
                        all_logs_dict[log_path] = log_info

            if aggressive:
                logs_to_clean = list(all_logs_dict.values())
            else:
                logs_to_clean = [log for log in all_logs_dict.values() if log_monitor.check_log_threshold(log)]

            logs_to_clean.sort(key=lambda x: x.get('size_bytes', 0), reverse=True)

            for log_info in logs_to_clean:
                try:
                    log_path = log_info.get('path', '')
                    if not log_path:
                        continue

                    current_log_info = log_monitor.get_log_size(log_path)
                    if not current_log_info:
                        continue

                    original_size = current_log_info.get('size_bytes', 0)
                    if original_size == 0:
                        continue

                    if log_monitor.cleanup_log(current_log_info, force=aggressive):
                        stats['logs_cleaned'] += 1
                        size_after = log_monitor.get_log_size(log_path)
                        size_after_bytes = size_after.get('size_bytes', 0) if size_after else 0
                        stats['total_size_cleared'] += max(0, original_size - size_after_bytes)
                        logger.info(f"    Cleaned log: {log_path} ({format_bytes(original_size)} -> {format_bytes(size_after_bytes)})")
                except Exception as e:
                    logger.error(f"Unexpected error cleaning log {log_info.get('path', 'unknown')}: {e}", exc_info=True)
                    stats['errors'].append(f"Error cleaning log {log_info.get('path', 'unknown')}: {e}")

            if stats['logs_cleaned'] > 0:
                self.restart_log_services()
                self.setup_nginx_logs()

        except Exception as e:
            logger.error(f"Unexpected error getting log sizes: {e}", exc_info=True)
            stats['errors'].append(f"Error getting log sizes: {e}")

        return stats

    def cleanup_disk_space(self, mountpoint: str = '/', target_free_percent: float = 20.0, log_monitor=None) -> Dict:
        """
        Clean up disk space to reach target free percentage.
        Performs aggressive cleanup of temporary files, caches, and logs.

        Args:
            mountpoint: Mount point to clean (default: '/')
            target_free_percent: Target free space percentage (default: 20.0)
            log_monitor: Optional LogMonitor instance to clean logs

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'files_removed': 0,
            'dirs_removed': 0,
            'total_size_cleared': 0,
            'errors': [],
            'initial_usage': None,
            'final_usage': None,
            'logs_cleaned': 0
        }

        initial_usage = self.get_disk_usage(mountpoint)
        if not initial_usage:
            stats['errors'].append(f"Could not get disk usage for {mountpoint}")
            return stats

        stats['initial_usage'] = initial_usage['percent']
        current_free_percent = 100.0 - initial_usage['percent']

        if current_free_percent >= target_free_percent:
            logger.info(f"[CLEANUP] Disk already has {current_free_percent:.1f}% free (target: {target_free_percent:.1f}%)")
            stats['final_usage'] = initial_usage['percent']
            return stats

        logger.info(f"[CLEANUP] Starting disk cleanup: {initial_usage['percent']:.1f}% used, {current_free_percent:.1f}% free (target: {target_free_percent:.1f}% free)")

        if log_monitor:
            aggressive_cleanup = initial_usage['percent'] >= 40.0

            if aggressive_cleanup:
                logger.info("[CLEANUP] Step 1: Aggressive log cleanup (disk >= 40%, cleaning ALL logs in /var/log)...")
            else:
                logger.info("[CLEANUP] Step 1: Cleaning log files (all logs in /var/log exceeding threshold)...")

            log_cleanup_stats = self._perform_log_cleanup(log_monitor, aggressive=aggressive_cleanup, mountpoint=mountpoint)
            stats['logs_cleaned'] = log_cleanup_stats['logs_cleaned']
            stats['total_size_cleared'] += log_cleanup_stats['total_size_cleared']
            stats['errors'].extend(log_cleanup_stats['errors'])

            current_usage = self.get_disk_usage(mountpoint)
            if current_usage:
                current_free_percent = 100.0 - current_usage['percent']
                if current_free_percent >= target_free_percent:
                    logger.info(f"[CLEANUP] Target free space reached after log cleanup: {current_free_percent:.1f}% free")
                    final_usage = self.get_disk_usage(mountpoint)
                    if final_usage:
                        stats['final_usage'] = final_usage['percent']
                    return stats

        logger.info("[CLEANUP] Step 2: Cleaning temporary files...")
        max_attempts = 4
        for attempt, max_age_days in enumerate([7, 3, 1, 0]):
            if attempt >= max_attempts:
                logger.warning("[CLEANUP] Max cleanup attempts reached, stopping to prevent infinite loop")
                break

            cleanup_stats = self.cleanup_temporary_files(mountpoint, max_age_days=max_age_days)
            stats['files_removed'] += cleanup_stats['files_removed']
            stats['dirs_removed'] += cleanup_stats['dirs_removed']
            stats['total_size_cleared'] += cleanup_stats['total_size_cleared']
            stats['errors'].extend(cleanup_stats['errors'])

            current_usage = self.get_disk_usage(mountpoint)
            if current_usage:
                current_free_percent = 100.0 - current_usage['percent']
                if current_free_percent >= target_free_percent:
                    logger.info(f"[CLEANUP] Target free space reached: {current_free_percent:.1f}% free")
                    break

        if initial_usage['percent'] >= 50.0:
            logger.info("[CLEANUP] Step 3: Cleaning package caches and other space consumers...")
            cache_cleanup_stats = self._cleanup_package_caches()
            stats['files_removed'] += cache_cleanup_stats.get('files_removed', 0)
            stats['dirs_removed'] += cache_cleanup_stats.get('dirs_removed', 0)
            stats['total_size_cleared'] += cache_cleanup_stats.get('total_size_cleared', 0)
            stats['errors'].extend(cache_cleanup_stats.get('errors', []))

            current_usage = self.get_disk_usage(mountpoint)
            if current_usage:
                current_free_percent = 100.0 - current_usage['percent']
                if current_free_percent >= target_free_percent:
                    logger.info(f"[CLEANUP] Target free space reached after cache cleanup: {current_free_percent:.1f}% free")

        if stats['logs_cleaned'] > 0:
            self.restart_log_services()

        final_usage = self.get_disk_usage(mountpoint)
        if final_usage:
            stats['final_usage'] = final_usage['percent']
            final_free_percent = 100.0 - final_usage['percent']
            logger.info(f"[CLEANUP] Disk cleanup complete: {final_usage['percent']:.1f}% used, {final_free_percent:.1f}% free. "
                       f"Cleared {format_bytes(stats['total_size_cleared'])} "
                       f"({stats['files_removed']} files, {stats['dirs_removed']} dirs, {stats['logs_cleaned']} logs)")

        return stats

    def _cleanup_package_caches(self) -> Dict:
        """
        Clean package caches and other common space-consuming directories.

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'files_removed': 0,
            'dirs_removed': 0,
            'total_size_cleared': 0,
            'errors': []
        }

        if not platform.system() == 'Linux':
            return stats

        cache_dirs_env = os.getenv('CACHE_DIRS', None)
        if cache_dirs_env:
            cache_dirs_to_clean = cache_dirs_env.split(':')
        else:
            cache_dirs_to_clean = [
                '/var/cache/apt/archives',
                '/var/cache/apt/pkgcache.bin',
                '/var/cache/apt/srcpkgcache.bin',
            ]

        for cache_dir in cache_dirs_to_clean:
            cache_dir_obj = Path(cache_dir)
            if not cache_dir_obj.exists():
                continue

            try:
                is_valid, resolved_path, error = validate_path(cache_dir, allow_symlinks=False)
                if not is_valid:
                    continue

                cache_dir = resolved_path
                cache_dir_obj = Path(cache_dir)

                if 'apt/archives' in cache_dir:
                    try:
                        for filename in os.listdir(cache_dir):
                            filepath = cache_dir_obj / filename
                            if filepath.is_file() and filename.endswith('.deb'):
                                try:
                                    file_size = filepath.stat().st_size
                                    filepath.unlink()
                                    stats['files_removed'] += 1
                                    stats['total_size_cleared'] += file_size
                                    logger.debug(f"Removed package cache file: {filepath} ({format_bytes(file_size)})")
                                except (OSError, PermissionError):
                                    continue
                    except (OSError, PermissionError) as e:
                        stats['errors'].append(f"Error cleaning {cache_dir}: {e}")

                elif cache_dir.endswith('.bin'):
                    try:
                        if cache_dir_obj.is_file():
                            file_size = cache_dir_obj.stat().st_size
                            cache_dir_obj.unlink()
                            stats['files_removed'] += 1
                            stats['total_size_cleared'] += file_size
                            logger.debug(f"Removed cache binary: {cache_dir} ({format_bytes(file_size)})")
                    except (OSError, PermissionError) as e:
                        stats['errors'].append(f"Error removing {cache_dir}: {e}")
            except Exception as e:
                stats['errors'].append(f"Error processing {cache_dir}: {e}")

        if stats['total_size_cleared'] > 0:
            logger.info(f"[CLEANUP] Cleaned package caches: {format_bytes(stats['total_size_cleared'])} ({stats['files_removed']} files)")

        return stats

    def emergency_cleanup_disk_space(self, mountpoint: str = '/', log_monitor=None) -> Dict:
        """
        Emergency cleanup when disk is critically full (near 100%).
        Performs aggressive cleanup including logs and all temporary files.

        Args:
            mountpoint: Mount point to clean (default: '/')
            log_monitor: Optional LogMonitor instance to clean logs

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'files_removed': 0,
            'dirs_removed': 0,
            'total_size_cleared': 0,
            'errors': [],
            'initial_usage': None,
            'final_usage': None,
            'logs_cleaned': 0
        }

        initial_usage = self.get_disk_usage(mountpoint)
        if not initial_usage:
            stats['errors'].append(f"Could not get disk usage for {mountpoint}")
            return stats

        stats['initial_usage'] = initial_usage['percent']
        current_free_percent = 100.0 - initial_usage['percent']

        logger.warning(f"[EMERGENCY] Disk critically full: {initial_usage['percent']:.1f}% used, {current_free_percent:.1f}% free")

        logger.info("[EMERGENCY] Step 1: Cleaning all temporary files...")
        cleanup_stats = self.cleanup_temporary_files(mountpoint, max_age_days=0)
        stats['files_removed'] += cleanup_stats['files_removed']
        stats['dirs_removed'] += cleanup_stats['dirs_removed']
        stats['total_size_cleared'] += cleanup_stats['total_size_cleared']
        stats['errors'].extend(cleanup_stats['errors'])

        if log_monitor:
            logger.info("[EMERGENCY] Step 2: Cleaning log files (aggressive - ALL logs in /var/log)...")
            log_cleanup_stats = self._perform_log_cleanup(log_monitor, aggressive=True, mountpoint=mountpoint)
            stats['logs_cleaned'] = log_cleanup_stats['logs_cleaned']
            stats['total_size_cleared'] += log_cleanup_stats['total_size_cleared']
            stats['errors'].extend(log_cleanup_stats['errors'])

        logger.info("[EMERGENCY] Step 3: Cleaning additional cache directories...")
        emergency_cache_dirs_env = os.getenv('EMERGENCY_CACHE_DIRS', None)
        if emergency_cache_dirs_env:
            cache_dirs = emergency_cache_dirs_env.split(':')
        elif platform.system() == 'Linux':
            cache_dirs = ['/var/cache', '/tmp', '/var/tmp']
        else:
            cache_dirs = []
        for cache_dir in cache_dirs:
            cache_dir_obj = Path(cache_dir)
            if not cache_dir_obj.exists() or not cache_dir_obj.is_dir():
                continue

            try:
                is_valid, resolved_path, error = validate_path(cache_dir, allow_symlinks=False)
                if not is_valid:
                    continue

                current_time = time.time()
                max_age_seconds = 24 * 60 * 60
                resolved_path_obj = Path(resolved_path)

                for root, dirs, files in os.walk(resolved_path, topdown=False):
                    for filename in files:
                        filepath = Path(root) / filename
                        try:
                            file_stat = filepath.stat()
                            file_age = current_time - file_stat.st_mtime

                            if file_age > max_age_seconds:
                                file_size = file_stat.st_size
                                filepath.unlink()
                                stats['files_removed'] += 1
                                stats['total_size_cleared'] += file_size
                        except (OSError, PermissionError):
                            continue
                        except Exception as e:
                            logger.error(f"Unexpected error removing file {filepath}: {e}", exc_info=True)
                            stats['errors'].append(f"Error removing {filepath}: {e}")

                    for dirname in dirs:
                        dirpath = Path(root) / dirname
                        try:
                            if str(dirpath) != resolved_path and not any(dirpath.iterdir()):
                                dirpath.rmdir()
                                stats['dirs_removed'] += 1
                        except (OSError, PermissionError):
                            continue
            except Exception as e:
                logger.error(f"Unexpected error cleaning cache directory {cache_dir}: {e}", exc_info=True)
                stats['errors'].append(f"Error cleaning cache directory {cache_dir}: {e}")

        if stats['logs_cleaned'] > 0:
            self.restart_log_services()

        final_usage = self.get_disk_usage(mountpoint)
        if final_usage:
            stats['final_usage'] = final_usage['percent']
            final_free_percent = 100.0 - final_usage['percent']
            logger.warning(f"[EMERGENCY] Complete: {final_usage['percent']:.1f}% used, {final_free_percent:.1f}% free. "
                          f"Cleared {format_bytes(stats['total_size_cleared'])} "
                          f"({stats['files_removed']} files, {stats['dirs_removed']} dirs, {stats['logs_cleaned']} logs)")

        return stats

    def cleanup_logs_and_restart_services(self, log_monitor=None) -> Dict:
        """
        Public method to clean all logs in /var/log and restart services.
        Consolidates log cleanup logic from main loop.

        Args:
            log_monitor: Optional LogMonitor instance to clean logs

        Returns:
            Dictionary with cleanup statistics
        """
        stats = {
            'logs_cleaned': 0,
            'total_size_cleared': 0,
            'errors': []
        }

        if not log_monitor:
            stats['errors'].append("No log monitor provided")
            return stats

        try:
            all_log_files = log_monitor.get_all_log_files_in_directory('/var/log')

            all_log_sizes = log_monitor.get_all_log_sizes()
            monitored_logs = []
            if all_log_sizes.get('journal_log'):
                monitored_logs.append(all_log_sizes['journal_log'])

            all_logs_dict = {}
            for log_info in all_log_files + monitored_logs:
                log_path = log_info.get('path', '')
                if log_path:
                    if log_path not in all_logs_dict or log_info.get('size_bytes', 0) > all_logs_dict[log_path].get('size_bytes', 0):
                        all_logs_dict[log_path] = log_info

            logs_to_clean = sorted(all_logs_dict.values(), key=lambda x: x.get('size_bytes', 0), reverse=True)

            for log_info in logs_to_clean:
                try:
                    log_path = log_info.get('path', '')
                    if not log_path:
                        continue

                    current_log_info = log_monitor.get_log_size(log_path)
                    if not current_log_info:
                        continue

                    original_size = current_log_info.get('size_bytes', 0)
                    if original_size == 0:
                        continue

                    if log_monitor.cleanup_log(current_log_info, force=True):
                        stats['logs_cleaned'] += 1
                        size_after = log_monitor.get_log_size(log_path)
                        size_after_bytes = size_after.get('size_bytes', 0) if size_after else 0
                        stats['total_size_cleared'] += max(0, original_size - size_after_bytes)
                        logger.info(f"    Cleared log: {log_path} ({format_bytes(original_size)} -> {format_bytes(size_after_bytes)})")
                except (OSError, PermissionError) as e:
                    stats['errors'].append(f"Permission error cleaning log {log_info.get('path', 'unknown')}: {e}")
                except Exception as e:
                    stats['errors'].append(f"Error cleaning log {log_info.get('path', 'unknown')}: {e}")

            if stats['logs_cleaned'] > 0:
                self.restart_log_services()
                self.setup_nginx_logs()

        except (OSError, PermissionError) as e:
            stats['errors'].append(f"Permission error during log cleanup: {e}")
            logger.error(f"Permission error during log cleanup: {e}")
        except Exception as e:
            stats['errors'].append(f"Error during log cleanup: {e}")
            logger.error(f"Error during log cleanup: {e}")

        return stats

    def restart_log_services(self):
        """
        Restart log services (rsyslog and systemd-journald) after log cleanup.
        This ensures services properly manage log files after cleanup.
        """
        if not platform.system() == 'Linux':
            return

        services_to_restart = ['rsyslog', 'systemd-journald']

        for service in services_to_restart:
            try:
                logger.info(f"    Restarting {service}...")
                result = subprocess.run(
                    ['systemctl', 'restart', service],
                    capture_output=True,
                    text=True,
                    timeout=30,
                    check=False
                )
                if result.returncode == 0:
                    logger.info(f"    {service} restarted successfully")
                else:
                    logger.warning(f"    Failed to restart {service}: {result.stderr}")
            except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                logger.debug(f"    Could not restart {service}: {e}")
            except Exception as e:
                logger.debug(f"    Error restarting {service}: {e}")

    def setup_nginx_logs(self, max_retries: int = 3):
        """
        Setup nginx log directory and files with proper ownership and permissions.
        Creates nginx log directory if it doesn't exist, sets ownership,
        creates log files, and restarts nginx.

        Args:
            max_retries: Maximum number of retry attempts for nginx restart
        """
        if not platform.system() == 'Linux':
            return

        nginx_log_dir = os.getenv('NGINX_LOG_DIR', '/var/log/nginx')
        nginx_user_group = os.getenv('NGINX_USER_GROUP', 'www-data:adm')
        nginx_service_name = os.getenv('NGINX_SERVICE_NAME', 'nginx')

        import re
        if not re.match(r'^[a-z_][a-z0-9_-]*[$]?:[a-z_][a-z0-9_-]*[$]?$', nginx_user_group):
            logger.error(f"Invalid NGINX_USER_GROUP format: {nginx_user_group}. Expected format: 'user:group'")
            return

        try:
            nginx_log_dir_obj = Path(nginx_log_dir)
            if not nginx_log_dir_obj.exists():
                nginx_log_dir_obj.mkdir(mode=0o755, parents=True, exist_ok=True)
                logger.info(f"    Created nginx log directory: {nginx_log_dir}")

            try:
                result = subprocess.run(
                    ['id', nginx_user_group.split(':')[0]],
                    capture_output=True,
                    text=True,
                    timeout=5,
                    check=False
                )
                if result.returncode == 0:
                    result = subprocess.run(
                        ['chown', nginx_user_group, nginx_log_dir],
                        capture_output=True,
                        text=True,
                        timeout=10,
                        check=False
                    )
                    if result.returncode != 0:
                        logger.warning(f"    Could not set ownership on {nginx_log_dir}: {result.stderr}")
                else:
                    logger.warning(f"    User {nginx_user_group} does not exist, skipping ownership change")
            except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                logger.warning(f"    Error setting ownership on {nginx_log_dir}: {e}")

            for log_file in ['error.log', 'access.log']:
                log_path = Path(nginx_log_dir) / log_file
                if not log_path.exists():
                    try:
                        Path(log_path).touch()
                        subprocess.run(
                            ['chown', nginx_user_group, log_path],
                            capture_output=True,
                            text=True,
                            timeout=10,
                            check=False
                        )
                        logger.debug(f"    Created nginx log file: {log_path}")
                    except (OSError, PermissionError) as e:
                        logger.warning(f"    Permission error creating nginx log file {log_path}: {e}")
                    except Exception as e:
                        logger.warning(f"    Error creating nginx log file {log_path}: {e}")

            logger.info(f"    Nginx log directory setup: {nginx_log_dir}")

            for attempt in range(max_retries):
                try:
                    result = subprocess.run(
                        ['systemctl', 'restart', nginx_service_name],
                        capture_output=True,
                        text=True,
                        timeout=30,
                        check=False
                    )
                    if result.returncode == 0:
                        logger.info(f"    {nginx_service_name} restarted successfully")
                        break
                    else:
                        logger.warning(f"    Failed to restart {nginx_service_name}: {result.stderr}")
                        if attempt < max_retries - 1:
                            time.sleep(2 ** attempt)
                except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                    logger.warning(f"    Could not restart {nginx_service_name}: {e}")
                    if attempt < max_retries - 1:
                        time.sleep(2 ** attempt)
                except Exception as e:
                    logger.error(f"    Error restarting {nginx_service_name}: {e}")
        except (OSError, PermissionError) as e:
            logger.error(f"    Permission error setting up nginx logs: {e}")
        except Exception as e:
            logger.error(f"    Error setting up nginx logs: {e}")

