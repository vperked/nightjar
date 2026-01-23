"""
Alert handling and cooldown management.

Manages alert cooldowns to prevent spam and tracks alert state.
"""

import time
from typing import Dict, Optional


class AlertCooldownManager:
    """Manages cooldown periods for alerts to prevent spam."""

    def __init__(self, cooldown_seconds: float = 300.0):
        """
        Initialize alert cooldown manager.

        Args:
            cooldown_seconds: Default cooldown period in seconds (default: 300.0)
        """
        self.cooldown_seconds = cooldown_seconds
        self.last_alert_times: Dict[str, float] = {}
        self.alert_sent_flags: Dict[str, bool] = {}

    def can_alert(self, metric_key: str, custom_cooldown: Optional[float] = None) -> bool:
        """
        Check if an alert can be sent for a given metric.

        Args:
            metric_key: Unique identifier for the metric (e.g., 'cpu', 'memory')
            custom_cooldown: Optional custom cooldown period (overrides default)

        Returns:
            True if alert can be sent, False if still in cooldown
        """
        cooldown = custom_cooldown if custom_cooldown is not None else self.cooldown_seconds
        current_time = time.time()
        last_alert_time = self.last_alert_times.get(metric_key, 0.0)
        time_since_last_alert = current_time - last_alert_time

        return time_since_last_alert >= cooldown

    def record_alert(self, metric_key: str) -> None:
        """
        Record that an alert was sent for a metric.

        Args:
            metric_key: Unique identifier for the metric
        """
        self.last_alert_times[metric_key] = time.time()
        self.alert_sent_flags[metric_key] = True

    def get_remaining_cooldown(self, metric_key: str, custom_cooldown: Optional[float] = None) -> float:
        """
        Get remaining cooldown time for a metric.

        Args:
            metric_key: Unique identifier for the metric
            custom_cooldown: Optional custom cooldown period (overrides default)

        Returns:
            Remaining cooldown time in seconds (0 if cooldown has expired)
        """
        cooldown = custom_cooldown if custom_cooldown is not None else self.cooldown_seconds
        current_time = time.time()
        last_alert_time = self.last_alert_times.get(metric_key, 0.0)
        time_since_last_alert = current_time - last_alert_time
        remaining = max(0.0, cooldown - time_since_last_alert)
        return remaining

    def reset_alert_flag(self, metric_key: str) -> None:
        """
        Reset the alert sent flag for a metric (when value drops below threshold).

        Args:
            metric_key: Unique identifier for the metric
        """
        self.alert_sent_flags[metric_key] = False

    def is_alert_sent(self, metric_key: str) -> bool:
        """
        Check if an alert was recently sent for a metric.

        Args:
            metric_key: Unique identifier for the metric

        Returns:
            True if alert was sent and flag is still set
        """
        return self.alert_sent_flags.get(metric_key, False)
