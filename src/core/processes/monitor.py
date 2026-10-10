import heapq
import threading
import time
from operator import itemgetter

import psutil
from typing import Optional, Callable
from datetime import datetime
from core.format import format_bytes, CPU_COUNT
from core.observability import Component, get_logger

logger = get_logger(Component.PROCESSES)

PROCESS_RANK_ATTRS = [
    'pid',
    'name',
    'cpu_percent',
    'memory_percent',
    'username',
    'memory_info',
]

# Ranking only needs these; the expensive fields (username lookup, memory_info) are
# fetched afterwards for the handful of processes that are actually returned.
PROCESS_LEAN_ATTRS = ['pid', 'name', 'cpu_percent', 'memory_percent', 'status']
PROCESS_DETAIL_ATTRS = ['username', 'memory_info']

# psutil reports a process's CPU as usage since the previous read of that same process, so
# independent scans (dashboard and alert loop) would each measure over arbitrary short windows.
# Every caller shares one scan instead, reused for this many seconds.
SHARED_SCAN_MAX_AGE = 2.0


class ProcessMonitor:
    """Monitor system processes and their resource usage."""

    def __init__(self):
        """Initialize the process monitor."""
        self._cpu_count = CPU_COUNT if CPU_COUNT and CPU_COUNT > 0 else 1
        self._scan_lock = threading.Lock()
        self._scan_cache = None

    def _normalize_cpu(self, cpu_percent_raw) -> float:
        """Scale process CPU percent to 0-100 relative to logical core count."""
        if cpu_percent_raw is None:
            return 0.0
        return min(cpu_percent_raw / self._cpu_count, 100.0)

    def _snapshot_from_info(self, info: dict) -> Optional[dict]:
        """
        Build a process info dict from psutil process_iter fields.

        Args:
            info: Attribute dict from process_iter

        Returns:
            Process snapshot, or None if required fields are missing
        """
        pid = info.get('pid')
        if pid is None:
            return None

        mem = info.get('memory_info')

        return {
            'pid': pid,
            'name': info.get('name') or 'Unknown',
            'status': info.get('status'),
            'cpu_percent': self._normalize_cpu(info.get('cpu_percent')),
            'memory_percent': info.get('memory_percent') or 0.0,
            'memory_info': {
                'rss': getattr(mem, 'rss', 0) if mem is not None else 0,
                'vms': getattr(mem, 'vms', 0) if mem is not None else 0,
                'shared': getattr(mem, 'shared', 0) if mem is not None else 0,
            },
            'num_threads': info.get('num_threads') or 0,
            'create_time': None,
            'username': info.get('username'),
            'cmdline': info.get('cmdline') or [],
            'exe': None,
            'cwd': None,
        }

    def _enrich_snapshots(self, snapshots: list[dict]) -> None:
        """Fill username and memory details in place for already-ranked snapshots."""
        for snapshot in snapshots:
            if snapshot.get('username') is not None:
                continue
            try:
                info = psutil.Process(snapshot['pid']).as_dict(attrs=PROCESS_DETAIL_ATTRS)
            except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, OSError):
                continue
            snapshot['username'] = info.get('username')
            mem = info.get('memory_info')
            if mem is not None:
                snapshot['memory_info'] = {
                    'rss': getattr(mem, 'rss', 0),
                    'vms': getattr(mem, 'vms', 0),
                    'shared': getattr(mem, 'shared', 0),
                }

    def _collect_snapshots(self, lean: bool = False) -> list[dict]:
        """
        Collect process snapshots in a single process_iter pass.

        Args:
            lean: Fetch only the fields needed to rank processes; callers must
                pass the survivors to _enrich_snapshots
        """
        snapshots = []
        try:
            for proc in psutil.process_iter(PROCESS_LEAN_ATTRS if lean else PROCESS_RANK_ATTRS):
                try:
                    snapshot = self._snapshot_from_info(proc.info)
                    if snapshot:
                        snapshots.append(snapshot)
                except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError):
                    continue
        except (PermissionError, OSError) as e:
            logger.debug(f"Process iteration failed: {e}")
            return []
        except Exception as e:
            logger.error(f"Failed to iterate processes: {e}", exc_info=True)
            return []
        return snapshots

    def get_process_count(self) -> dict:
        """
        Get process count by status.

        Returns:
            Dictionary containing process counts by status
        """
        status_count = {}
        total = 0

        try:
            for proc in psutil.process_iter(['status']):
                try:
                    total += 1
                    status = proc.info['status']
                    status_count[status] = status_count.get(status, 0) + 1
                except (psutil.NoSuchProcess, psutil.AccessDenied):
                    continue
        except (PermissionError, OSError):
            return {'total': 0, 'by_status': {}}

        return {
            'total': total,
            'by_status': status_count,
        }

    def get_process_info(self, pid: int) -> Optional[dict]:
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
                mem_info = proc.memory_info()
                cpu_percent = self._normalize_cpu(proc.cpu_percent(interval=None))
                try:
                    exe = proc.exe() or None
                except (psutil.AccessDenied, psutil.ZombieProcess, OSError):
                    exe = None
                try:
                    cwd = proc.cwd() or None
                except (psutil.AccessDenied, psutil.ZombieProcess, OSError):
                    cwd = None
                try:
                    cmdline = proc.cmdline()
                except (psutil.AccessDenied, psutil.ZombieProcess, OSError):
                    cmdline = []

                return {
                    'pid': pid,
                    'name': proc.name(),
                    'status': proc.status(),
                    'cpu_percent': cpu_percent,
                    'memory_percent': proc.memory_percent(),
                    'memory_info': {
                        'rss': mem_info.rss,
                        'vms': mem_info.vms,
                        'shared': getattr(mem_info, 'shared', 0),
                    },
                    'num_threads': proc.num_threads(),
                    'create_time': datetime.fromtimestamp(proc.create_time()).isoformat(),
                    'username': proc.username(),
                    'cmdline': cmdline,
                    'exe': exe,
                    'cwd': cwd,
                }
        except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
            return None

    def find_processes_by_name(self, name: str, exact_match: bool = False) -> list[dict]:
        """
        Find processes by name.

        Args:
            name: Process name to search for
            exact_match: If True, match exact name; if False, case-insensitive partial match

        Returns:
            List of process dictionaries matching the name
        """
        matching_processes = []
        name_lower = name.lower()

        for snapshot in self._collect_snapshots():
            proc_name = snapshot.get('name') or ''
            if exact_match:
                if proc_name == name:
                    matching_processes.append(snapshot)
            elif name_lower in proc_name.lower():
                matching_processes.append(snapshot)

        return matching_processes

    def get_process_overview(self, limit: int = 5, max_age: float = SHARED_SCAN_MAX_AGE) -> dict:
        """
        Count processes by status and rank the top CPU and memory users, from one shared scan.

        Thread-safe. A scan younger than `max_age` seconds that ranked at least `limit`
        processes is reused, so the dashboard and the alert loop see the same CPU sample.

        Args:
            limit: Number of top processes to return per metric
            max_age: Maximum age in seconds of a reusable scan

        Returns:
            Dictionary with 'count' ({'total', 'by_status'}), 'top_cpu' and 'top_memory'
        """
        with self._scan_lock:
            now = time.monotonic()
            cached = self._scan_cache
            if cached is None or cached[1] < limit or now - cached[0] >= max_age:
                cached = (now, limit, self._scan(limit))
                self._scan_cache = cached
            overview = cached[2]
        return {
            'count': overview['count'],
            'top_cpu': overview['top_cpu'][:limit],
            'top_memory': overview['top_memory'][:limit],
        }

    def _scan(self, limit: int) -> dict:
        snapshots = self._collect_snapshots(lean=True)
        by_status: dict[str, int] = {}
        for snapshot in snapshots:
            status = snapshot.get('status')
            if status:
                by_status[status] = by_status.get(status, 0) + 1

        if limit <= 0 or not snapshots:
            top_cpu, top_memory = [], []
        else:
            top_cpu = heapq.nlargest(limit, snapshots, key=itemgetter('cpu_percent'))
            top_memory = heapq.nlargest(limit, snapshots, key=itemgetter('memory_percent'))
            ranked = {id(p): p for p in top_cpu + top_memory}
            self._enrich_snapshots(list(ranked.values()))
        return {
            'count': {'total': len(snapshots), 'by_status': by_status},
            'top_cpu': top_cpu,
            'top_memory': top_memory,
        }

    def get_top_processes(self, limit: int = 10, sort_by: str = 'cpu') -> list[dict]:
        """
        Get top processes by CPU or memory usage (from the shared scan).

        CPU is the percentage since the previous scan; the first scan after start
        may report 0% until counters are primed.

        Args:
            limit: Number of processes to return
            sort_by: 'cpu' or 'memory' to sort by

        Returns:
            List of process dictionaries sorted by the specified metric
        """
        overview = self.get_process_overview(limit=limit)
        return overview['top_memory' if sort_by == 'memory' else 'top_cpu']

    def get_top_cpu_and_memory(self, limit: int = 10) -> tuple:
        """
        Get top CPU and memory processes (from the shared scan).

        Args:
            limit: Number of processes to return per list

        Returns:
            Tuple of (top_cpu_processes, top_memory_processes)
        """
        overview = self.get_process_overview(limit=limit)
        return overview['top_cpu'], overview['top_memory']

    def get_process_tree(self, pid: int) -> dict:
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
            parent_proc = proc.parent()
            if parent_proc:
                try:
                    parent = {
                        'pid': parent_proc.pid,
                        'name': parent_proc.name(),
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
                       callback: Optional[Callable[[dict], None]] = None) -> list[dict]:
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

    def get_processes_by_user(self, username: Optional[str] = None) -> list[dict]:
        """
        Get all processes for a specific user, or all users if username is None.

        Args:
            username: Username to filter by (None for all users)

        Returns:
            List of process dictionaries
        """
        processes = []
        for snapshot in self._collect_snapshots():
            proc_username = snapshot.get('username')
            if username is None or proc_username == username:
                processes.append(snapshot)
        return processes

    def get_process_summary(self) -> dict:
        """
        Get a summary of all processes.

        Returns:
            Dictionary containing process summary statistics
        """
        process_count = self.get_process_count()
        top_cpu, top_memory = self.get_top_cpu_and_memory(limit=5)

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
