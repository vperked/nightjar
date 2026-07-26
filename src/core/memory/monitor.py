import psutil
import time
from typing import Dict, Optional, List, Callable
from core.format import format_bytes


class MemoryMonitor:
    """Monitor memory usage and statistics."""

    def get_memory_percent(self) -> float:
        """
        Get current memory usage percentage.

        Returns:
            Memory usage as a percentage (0-100)
        """
        return psutil.virtual_memory().percent

    def get_memory_info(self) -> Dict[str, int]:
        """
        Get detailed memory information.

        Returns:
            Dictionary containing:
            - total: Total physical memory in bytes
            - available: Available memory in bytes
            - used: Used memory in bytes
            - free: Free memory in bytes
            - percent: Memory usage percentage
        """
        mem = psutil.virtual_memory()
        return {
            'total': mem.total,
            'available': mem.available,
            'used': mem.used,
            'free': mem.free,
            'percent': mem.percent,
            'cached': getattr(mem, 'cached', 0),
            'buffers': getattr(mem, 'buffers', 0),
            'shared': getattr(mem, 'shared', 0),
        }

    def get_memory_info_human(self) -> Dict[str, str]:
        """
        Get memory information in human-readable format.

        Returns:
            Dictionary with memory values formatted as strings (KB, MB, GB, TB)
        """
        mem_info = self.get_memory_info()

        return {
            'total': format_bytes(mem_info['total']),
            'available': format_bytes(mem_info['available']),
            'used': format_bytes(mem_info['used']),
            'free': format_bytes(mem_info['free']),
            'percent': f"{mem_info['percent']:.1f}%",
            'cached': format_bytes(mem_info['cached']),
            'buffers': format_bytes(mem_info['buffers']),
            'shared': format_bytes(mem_info['shared']),
        }

    def get_swap_info(self) -> Dict[str, int]:
        """
        Get swap memory information.

        Returns:
            Dictionary containing:
            - total: Total swap memory in bytes
            - used: Used swap memory in bytes
            - free: Free swap memory in bytes
            - percent: Swap usage percentage
        """
        swap = psutil.swap_memory()
        return {
            'total': swap.total,
            'used': swap.used,
            'free': swap.free,
            'percent': swap.percent,
            'sin': getattr(swap, 'sin', 0),
            'sout': getattr(swap, 'sout', 0),
        }

    def get_swap_info_human(self) -> Dict[str, str]:
        """
        Get swap memory information in human-readable format.

        Returns:
            Dictionary with swap values formatted as strings (KB, MB, GB, TB)
        """
        swap_info = self.get_swap_info()

        return {
            'total': format_bytes(swap_info['total']),
            'used': format_bytes(swap_info['used']),
            'free': format_bytes(swap_info['free']),
            'percent': f"{swap_info['percent']:.1f}%",
            'sin': format_bytes(swap_info['sin']),
            'sout': format_bytes(swap_info['sout']),
        }

    def get_memory_stats(self) -> Dict:
        """
        Get comprehensive memory statistics.

        Returns:
            Dictionary containing all memory information including virtual and swap memory
        """
        return {
            'virtual_memory': self.get_memory_info(),
            'virtual_memory_human': self.get_memory_info_human(),
            'swap_memory': self.get_swap_info(),
            'swap_memory_human': self.get_swap_info_human(),
            'memory_percent': self.get_memory_percent(),
        }

    def monitor_continuous(self, interval: float = 1.0, duration: Optional[float] = None,
                          callback: Optional[Callable[[Dict], None]] = None) -> List[Dict]:
        """
        Continuously monitor memory usage.

        Args:
            interval: Time interval between measurements in seconds
            duration: Total duration to monitor in seconds (None for infinite)
            callback: Optional callback function to call with each measurement

        Returns:
            List of memory usage measurements
        """
        measurements = []
        start_time = time.time()

        while True:
            if duration and (time.time() - start_time) >= duration:
                break

            measurement = {
                'timestamp': time.time(),
                'memory_percent': self.get_memory_percent(),
                'memory_info': self.get_memory_info(),
                'swap_info': self.get_swap_info(),
            }

            measurements.append(measurement)

            if callback:
                callback(measurement)

            time.sleep(interval)

        return measurements

