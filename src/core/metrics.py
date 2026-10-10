"""
Aggregation of monitor readings into metric snapshots.
"""

import time
from typing import Optional, Any
from collections import defaultdict, deque
from core.format import format_duration
from core.observability import Component, get_logger

logger = get_logger(Component.METRICS)


class MetricsCollector:
    """Collects and aggregates metrics for the server monitor."""

    def __init__(self, max_history_size: int = 1000):
        """
        Initialize the metrics collector.

        Args:
            max_history_size: Maximum number of historical entries to keep
        """
        self.max_history_size = max_history_size
        self.start_time = time.time()

        self.alert_counts: dict[str, int] = defaultdict(int)
        self.execution_times: dict[str, deque] = defaultdict(lambda: deque(maxlen=max_history_size))
        self.error_counts: dict[str, int] = defaultdict(int)
        self.webhook_stats = {'success': 0, 'failure': 0}
        self.last_execution_times: dict[str, float] = {}
        self.alert_timestamps: dict[str, deque] = defaultdict(deque)

    def record_alert(self, alert_type: str) -> None:
        """
        Record an alert occurrence.

        Args:
            alert_type: Type of alert (e.g., 'cpu', 'memory', 'disk', 'temperature')
        """
        now = time.time()
        self.alert_counts[alert_type] += 1
        stamps = self.alert_timestamps[alert_type]
        stamps.append(now)
        cutoff = now - 3600
        while stamps and stamps[0] <= cutoff:
            stamps.popleft()

    def start_monitor_execution(self, monitor_name: str) -> None:
        """
        Mark the start of a monitor execution.

        Args:
            monitor_name: Name of the monitor (e.g., 'cpu', 'memory', 'disk')
        """
        self.last_execution_times[monitor_name] = time.time()

    def end_monitor_execution(self, monitor_name: str) -> None:
        """
        Mark the end of a monitor execution and record duration.

        Args:
            monitor_name: Name of the monitor
        """
        if monitor_name in self.last_execution_times:
            duration = time.time() - self.last_execution_times[monitor_name]
            self.execution_times[monitor_name].append(duration)
            del self.last_execution_times[monitor_name]

    def record_error(self, error_type: str) -> None:
        """
        Record an error occurrence.

        Args:
            error_type: Type of error (e.g., 'monitor_init', 'webhook_failure', 'timeout')
        """
        self.error_counts[error_type] += 1

    def record_webhook_result(self, success: bool) -> None:
        """
        Record a webhook send result.

        Args:
            success: True if webhook send was successful, False otherwise
        """
        if success:
            self.webhook_stats['success'] += 1
        else:
            self.webhook_stats['failure'] += 1

    def get_alert_frequency(self, alert_type: Optional[str] = None, window_seconds: int = 3600) -> dict[str, float]:
        """
        Get alert frequency (alerts per hour) for the specified window.

        Args:
            alert_type: Specific alert type to check, or None for all
            window_seconds: Time window in seconds (default: 1 hour)

        Returns:
            Dictionary mapping alert types to alerts per hour
        """
        cutoff = time.time() - window_seconds
        frequencies = {}

        types_to_check = [alert_type] if alert_type else self.alert_timestamps.keys()

        for atype in types_to_check:
            if atype in self.alert_timestamps:
                recent_alerts = [ts for ts in self.alert_timestamps[atype] if ts > cutoff]
                count = len(recent_alerts)
                hours = window_seconds / 3600.0
                frequencies[atype] = count / hours if hours > 0 else 0.0
            else:
                frequencies[atype] = 0.0

        return frequencies

    def get_average_execution_time(self, monitor_name: Optional[str] = None) -> dict[str, float]:
        """
        Get average execution time for monitors.

        Args:
            monitor_name: Specific monitor to check, or None for all

        Returns:
            Dictionary mapping monitor names to average execution time in seconds
        """
        averages = {}
        monitors_to_check = [monitor_name] if monitor_name else self.execution_times.keys()

        for mname in monitors_to_check:
            if mname in self.execution_times and len(self.execution_times[mname]) > 0:
                durations = self.execution_times[mname]
                averages[mname] = sum(durations) / len(durations)
            else:
                averages[mname] = 0.0

        return averages

    def get_webhook_success_rate(self) -> float:
        """
        Get webhook success rate as a percentage.

        Returns:
            Success rate as a percentage (0-100)
        """
        total = self.webhook_stats['success'] + self.webhook_stats['failure']
        if total == 0:
            return 100.0
        return (self.webhook_stats['success'] / total) * 100.0

    def get_uptime_seconds(self) -> float:
        """
        Get total uptime in seconds.

        Returns:
            Uptime in seconds
        """
        return time.time() - self.start_time

    def get_summary(self) -> dict[str, Any]:
        """
        Get a summary of all metrics.

        Returns:
            Dictionary containing all metrics summary
        """
        total_alerts = sum(self.alert_counts.values())
        total_errors = sum(self.error_counts.values())
        total_webhook_calls = self.webhook_stats['success'] + self.webhook_stats['failure']

        uptime_seconds = self.get_uptime_seconds()
        return {
            'uptime_seconds': uptime_seconds,
            'uptime_formatted': format_duration(uptime_seconds),
            'total_alerts': total_alerts,
            'alert_counts': dict(self.alert_counts),
            'alert_frequency_per_hour': self.get_alert_frequency(),
            'total_errors': total_errors,
            'error_counts': dict(self.error_counts),
            'webhook_total_calls': total_webhook_calls,
            'webhook_success': self.webhook_stats['success'],
            'webhook_failure': self.webhook_stats['failure'],
            'webhook_success_rate': self.get_webhook_success_rate(),
            'monitor_execution_times': self.get_average_execution_time(),
        }

    def reset(self) -> None:
        """Reset all metrics (useful for testing or periodic resets)."""
        self.start_time = time.time()
        self.alert_counts.clear()
        self.execution_times.clear()
        self.error_counts.clear()
        self.webhook_stats = {'success': 0, 'failure': 0}
        self.last_execution_times.clear()
        self.alert_timestamps.clear()

__all__ = [
    'MetricsCollector',
]
