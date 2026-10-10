import psutil
import time
import threading
from typing import Optional, Callable
from core.observability import Component, get_logger

logger = get_logger(Component.FREEZE)


class FreezeMonitor:
    """Monitor system for freeze detection by tracking responsiveness and metric updates."""

    def __init__(self, check_interval: float = 5.0):
        """
        Initialize the freeze monitor.

        Args:
            check_interval: Expected interval between monitoring checks in seconds
        """
        self.check_interval = check_interval
        self.last_heartbeat = time.time()
        self.last_metric_update = None
        self.last_cpu_value = None
        self.last_memory_value = None
        self.last_disk_io = None
        self.last_network_io = None
        self.monitoring_active = True
        self.freeze_threshold = 30.0
        self.heartbeat_timeout = 60.0
        self._watchdog_thread = None
        self._watchdog_running = False
        self._watchdog_lock = threading.Lock()

    def update_heartbeat(self):
        """Update the heartbeat timestamp (call this in the main monitoring loop)."""
        self.last_heartbeat = time.time()

    def update_metrics(self, cpu_percent: float, memory_percent: float,
                      disk_io: Optional[dict] = None, network_io: Optional[dict] = None):
        """
        Update metric values and check if they're changing.

        Args:
            cpu_percent: Current CPU usage percentage
            memory_percent: Current memory usage percentage
            disk_io: Optional disk I/O statistics
            network_io: Optional network I/O statistics
        """
        current_time = time.time()
        metrics_changed = False

        if self.last_cpu_value is None or abs(cpu_percent - self.last_cpu_value) > 0.1:
            metrics_changed = True

        if self.last_memory_value is None or abs(memory_percent - self.last_memory_value) > 0.1:
            metrics_changed = True

        self.last_cpu_value = cpu_percent
        self.last_memory_value = memory_percent

        if disk_io:
            current_disk_read = disk_io.get('read_bytes', 0)
            current_disk_write = disk_io.get('write_bytes', 0)
            if (self.last_disk_io is None or
                    current_disk_read != self.last_disk_io.get('read_bytes', 0) or
                    current_disk_write != self.last_disk_io.get('write_bytes', 0)):
                metrics_changed = True
            self.last_disk_io = {
                'read_bytes': current_disk_read,
                'write_bytes': current_disk_write
            }

        if network_io:
            current_net_sent = network_io.get('bytes_sent', 0)
            current_net_recv = network_io.get('bytes_recv', 0)
            if (self.last_network_io is None or
                    current_net_sent != self.last_network_io.get('bytes_sent', 0) or
                    current_net_recv != self.last_network_io.get('bytes_recv', 0)):
                metrics_changed = True
            self.last_network_io = {
                'bytes_sent': current_net_sent,
                'bytes_recv': current_net_recv
            }

        if metrics_changed or self.last_metric_update is None or cpu_percent < 15.0:
            self.last_metric_update = current_time

    def check_system_responsiveness(self, timeout: float = 2.0) -> dict:
        """
        Check if the system is responsive by attempting quick system calls.

        Args:
            timeout: Maximum time to wait for system calls in seconds

        Returns:
            Dictionary containing responsiveness information
        """
        start_time = time.time()
        responsive = True
        error_message = None

        try:
            test_start = time.time()
            psutil.cpu_percent(interval=0.0)
            cpu_time = time.time() - test_start

            mem_start = time.time()
            psutil.virtual_memory()
            mem_time = time.time() - mem_start

            total_time = time.time() - start_time

            if cpu_time > timeout or mem_time > timeout or total_time > timeout * 2:
                responsive = False
                error_message = f"System calls taking too long (CPU: {cpu_time:.2f}s, Mem: {mem_time:.2f}s)"

        except (OSError, psutil.Error, AttributeError) as e:
            responsive = False
            error_message = f"Exception during responsiveness check: {str(e)}"
            total_time = time.time() - start_time
        except Exception as e:
            responsive = False
            error_message = f"Unexpected error during responsiveness check: {str(e)}"
            total_time = time.time() - start_time

        return {
            'responsive': responsive,
            'check_time': time.time() - start_time,
            'error': error_message,
            'timestamp': time.time()
        }

    def check_freeze_status(self) -> dict:
        """
        Check if the system appears to be frozen.

        Returns:
            Dictionary containing freeze detection status
        """
        current_time = time.time()
        time_since_heartbeat = current_time - self.last_heartbeat
        time_since_metric_update = current_time - self.last_metric_update if self.last_metric_update else 0

        responsiveness = self.check_system_responsiveness()

        is_frozen = False
        freeze_reasons = []

        if time_since_heartbeat > self.heartbeat_timeout:
            is_frozen = True
            freeze_reasons.append(f"Heartbeat timeout ({time_since_heartbeat:.1f}s > {self.heartbeat_timeout:.1f}s)")

        if self.last_metric_update is not None and time_since_metric_update > self.freeze_threshold:
            is_frozen = True
            freeze_reasons.append(
                f"Metrics stalled ({time_since_metric_update:.1f}s > {self.freeze_threshold:.1f}s)"
            )

        if not responsiveness['responsive']:
            is_frozen = True
            if responsiveness['error']:
                freeze_reasons.append(f"System unresponsive: {responsiveness['error']}")
            else:
                freeze_reasons.append(f"System calls taking too long ({responsiveness['check_time']:.2f}s)")

        return {
            'is_frozen': is_frozen,
            'reasons': freeze_reasons,
            'time_since_heartbeat': time_since_heartbeat,
            'time_since_metric_update': time_since_metric_update,
            'responsive': responsiveness['responsive'],
            'responsiveness_check_time': responsiveness['check_time'],
            'timestamp': current_time
        }

    def set_freeze_threshold(self, threshold: float):
        """
        Set the freeze detection threshold.

        Args:
            threshold: Seconds without metric updates before considering frozen
        """
        self.freeze_threshold = max(10.0, threshold)

    def set_heartbeat_timeout(self, timeout: float):
        """
        Set the heartbeat timeout threshold.

        Args:
            timeout: Seconds without heartbeat before considering frozen
        """
        self.heartbeat_timeout = max(20.0, timeout)

    def get_freeze_info(self) -> dict:
        """
        Get comprehensive freeze detection information.

        Returns:
            Dictionary containing all freeze detection data
        """
        freeze_status = self.check_freeze_status()

        return {
            'freeze_status': freeze_status,
            'last_heartbeat': self.last_heartbeat,
            'last_metric_update': self.last_metric_update,
            'current_metrics': {
                'cpu_percent': self.last_cpu_value,
                'memory_percent': self.last_memory_value,
            },
            'thresholds': {
                'freeze_threshold': self.freeze_threshold,
                'heartbeat_timeout': self.heartbeat_timeout,
            }
        }

    def start_watchdog(self, callback: Optional[Callable[[dict], None]] = None):
        """
        Start a watchdog thread that monitors for freezes.

        Args:
            callback: Optional callback function to call when freeze is detected
        """
        with self._watchdog_lock:
            if self._watchdog_running:
                return

            self._watchdog_running = True

        def watchdog_loop():
            while True:
                with self._watchdog_lock:
                    if not self._watchdog_running or not self.monitoring_active:
                        break

                time.sleep(self.check_interval * 2)

                freeze_status = self.check_freeze_status()
                if freeze_status['is_frozen'] and callback:
                    try:
                        callback(freeze_status)
                    except (TypeError, ValueError) as e:
                        logger.debug(f"Freeze callback failed: {e}")
                    except Exception as e:
                        logger.warning(f"Unexpected error in freeze callback: {e}")

        self._watchdog_thread = threading.Thread(target=watchdog_loop, daemon=True)
        self._watchdog_thread.start()

    def stop_watchdog(self):
        """Stop the watchdog thread."""
        with self._watchdog_lock:
            self._watchdog_running = False
            self.monitoring_active = False
        if self._watchdog_thread:
            self._watchdog_thread.join(timeout=5.0)

