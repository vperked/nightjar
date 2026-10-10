import psutil
import time
import os
import platform
import stat
import subprocess
from pathlib import Path
from typing import Optional
from core.format import format_bytes
from core.security import validate_path
from core.config import (
    DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD,
    get_env_bool,
    get_env_float,
)
from core.observability import Component, get_logger, safe_log

logger = get_logger(Component.DISK)

PERMISSION_CHECK_TIMEOUT = 5

ALLOWED_TEMP_CLEANUP_BASES = ['/tmp', '/var/tmp']
ALLOWED_CACHE_CLEANUP_BASES = ['/var/cache']

# Emergency mode targets regenerable caches only, not all of /var/cache.
EMERGENCY_CACHE_DIRS_DEFAULT = [
    '/var/cache/apt/archives',
    '/var/cache/pacman/pkg',
    '/var/cache/dnf',
    '/tmp',
    '/var/tmp',
]

EXCLUDED_FSTYPES = frozenset([
    'squashfs', 'tmpfs', 'devtmpfs', 'proc', 'sysfs', 'devpts',
    'cgroup', 'cgroup2', 'pstore', 'bpf', 'tracefs', 'debugfs',
    'securityfs', 'configfs', 'fusectl', 'hugetlbfs', 'mqueue',
    'overlay', 'rpc_pipefs', 'systemd-1',
])


def _resolve_cleanup_dirs(candidates: list[str], allowed_bases: list[str]) -> list[str]:
    """
    Resolve candidate cleanup directories, rejecting anything outside the allowlist.

    Operators can opt out of the allowlist with ALLOW_UNSAFE_CLEANUP_PATHS=true, which
    is logged loudly because it permits deletion anywhere the process can write.

    Args:
        candidates: Directory paths requested for cleanup
        allowed_bases: Base directories cleanup is permitted under

    Returns:
        List of resolved directory paths that are safe to clean
    """
    allow_unsafe = get_env_bool('ALLOW_UNSAFE_CLEANUP_PATHS', False)
    resolved_dirs = []

    for raw_candidate in candidates:
        candidate = raw_candidate.strip()
        if not candidate:
            continue

        is_valid, resolved_path, error = validate_path(
            candidate,
            allowed_base_paths=None if allow_unsafe else allowed_bases,
            allow_symlinks=False,
        )

        if not is_valid or not resolved_path:
            logger.warning(f"[CLEANUP] Refusing to clean {candidate}: {error}")
            continue

        if allow_unsafe:
            logger.warning(
                f"[CLEANUP] ALLOW_UNSAFE_CLEANUP_PATHS is enabled - cleaning unrestricted path {resolved_path}"
            )

        resolved_dirs.append(resolved_path)

    return resolved_dirs


# Names under the cleanup roots that hold live state (sockets, per-service private dirs,
# package-manager databases). They are never descended into or removed.
CLEANUP_PROTECTED_NAMES = frozenset({
    '.X11-unix', '.ICE-unix', '.font-unix', '.XIM-unix', '.Test-unix',
    'debconf', 'ldconfig', 'private', 'snap-private-tmp',
})
CLEANUP_PROTECTED_PREFIXES = ('systemd-private-', 'tmux-', 'ssh-', 'snap.')


def _is_cleanup_protected(name: str) -> bool:
    """Return True for entries that must never be cleaned."""
    return name in CLEANUP_PROTECTED_NAMES or name.startswith(CLEANUP_PROTECTED_PREFIXES)


def _clean_directory_tree(root: str, max_age_seconds: float, stats: dict,
                          now: Optional[float] = None) -> None:
    """
    Remove stale regular files and symlinks under root without following links.

    Every operation is relative to an open directory descriptor opened with
    O_NOFOLLOW, so a directory swapped for a symlink mid-walk cannot redirect
    deletion elsewhere. Sockets, FIFOs and device nodes are left alone, other
    filesystems are not crossed, and a directory is only removed once it is both
    empty and older than the age limit (so a just-created work directory survives).

    Args:
        root: Directory to clean (never removed itself)
        max_age_seconds: Minimum mtime age before removal
        stats: Cleanup statistics dict updated in place
        now: Current time, for tests
    """
    now = time.time() if now is None else now

    try:
        root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError as e:
        stats['errors'].append(f"Could not open {root}: {e}")
        return

    def walk(dir_fd: int, dir_path: str, dev: int) -> None:
        try:
            entries = [entry.name for entry in os.scandir(dir_fd)]
        except OSError as e:
            stats['errors'].append(f"Could not list {dir_path}: {e}")
            return

        for name in entries:
            if _is_cleanup_protected(name):
                continue
            full_path = os.path.join(dir_path, name)
            try:
                st = os.stat(name, dir_fd=dir_fd, follow_symlinks=False)
                age = now - st.st_mtime

                if stat.S_ISDIR(st.st_mode):
                    if st.st_dev != dev:
                        continue
                    child_fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=dir_fd)
                    try:
                        walk(child_fd, full_path, dev)
                    finally:
                        os.close(child_fd)
                    if age > max_age_seconds:
                        try:
                            os.rmdir(name, dir_fd=dir_fd)
                            stats['dirs_removed'] += 1
                        except OSError:
                            pass
                elif stat.S_ISREG(st.st_mode) or stat.S_ISLNK(st.st_mode):
                    if age > max_age_seconds:
                        os.unlink(name, dir_fd=dir_fd)
                        stats['files_removed'] += 1
                        stats['total_size_cleared'] += st.st_size
                        logger.debug(f"Removed stale file: {full_path} ({format_bytes(st.st_size)})")
            except OSError as e:
                if e.errno == 28:
                    stats['errors'].append(f"Disk full while removing {full_path}: {e}")
                continue

    try:
        walk(root_fd, root, os.fstat(root_fd).st_dev)
    finally:
        os.close(root_fd)


class DiskMonitor:
    """Monitor disk usage and I/O statistics."""

    def __init__(self):
        """Initialize the disk monitor."""
        self._platform = platform.system()

    def get_disk_usage(self, path: str = '/') -> Optional[dict]:
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
        if self._platform != 'Linux':
            return False
        return 'nvme' in device.lower()

    def _should_monitor_partition(self, partition: dict) -> bool:
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

        if fstype_lower in EXCLUDED_FSTYPES:
            return False

        total = partition.get('total', 0)
        return not (total > 0 and total < 100 * 1024 * 1024)

    def get_all_partitions(self, filter_monitorable: bool = True) -> list[dict]:
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

    def get_nvme_partitions(self) -> list[dict]:
        """
        Get all NVMe partitions.

        Returns:
            List of dictionaries containing NVMe partition information
        """
        all_partitions = self.get_all_partitions()
        return [p for p in all_partitions if p.get('is_nvme', False)]

    def get_disk_usage_human(self, path: str = '/') -> Optional[dict[str, str]]:
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

    def get_total_disk_usage(self, partitions: Optional[list[dict]] = None) -> dict:
        """
        Get total disk usage across all partitions.

        Args:
            partitions: Optional pre-fetched partition list from get_all_partitions()

        Returns:
            Dictionary containing total disk usage
        """
        if partitions is None:
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

    def get_disk_io_counters(self, perdisk: bool = False) -> dict:
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

    def get_disk_io_human(self, perdisk: bool = False) -> dict:
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
        return {
            'read_count': io_data['read_count'],
            'write_count': io_data['write_count'],
            'read_bytes': format_bytes(io_data['read_bytes']),
            'write_bytes': format_bytes(io_data['write_bytes']),
            'read_time': f"{io_data['read_time']} ms",
            'write_time': f"{io_data['write_time']} ms",
        }

    def get_directory_size(self, path: str, allowed_base_paths: Optional[list[str]] = None,
                          timeout: Optional[float] = None, max_depth: Optional[int] = None) -> Optional[dict]:
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
            logger.warning("[SECURITY] Path validation failed: path must be a non-empty string")
            return None

        if allowed_base_paths is None:
            allowed_base_paths = ['/home', '/var', '/tmp', '/usr/local']
            logger.debug(f"Using default base paths: {allowed_base_paths}")

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
                    filepath = os.path.join(dirpath, filename)
                    try:
                        total_size += os.lstat(filepath).st_size
                        file_count += 1
                    except (OSError, PermissionError):
                        continue

                    if timeout and file_count % 100 == 0 and (time.time() - start_time) > timeout:
                        logger.warning(f"Directory size calculation timed out after {timeout}s for {path}")
                        timed_out = True
                        break

                if timed_out:
                    break

        except (OSError, PermissionError) as e:
            logger.debug(f"Directory size failed: {e}")
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

    def get_directory_size_human(self, path: str, size_info: Optional[dict] = None) -> Optional[dict]:
        """
        Get directory size in human-readable format.

        Args:
            path: Directory path to check
            size_info: Optional result of get_directory_size() to avoid a second walk

        Returns:
            Dictionary with formatted size values, or None if unavailable
        """
        if size_info is None:
            size_info = self.get_directory_size(path)
        if size_info is None:
            return None

        return {
            'path': size_info.get('path', path),
            'size': format_bytes(size_info['size']),
            'size_bytes': size_info['size'],
            'is_file': size_info.get('is_file', False),
            'is_directory': size_info.get('is_directory', False),
            'file_count': size_info.get('file_count', 0),
            'dir_count': size_info.get('dir_count', 0),
        }

    def get_subdirectories_sizes(self, parent_path: str) -> list[dict]:
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

    def get_subdirectories_sizes_human(self, parent_path: str) -> list[dict]:
        """
        Get sizes of all immediate subdirectories in human-readable format.

        Args:
            parent_path: Parent directory path

        Returns:
            List of dictionaries with formatted subdirectory sizes
        """
        subdirs = self.get_subdirectories_sizes(parent_path)
        return [self.get_directory_size_human(subdir['path']) for subdir in subdirs if subdir]

    def get_largest_directories(self, path: str = '/', limit: int = 10) -> list[dict]:
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

    def get_largest_directories_human(self, path: str = '/', limit: int = 10) -> list[dict]:
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

    def cleanup_temporary_files(self, mountpoint: str = '/', max_age_days: float = 7) -> dict:
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
        max_age_seconds = max(3600.0, float(max_age_days) * 24 * 60 * 60)

        temp_dirs_env = os.getenv('TEMP_DIRS', None)
        if temp_dirs_env:
            temp_dirs_to_check = temp_dirs_env.split(':')
        elif platform.system() == 'Linux':
            temp_dirs_to_check = list(ALLOWED_TEMP_CLEANUP_BASES)
        else:
            temp_dirs_to_check = []

        temp_dirs = _resolve_cleanup_dirs(temp_dirs_to_check, ALLOWED_TEMP_CLEANUP_BASES)

        for temp_dir in temp_dirs:
            if not Path(temp_dir).is_dir():
                continue
            _clean_directory_tree(temp_dir, max_age_seconds, stats, now=current_time)

        return stats

    def _perform_log_cleanup(self, log_monitor, aggressive: bool = False, mountpoint: str = '/') -> dict:
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
            rotated_stats = log_monitor.cleanup_rotated_logs(force=aggressive)
            stats['logs_cleaned'] += rotated_stats.get('files_removed', 0)
            stats['total_size_cleared'] += rotated_stats.get('total_size_cleared', 0)
            stats['errors'].extend(rotated_stats.get('errors', []))

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

    def cleanup_disk_space(self, mountpoint: str = '/', target_free_percent: float = 20.0, log_monitor=None) -> dict:
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

        safe_log(logger.info, f"[CLEANUP] Starting disk cleanup: {initial_usage['percent']:.1f}% used, {current_free_percent:.1f}% free (target: {target_free_percent:.1f}% free)")

        aggressive_threshold = get_env_float(
            'DISK_AGGRESSIVE_CLEANUP_THRESHOLD',
            DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD,
            min_val=0.0,
            max_val=100.0,
        )

        if log_monitor:
            aggressive_cleanup = initial_usage['percent'] >= aggressive_threshold

            if aggressive_cleanup:
                message = (
                    f"[CLEANUP] Step 1: Aggressive log cleanup "
                    f"(disk >= {aggressive_threshold:.1f}%, emptying logs in /var/log)..."
                )
                safe_log(logger.warning, message)
            else:
                safe_log(logger.info, "[CLEANUP] Step 1: Cleaning log files (all logs in /var/log exceeding threshold)...")

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

        safe_log(logger.info, "[CLEANUP] Step 2: Cleaning temporary files...")

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

        if initial_usage['percent'] >= aggressive_threshold:
            safe_log(logger.info, "[CLEANUP] Step 3: Cleaning package caches and other space consumers...")
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
            safe_log(logger.info, f"[CLEANUP] Disk cleanup complete: {final_usage['percent']:.1f}% used, {final_free_percent:.1f}% free. "
                           f"Cleared {format_bytes(stats['total_size_cleared'])} "
                           f"({stats['files_removed']} files, {stats['dirs_removed']} dirs, {stats['logs_cleaned']} logs)")

        return stats

    def _cleanup_package_caches(self) -> dict:
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

        if platform.system() != 'Linux':
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

        cache_dirs_to_clean = _resolve_cleanup_dirs(cache_dirs_to_clean, ALLOWED_CACHE_CLEANUP_BASES)

        for cache_dir in cache_dirs_to_clean:
            cache_dir_obj = Path(cache_dir)
            if not cache_dir_obj.exists():
                continue

            try:
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

    def emergency_cleanup_disk_space(self, mountpoint: str = '/', log_monitor=None) -> dict:
        """
        Emergency cleanup when disk is critically full (near 100%).
        Performs aggressive cleanup including logs and all temporary files.
        Handles disk full errors gracefully to ensure cleanup can proceed.

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

        safe_log(logger.warning, f"[EMERGENCY] Disk critically full: {initial_usage['percent']:.1f}% used, {current_free_percent:.1f}% free")

        safe_log(logger.info, "[EMERGENCY] Step 1: Cleaning all temporary files...")

        cleanup_stats = self.cleanup_temporary_files(mountpoint, max_age_days=1.0 / 24.0)
        stats['files_removed'] += cleanup_stats['files_removed']
        stats['dirs_removed'] += cleanup_stats['dirs_removed']
        stats['total_size_cleared'] += cleanup_stats['total_size_cleared']
        stats['errors'].extend(cleanup_stats['errors'])

        if log_monitor:
            safe_log(logger.info, "[EMERGENCY] Step 2: Cleaning log files (aggressive - ALL logs in /var/log)...")

            log_cleanup_stats = self._perform_log_cleanup(log_monitor, aggressive=True, mountpoint=mountpoint)
            stats['logs_cleaned'] = log_cleanup_stats['logs_cleaned']
            stats['total_size_cleared'] += log_cleanup_stats['total_size_cleared']
            stats['errors'].extend(log_cleanup_stats['errors'])

        safe_log(logger.info, "[EMERGENCY] Step 3: Cleaning additional cache directories...")
        emergency_cache_dirs_env = os.getenv('EMERGENCY_CACHE_DIRS', None)
        if emergency_cache_dirs_env:
            candidate_dirs = emergency_cache_dirs_env.split(':')
        elif platform.system() == 'Linux':
            candidate_dirs = list(EMERGENCY_CACHE_DIRS_DEFAULT)
        else:
            candidate_dirs = []

        cache_dirs = _resolve_cleanup_dirs(candidate_dirs, ALLOWED_TEMP_CLEANUP_BASES + ALLOWED_CACHE_CLEANUP_BASES)
        for cache_dir in cache_dirs:
            if Path(cache_dir).is_dir():
                _clean_directory_tree(cache_dir, 24 * 60 * 60, stats)

        if stats['logs_cleaned'] > 0:
            self.restart_log_services()

        final_usage = self.get_disk_usage(mountpoint)
        if final_usage:
            stats['final_usage'] = final_usage['percent']
            final_free_percent = 100.0 - final_usage['percent']
            safe_log(logger.warning, f"[EMERGENCY] Complete: {final_usage['percent']:.1f}% used, {final_free_percent:.1f}% free. "
                              f"Cleared {format_bytes(stats['total_size_cleared'])} "
                              f"({stats['files_removed']} files, {stats['dirs_removed']} dirs, {stats['logs_cleaned']} logs)")

        return stats

    def cleanup_logs_and_restart_services(self, log_monitor=None) -> dict:
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
            rotated_stats = log_monitor.cleanup_rotated_logs(force=True)
            stats['logs_cleaned'] += rotated_stats.get('files_removed', 0)
            stats['total_size_cleared'] += rotated_stats.get('total_size_cleared', 0)
            stats['errors'].extend(rotated_stats.get('errors', []))

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
        if platform.system() != 'Linux':
            return

        if not get_env_bool('RESTART_LOG_SERVICES', False):
            logger.debug("Log service restart skipped (RESTART_LOG_SERVICES=false); "
                         "logs are trimmed in place so open file handles stay valid")
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
                logger.debug(f"{service} restart failed: {e}")
            except Exception as e:
                logger.debug(f"{service} restart failed: {e}")

    def setup_nginx_logs(self, max_retries: int = 3):
        """
        Setup nginx log directory and files with proper ownership and permissions.
        Creates nginx log directory if it doesn't exist, sets ownership,
        creates log files, and restarts nginx.

        Args:
            max_retries: Maximum number of retry attempts for nginx restart
        """
        if platform.system() != 'Linux':
            return

        if not get_env_bool('MANAGE_NGINX_LOGS', False):
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
                        logger.debug(f"Created nginx log file: {log_path}")
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

