import psutil
import time
import os
from typing import Dict, List, Optional
from util.util import format_bytes


class DiskMonitor:
    """Monitor disk usage and I/O statistics."""
    
    def __init__(self):
        """Initialize the disk monitor."""
        pass
    
    def get_disk_usage(self, path: str = '/') -> Dict:
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
    
    def get_all_partitions(self) -> List[Dict]:
        """
        Get information about all disk partitions.
        
        Returns:
            List of dictionaries containing partition information
        """
        partitions = []
        for partition in psutil.disk_partitions():
            try:
                usage = psutil.disk_usage(partition.mountpoint)
                partition_info = {
                    'device': partition.device,
                    'mountpoint': partition.mountpoint,
                    'fstype': partition.fstype,
                    'opts': partition.opts,
                    'total': usage.total,
                    'used': usage.used,
                    'free': usage.free,
                    'percent': usage.percent,
                }
                partitions.append(partition_info)
            except (PermissionError, OSError):
                continue
        return partitions
    
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
                          callback: Optional[callable] = None) -> List[Dict]:
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
    
    def get_directory_size(self, path: str) -> Optional[Dict]:
        """
        Get the total size of a directory and all its contents recursively.
        
        Args:
            path: Directory path to check
            
        Returns:
            Dictionary containing directory size information, or None if path doesn't exist
        """
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
        
        try:
            for dirpath, dirnames, filenames in os.walk(path):
                dir_count += len(dirnames)
                
                for filename in filenames:
                    filepath = os.path.join(dirpath, filename)
                    try:
                        total_size += os.path.getsize(filepath)
                        file_count += 1
                    except (OSError, PermissionError):
                        continue
        except (OSError, PermissionError) as e:
            return None
        
        return {
            'path': path,
            'size': total_size,
            'is_file': False,
            'is_directory': True,
            'file_count': file_count,
            'dir_count': dir_count,
        }
    
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

