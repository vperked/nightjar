import psutil
import platform
import socket
import time
from datetime import datetime, timedelta
from typing import Dict, List, Optional
from monitor.cpu import CpuMonitor
from monitor.memory import MemoryMonitor
from util.util import format_bytes, CPU_COUNT


class SystemMonitor:
    """Monitor comprehensive system information."""

    def __init__(self):
        """Initialize the system monitor."""
        self.cpu_monitor = CpuMonitor()
        self.memory_monitor = MemoryMonitor()

    def get_system_info(self) -> Dict:
        """
        Get basic system information.

        Returns:
            Dictionary containing system information
        """
        return {
            'platform': platform.system(),
            'platform_release': platform.release(),
            'platform_version': platform.version(),
            'architecture': platform.machine(),
            'processor': platform.processor(),
            'hostname': socket.gethostname(),
            'python_version': platform.python_version(),
        }

    def get_boot_time(self) -> float:
        """
        Get system boot time.

        Returns:
            Boot time as Unix timestamp
        """
        return psutil.boot_time()

    def get_uptime(self) -> Dict[str, str]:
        """
        Get system uptime.

        Returns:
            Dictionary with uptime in various formats
        """
        boot_time = self.get_boot_time()
        uptime_seconds = time.time() - boot_time
        uptime_delta = timedelta(seconds=int(uptime_seconds))

        days = uptime_delta.days
        hours, remainder = divmod(uptime_delta.seconds, 3600)
        minutes, seconds = divmod(remainder, 60)

        return {
            'seconds': int(uptime_seconds),
            'formatted': f"{days} days, {hours} hours, {minutes} minutes, {seconds} seconds",
            'days': days,
            'hours': hours,
            'minutes': minutes,
            'boot_time': datetime.fromtimestamp(boot_time).isoformat(),
        }

    def get_users(self) -> List[Dict]:
        """
        Get currently logged in users.

        Returns:
            List of dictionaries containing user information
        """
        users = []
        for user in psutil.users():
            users.append({
                'name': user.name,
                'terminal': user.terminal or 'N/A',
                'host': user.host or 'N/A',
                'started': datetime.fromtimestamp(user.started).isoformat() if user.started else 'N/A',
            })
        return users

    def get_disk_info(self) -> Dict:
        """
        Get disk usage information for all partitions.

        Returns:
            Dictionary containing disk information for all partitions
        """
        partitions = psutil.disk_partitions()
        disk_info = {
            'partitions': [],
            'total': {'total': 0, 'used': 0, 'free': 0}
        }

        for partition in partitions:
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
                disk_info['partitions'].append(partition_info)
                disk_info['total']['total'] += usage.total
                disk_info['total']['used'] += usage.used
                disk_info['total']['free'] += usage.free
            except PermissionError:
                continue

        if disk_info['total']['total'] > 0:
            disk_info['total']['percent'] = (disk_info['total']['used'] / disk_info['total']['total']) * 100
        else:
            disk_info['total']['percent'] = 0

        return disk_info

    def get_disk_io(self) -> Dict:
        """
        Get disk I/O statistics.

        Returns:
            Dictionary containing disk I/O statistics
        """
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

    def get_network_info(self) -> Dict:
        """
        Get network interface information.

        Returns:
            Dictionary containing network interface information
        """
        interfaces = psutil.net_if_addrs()
        stats = psutil.net_if_stats()
        io_counters = psutil.net_io_counters(pernic=True)

        network_info = {
            'interfaces': [],
            'total': {}
        }

        for interface_name, addrs in interfaces.items():
            interface_info = {
                'name': interface_name,
                'addresses': [],
                'isup': stats[interface_name].isup if interface_name in stats else False,
                'speed': stats[interface_name].speed if interface_name in stats else 0,
                'mtu': stats[interface_name].mtu if interface_name in stats else 0,
            }

            for addr in addrs:
                interface_info['addresses'].append({
                    'family': str(addr.family),
                    'address': addr.address,
                    'netmask': addr.netmask if addr.netmask else None,
                    'broadcast': addr.broadcast if addr.broadcast else None,
                })

            if interface_name in io_counters:
                io = io_counters[interface_name]
                interface_info['io'] = {
                    'bytes_sent': io.bytes_sent,
                    'bytes_recv': io.bytes_recv,
                    'packets_sent': io.packets_sent,
                    'packets_recv': io.packets_recv,
                    'errin': io.errin,
                    'errout': io.errout,
                    'dropin': io.dropin,
                    'dropout': io.dropout,
                }

            network_info['interfaces'].append(interface_info)

        total_io = psutil.net_io_counters()
        if total_io:
            network_info['total'] = {
                'bytes_sent': total_io.bytes_sent,
                'bytes_recv': total_io.bytes_recv,
                'packets_sent': total_io.packets_sent,
                'packets_recv': total_io.packets_recv,
                'errin': total_io.errin,
                'errout': total_io.errout,
                'dropin': total_io.dropin,
                'dropout': total_io.dropout,
            }

        return network_info

    def get_process_count(self) -> Dict:
        """
        Get process count information.

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

    def get_top_processes(self, limit: int = 10, sort_by: str = 'cpu') -> List[Dict]:
        """
        Get top processes by CPU or memory usage.
        Optimized to fetch all necessary attributes in one pass.

        Args:
            limit: Number of processes to return
            sort_by: 'cpu' or 'memory' to sort by

        Returns:
            List of process dictionaries sorted by the specified metric
        """
        processes_data = []

        attrs = ['pid', 'name', 'cpu_percent', 'memory_percent']

        for proc in psutil.process_iter(attrs=attrs):
            try:
                cpu_percent_raw = proc.info.get('cpu_percent', 0.0)
                cpu_count = self.cpu_monitor.cpu_count if self.cpu_monitor.cpu_count > 0 else 1
                cpu_percent_normalized = min(cpu_percent_raw / cpu_count, 100.0)

                processes_data.append({
                    'pid': proc.info['pid'],
                    'name': proc.info['name'],
                    'cpu_percent': cpu_percent_normalized,
                    'memory_percent': proc.info.get('memory_percent', 0.0),
                })
            except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, TypeError):
                continue

        if sort_by == 'cpu':
            processes_data.sort(key=lambda x: x.get('cpu_percent', 0), reverse=True)
        elif sort_by == 'memory':
            processes_data.sort(key=lambda x: x.get('memory_percent', 0), reverse=True)

        return processes_data[:limit]

    def get_all_info(self, include_top_processes: bool = False) -> Dict:
        """
        Get comprehensive system information.

        Args:
            include_top_processes: Whether to include top processes (can be slow, default: False)

        Returns:
            Dictionary containing all system information
        """
        info = {
            'timestamp': datetime.now().isoformat(),
            'system': self.get_system_info(),
            'uptime': self.get_uptime(),
            'users': self.get_users(),
            'cpu': self.cpu_monitor.get_cpu_info(),
            'memory': self.memory_monitor.get_memory_stats(),
            'disk': self.get_disk_info(),
            'disk_io': self.get_disk_io(),
            'network': self.get_network_info(),
            'processes': self.get_process_count(),
        }

        if include_top_processes:
            info['top_processes_cpu'] = self.get_top_processes(limit=5, sort_by='cpu')
            info['top_processes_memory'] = self.get_top_processes(limit=5, sort_by='memory')
        else:
            info['top_processes_cpu'] = []
            info['top_processes_memory'] = []

        return info

    def get_system_summary(self) -> Dict[str, str]:
        """
        Get a human-readable system summary (fast version, doesn't gather all processes).

        Returns:
            Dictionary with formatted system summary
        """
        system_info = self.get_system_info()
        uptime = self.get_uptime()
        cpu_info = self.cpu_monitor.get_cpu_info()
        memory_info = self.memory_monitor.get_memory_info_human()
        disk_info = self.get_disk_info()
        process_count = self.get_process_count()
        users = self.get_users()

        total_disk = disk_info['total']
        total_disk_formatted = format_bytes(total_disk['total'])
        used_disk_formatted = format_bytes(total_disk['used'])

        return {
            'hostname': system_info['hostname'],
            'platform': f"{system_info['platform']} {system_info['platform_release']}",
            'uptime': uptime['formatted'],
            'cpu_usage': f"{cpu_info['cpu_percent']:.1f}%",
            'cpu_count': f"{cpu_info['cpu_count_logical']} logical, {cpu_info['cpu_count_physical']} physical",
            'memory_usage': f"{memory_info['percent']} ({memory_info['used']} / {memory_info['total']})",
            'disk_usage': f"{total_disk['percent']:.1f}% ({used_disk_formatted} / {total_disk_formatted})",
            'processes': f"{process_count['total']} total",
            'users': f"{len(users)} logged in",
        }

