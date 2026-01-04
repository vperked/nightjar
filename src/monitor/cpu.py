import psutil
import time
from typing import Dict, List, Optional, Tuple


class CpuMonitor:
    """Monitor CPU usage and statistics."""
    
    def __init__(self):
        """Initialize the CPU monitor."""
        self.cpu_count = psutil.cpu_count(logical=True)
        self.cpu_count_physical = psutil.cpu_count(logical=False)
    
    def get_cpu_percent(self, interval: float = 1.0) -> float:
        """
        Get current CPU usage percentage.
        
        Args:
            interval: Time interval in seconds to measure CPU usage
            
        Returns:
            CPU usage as a percentage (0-100)
        """
        return psutil.cpu_percent(interval=interval)
    
    def get_cpu_percent_per_core(self, interval: float = 1.0) -> List[float]:
        """
        Get CPU usage percentage for each core.
        
        Args:
            interval: Time interval in seconds to measure CPU usage
            
        Returns:
            List of CPU usage percentages for each core
        """
        return psutil.cpu_percent(interval=interval, percpu=True)
    
    def get_cpu_stats(self) -> Dict[str, float]:
        """
        Get detailed CPU statistics.
        
        Returns:
            Dictionary containing CPU statistics including:
            - user: time spent in user mode
            - nice: time spent in user mode with low priority
            - system: time spent in system mode
            - idle: time spent idle
            - iowait: time spent waiting for I/O
            - irq: time spent handling interrupts
            - softirq: time spent handling soft interrupts
            - steal: time stolen by other operating systems
            - guest: time spent running virtual CPUs
            - guest_nice: time spent running virtual CPUs with low priority
        """
        stats = psutil.cpu_times()
        return {
            'user': stats.user,
            'nice': getattr(stats, 'nice', 0.0),
            'system': stats.system,
            'idle': stats.idle,
            'iowait': getattr(stats, 'iowait', 0.0),
            'irq': getattr(stats, 'irq', 0.0),
            'softirq': getattr(stats, 'softirq', 0.0),
            'steal': getattr(stats, 'steal', 0.0),
            'guest': getattr(stats, 'guest', 0.0),
            'guest_nice': getattr(stats, 'guest_nice', 0.0),
        }
    
    def get_cpu_freq(self) -> Dict[str, float]:
        """
        Get CPU frequency information.
        
        Returns:
            Dictionary containing:
            - current: current CPU frequency in MHz
            - min: minimum CPU frequency in MHz
            - max: maximum CPU frequency in MHz
        """
        freq = psutil.cpu_freq()
        if freq is None:
            return {'current': 0.0, 'min': 0.0, 'max': 0.0}
        
        return {
            'current': freq.current,
            'min': freq.min,
            'max': freq.max,
        }
    
    def get_cpu_freq_per_core(self) -> List[Dict[str, float]]:
        """
        Get CPU frequency information for each core.
        
        Returns:
            List of dictionaries containing frequency info for each core
        """
        freqs = psutil.cpu_freq(percpu=True)
        if freqs is None:
            return [{'current': 0.0, 'min': 0.0, 'max': 0.0} for _ in range(self.cpu_count)]
        
        return [
            {
                'current': freq.current if freq else 0.0,
                'min': freq.min if freq else 0.0,
                'max': freq.max if freq else 0.0,
            }
            for freq in freqs
        ]
    
    def get_cpu_load_average(self) -> Optional[Tuple[float, float, float]]:
        """
        Get system load average.
        
        Returns:
            Tuple of (1min, 5min, 15min) load averages, or None if not available
        """
        return psutil.getloadavg()
    
    def get_cpu_info(self) -> Dict:
        """
        Get comprehensive CPU information.
        
        Returns:
            Dictionary containing all CPU information
        """
        return {
            'cpu_count_logical': self.cpu_count,
            'cpu_count_physical': self.cpu_count_physical,
            'cpu_percent': self.get_cpu_percent(),
            'cpu_percent_per_core': self.get_cpu_percent_per_core(),
            'cpu_stats': self.get_cpu_stats(),
            'cpu_freq': self.get_cpu_freq(),
            'cpu_freq_per_core': self.get_cpu_freq_per_core(),
            'load_average': self.get_cpu_load_average(),
        }
    
    def monitor_continuous(self, interval: float = 1.0, duration: Optional[float] = None, 
                          callback: Optional[callable] = None) -> List[Dict]:
        """
        Continuously monitor CPU usage.
        
        Args:
            interval: Time interval between measurements in seconds
            duration: Total duration to monitor in seconds (None for infinite)
            callback: Optional callback function to call with each measurement
            
        Returns:
            List of CPU usage measurements
        """
        measurements = []
        start_time = time.time()
        
        while True:
            if duration and (time.time() - start_time) >= duration:
                break
            
            measurement = {
                'timestamp': time.time(),
                'cpu_percent': self.get_cpu_percent(interval=interval),
                'cpu_percent_per_core': self.get_cpu_percent_per_core(interval=0.1),
            }
            
            measurements.append(measurement)
            
            if callback:
                callback(measurement)
            
            time.sleep(interval)
        
        return measurements

