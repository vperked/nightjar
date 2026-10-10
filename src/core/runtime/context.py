"""
Shared state for one monitoring run: monitors, webhooks, settings, metrics and alert gates.
"""

import threading
import time
import traceback
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any, Optional

from core.alerts import AlertGates
from core.metrics import MetricsCollector
from core.observability import Component, get_logger
from core.observability.redaction import redact_secrets
from core.runtime.settings import RuntimeSettings
from core.timeout import send_webhook

logger = get_logger(Component.CORE)


@dataclass
class MonitorContext:
    """Everything the monitoring handlers share. Optional members are None when not configured."""

    cfg: RuntimeSettings
    cpu_monitor: Any
    memory_monitor: Any
    system_monitor: Any
    gpu_monitor: Any
    disk_monitor: Any
    network_monitor: Any
    sensors_monitor: Any
    process_monitor: Any
    log_monitor: Any
    kernel_monitor: Any
    qbittorrent_monitor: Optional[Any]
    webhook: Optional[Any]
    disk_webhook: Optional[Any]
    crash_webhook: Optional[Any]
    qbittorrent_webhook: Optional[Any]
    metrics_collector: MetricsCollector
    alert_gates: AlertGates
    watchdog_monitor: Optional[Any] = None
    freeze_monitor: Optional[Any] = None
    error_log_file: str = 'server_monitor_errors.log'
    ERROR_WEBHOOK_COOLDOWN: float = 300.0
    _error_log_lock: threading.Lock = field(default_factory=threading.Lock)
    _last_error_webhook_time: float = 0

    def deliver(self, webhook: Any, method: str, operation_name: str, *args: Any, **kwargs: Any) -> bool:
        """
        Call a webhook method under the standard send timeout.

        Arguments are bound now, so a slow send can never pick up a later value.

        Returns:
            True when Discord accepted the message
        """
        return send_webhook(lambda: getattr(webhook, method)(*args, **kwargs), operation_name=operation_name)

    def heartbeat(self) -> None:
        """Tell the watchdog the loop is still alive."""
        if self.watchdog_monitor:
            self.watchdog_monitor.update_heartbeat()

    def report_error(self, error: Exception, context: str = "", include_traceback: bool = True):
        """
        Log error to dedicated error log file and send webhook notification.

        Args:
            error: The exception that occurred
            context: Additional context about where the error occurred
            include_traceback: Whether to include full traceback in log
        """
        timestamp = datetime.now().strftime('%Y-%m-%d %H:%M:%S')
        error_message = str(error)
        error_type = type(error).__name__

        log_entry_parts = [f"[{timestamp}] {error_type}: {error_message}"]
        if context:
            log_entry_parts.append(f"Context: {context}")
        if include_traceback:
            log_entry_parts.append(f"Traceback:\n{traceback.format_exc()}")
        log_entry = redact_secrets("\n".join(log_entry_parts) + "\n" + ("=" * 80) + "\n")

        try:
            with self._error_log_lock, open(self.error_log_file, 'a', encoding='utf-8') as f:
                f.write(log_entry)
        except (OSError, PermissionError) as e:
            logger.warning(f"Could not write to error log file {self.error_log_file}: {e}")

        current_time = time.time()
        time_since_last_webhook = current_time - self._last_error_webhook_time

        target_webhook = self.crash_webhook if self.crash_webhook else self.webhook

        if target_webhook and time_since_last_webhook >= self.ERROR_WEBHOOK_COOLDOWN:
            try:
                traceback_text = traceback.format_exc() if include_traceback else ""
                if len(traceback_text) > 1000:
                    traceback_text = traceback_text[:1000] + "... (truncated)"

                fields = [
                    {'name': 'Error Type', 'value': error_type, 'inline': True},
                    {'name': 'Error Message', 'value': error_message[:500], 'inline': False},
                ]

                if context:
                    fields.append({'name': 'Context', 'value': context[:500], 'inline': False})

                if traceback_text:
                    fields.append({'name': 'Traceback', 'value': f"```\n{traceback_text}\n```", 'inline': False})

                success = send_webhook(
                    lambda: target_webhook.send_embed(
                        title='Monitoring Error Detected',
                        description='An error occurred in the monitoring system',
                        fields=fields,
                        color=0x000000,
                        footer='Server Monitor - Error Alert',
                        timestamp=True,
                    ),
                    operation_name="target_webhook send_embed",
                )

                if success:
                    self._last_error_webhook_time = current_time
                    webhook_name = "Crash webhook" if self.crash_webhook else "Discord"
                    logger.info(f"    Error notification sent to {webhook_name}")
            except Exception as e:
                logger.warning(f"Failed to send error notification to webhook: {e}")
