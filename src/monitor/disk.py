import psutil
import time
import os
import platform
from typing import Dict, List, Optional, Callable
from util.util import format_bytes
from util.security import validate_path, SecurityError


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
            allowed_base_paths: Optional list of allowed base paths for security validation
            timeout: Optional timeout in seconds. If exceeded, returns partial results.
            max_depth: Optional maximum depth to traverse. None for unlimited.
            
        Returns:
            Dictionary containing directory size information, or None if path doesn't exist or is invalid.
            If timeout is exceeded, returns partial results with 'timed_out': True flag.
        """
        import time
        
        if not path or not isinstance(path, str):
            return None
        
        is_valid, resolved_path, error = validate_path(path, allowed_base_paths=allowed_base_paths, allow_symlinks=False)
        if not is_valid:
            return None
        
        path = resolved_path
        
        if not os.path.exists(path):
            return None
        
        if not os.path.isdir(path):
            try:
                size = os.path.getsize(path)
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
        start_time = time.time() if timeout else None
        
        try:
            for dirpath, dirnames, filenames in os.walk(path, topdown=True):
                if timeout and (time.time() - start_time) > timeout:
                    logger.warning(f"Directory size calculation timed out after {timeout}s for {path}")
                    timed_out = True
                    break
                
                if max_depth is not None:
                    current_depth = dirpath[len(path):].count(os.sep)
                    if current_depth >= max_depth:
                        dirnames.clear()
                
                dir_count += len(dirnames)
                
                for filename in filenames:
                    filepath = os.path.join(dirpath, filename)
                    try:
                        total_size += os.path.getsize(filepath)
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
        if not os.path.exists(parent_path) or not os.path.isdir(parent_path):
            return []
        
        subdirs = []
        try:
            for item in os.listdir(parent_path):
                item_path = os.path.join(parent_path, item)
                if os.path.isdir(item_path):
                    size_info = self.get_directory_size(item_path)
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
        if not os.path.exists(path) or not os.path.isdir(path):
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

