import psutil
import time
from typing import Dict, List, Optional, Callable
from datetime import datetime
from core.format import format_bytes, CPU_COUNT
from core.observability import Component, get_logger

logger = get_logger(Component.PROCESSES)


class ProcessMonitor:
    """Monitor system processes and their resource usage."""

    def __init__(self):
        """Initialize the process monitor."""
        pass

    def get_process_count(self) -> Dict:
        """
        Get process count by status.

        Returns:
            Dictionary containing process counts by status
        """
        processes = psutil.process_iter(['status'])
        status_count = {}

        for proc in processes:
            try:
                status = proc.info['status']
                status_count[status] = status_count.get(status, 0) + 1
            except (psutil.NoSuchProcess, psutil.AccessDenied):
                continue

        return {
            'total': len(psutil.pids()),
            'by_status': status_count,
        }

    def get_process_info(self, pid: int) -> Optional[Dict]:
        """
        Get detailed information about a specific process.

        Args:
            pid: Process ID

        Returns:
            Dictionary containing process information, or None if process not found
        """
        try:
            proc = psutil.Process(pid)
            with proc.oneshot():
                cpu_percent = proc.cpu_percent(interval=0.1)
                cpu_percent = min(cpu_percent / CPU_COUNT, 100.0) if CPU_COUNT > 0 else min(cpu_percent, 100.0)

                return {
                    'pid': pid,
                    'name': proc.name(),
                    'status': proc.status(),
                    'cpu_percent': cpu_percent,
                    'memory_percent': proc.memory_percent(),
                    'memory_info': {
                        'rss': proc.memory_info().rss,
                        'vms': proc.memory_info().vms,
                        'shared': getattr(proc.memory_info(), 'shared', 0),
                    },
                    'num_threads': proc.num_threads(),
                    'create_time': datetime.fromtimestamp(proc.create_time()).isoformat(),
                    'username': proc.username(),
                    'cmdline': proc.cmdline(),
                    'exe': proc.exe() if proc.exe() else None,
                    'cwd': proc.cwd() if proc.cwd() else None,
                }
        except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
            return None

    def find_processes_by_name(self, name: str, exact_match: bool = False) -> List[Dict]:
        """
        Find processes by name.

        Args:
            name: Process name to search for
            exact_match: If True, match exact name; if False, case-insensitive partial match

        Returns:
            List of process dictionaries matching the name
        """
        matching_processes = []

        for proc in psutil.process_iter(['pid', 'name']):
            try:
                proc_name = proc.info['name']
                if exact_match:
                    if proc_name == name:
                        matching_processes.append(self.get_process_info(proc.info['pid']))
                else:
                    if name.lower() in proc_name.lower():
                        matching_processes.append(self.get_process_info(proc.info['pid']))
            except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
                continue

        return matching_processes

    def get_top_processes(self, limit: int = 10, sort_by: str = 'cpu') -> List[Dict]:
        """
        Get top processes by CPU or memory usage.
        Optimized to minimize expensive calls by filtering candidates first.

        Args:
            limit: Number of processes to return
            sort_by: 'cpu' or 'memory' to sort by

        Returns:
            List of process dictionaries sorted by the specified metric
        """
        processes = []

        if sort_by == 'cpu':
            try:
                for proc in psutil.process_iter(['pid', 'name', 'memory_percent']):
                    try:
                        proc_info = proc.info.copy()
                        if proc_info.get('memory_percent') is None:
                            proc_info['memory_percent'] = proc.memory_percent()
                        proc_info['cpu_percent'] = 0.0
                        processes.append(proc_info)
                    except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError):
                        continue
            except (PermissionError, OSError) as e:
                logger.debug(f"Process iteration failed: {e}")
                return []
            except Exception as e:
                logger.error(f"Failed to iterate processes: {e}", exc_info=True)
                return []

            processes.sort(key=lambda x: x.get('memory_percent', 0), reverse=True)
            candidates = processes[:min(limit * 5, 50, len(processes))]

            for proc_info in candidates:
                try:
                    proc = psutil.Process(proc_info['pid'])
                    cpu_percent = proc.cpu_percent(interval=None)
                    cpu_count = psutil.cpu_count(logical=True) or 1
                    proc_info['cpu_percent'] = min(cpu_percent / cpu_count, 100.0)
                except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError):
                    proc_info['cpu_percent'] = 0.0
                    continue

            processes.sort(key=lambda x: x.get('cpu_percent', 0), reverse=True)
        else:
            try:
                for proc in psutil.process_iter(['pid', 'name', 'memory_percent']):
                    try:
                        proc_info = proc.info.copy()
                        if proc_info.get('memory_percent') is None:
                            proc_info['memory_percent'] = proc.memory_percent()
                        proc_info['cpu_percent'] = 0.0
                        processes.append(proc_info)
                    except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError):
                        continue
            except (PermissionError, OSError) as e:
                logger.debug(f"Process iteration failed: {e}")
                return []
            except Exception as e:
                logger.error(f"Failed to iterate processes: {e}", exc_info=True)
                return []

            processes.sort(key=lambda x: x.get('memory_percent', 0), reverse=True)

        top_pids = [p['pid'] for p in processes[:limit]]
        result = []
        for pid in top_pids:
            proc_info = self.get_process_info(pid)
            if proc_info:
                result.append(proc_info)

        if sort_by == 'cpu':
            result.sort(key=lambda x: x.get('cpu_percent', 0), reverse=True)
        elif sort_by == 'memory':
            result.sort(key=lambda x: x.get('memory_percent', 0), reverse=True)

        return result[:limit]

    def get_process_tree(self, pid: int) -> Dict:
        """
        Get process tree (parent and children) for a process.

        Args:
            pid: Process ID

        Returns:
            Dictionary containing process tree information
        """
        try:
            proc = psutil.Process(pid)
            parent = None
            if proc.parent():
                try:
                    parent = {
                        'pid': proc.parent().pid,
                        'name': proc.parent().name(),
                    }
                except (psutil.NoSuchProcess, psutil.AccessDenied):
                    pass

            children = []
            for child in proc.children(recursive=False):
                try:
                    children.append({
                        'pid': child.pid,
                        'name': child.name(),
                    })
                except (psutil.NoSuchProcess, psutil.AccessDenied):
                    continue

            return {
                'pid': pid,
                'name': proc.name(),
                'parent': parent,
                'children': children,
            }
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            return {}

    def monitor_process(self, pid: int, interval: float = 1.0, duration: Optional[float] = None,
                       callback: Optional[Callable[[Dict], None]] = None) -> List[Dict]:
        """
        Continuously monitor a specific process.

        Args:
            pid: Process ID to monitor
            interval: Time interval between measurements in seconds
            duration: Total duration to monitor in seconds (None for infinite)
            callback: Optional callback function to call with each measurement

        Returns:
            List of process measurements
        """
        measurements = []
        start_time = time.time()

        while True:
            if duration and (time.time() - start_time) >= duration:
                break

            proc_info = self.get_process_info(pid)
            if not proc_info:
                break

            measurement = {
                'timestamp': time.time(),
                'pid': pid,
                'cpu_percent': proc_info['cpu_percent'],
                'memory_percent': proc_info['memory_percent'],
                'memory_rss': proc_info['memory_info']['rss'],
                'num_threads': proc_info['num_threads'],
            }

            measurements.append(measurement)

            if callback:
                callback(measurement)

            time.sleep(interval)

        return measurements

    def get_processes_by_user(self, username: Optional[str] = None) -> List[Dict]:
        """
        Get all processes for a specific user, or all users if username is None.

        Args:
            username: Username to filter by (None for all users)

        Returns:
            List of process dictionaries
        """
        processes = []

        for proc in psutil.process_iter(['pid', 'name', 'username']):
            try:
                proc_username = proc.info.get('username')
                if username is None or proc_username == username:
                    proc_info = self.get_process_info(proc.info['pid'])
                    if proc_info:
                        processes.append(proc_info)
            except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
                continue

        return processes

    def get_process_summary(self) -> Dict:
        """
        Get a summary of all processes.

        Returns:
            Dictionary containing process summary statistics
        """
        process_count = self.get_process_count()
        top_cpu = self.get_top_processes(limit=5, sort_by='cpu')
        top_memory = self.get_top_processes(limit=5, sort_by='memory')

        return {
            'total_processes': process_count['total'],
            'by_status': process_count['by_status'],
            'top_cpu_processes': top_cpu,
            'top_memory_processes': top_memory,
        }

    def format_memory_bytes(self, bytes_value: int) -> str:
        """
        Format memory bytes to human-readable format.

        Args:
            bytes_value: Memory in bytes

        Returns:
            Formatted string (e.g., "1.5 GB")
        """
        return format_bytes(bytes_value)

