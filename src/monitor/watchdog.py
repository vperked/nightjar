"""
Monitoring watchdog module.
Detects if the monitoring loop has stopped and can automatically restart the service.
"""

import os
import time
import threading
import subprocess
import psutil
from typing import Optional, Dict, Any
from logger import get_logger
from util.systemd import is_systemd_available

logger = get_logger()


class WatchdogMonitor:
    """Monitor that detects if the main monitoring loop has stopped responding."""

    def __init__(
        self,
        timeout: float = 30.0,
        auto_restart: bool = False,
        restart_cooldown: float = 300.0,
        service_name: str = 'server-monitor',
        alert_cooldown: float = 300.0,
        webhook: Optional[Any] = None,
        crash_webhook: Optional[Any] = None
    ):
        """
        Initialize the watchdog monitor.

        Args:
            timeout: Seconds without heartbeat before watchdog triggers
            auto_restart: Whether to automatically restart service when watchdog triggers
            restart_cooldown: Seconds between restart attempts
            service_name: Systemd service name for restart
            alert_cooldown: Seconds between alerts for the same issue
            webhook: Discord webhook for notifications
            crash_webhook: Crash-specific webhook (takes precedence over webhook)
        """
        self.timeout = timeout
        self.auto_restart = auto_restart
        self.restart_cooldown = restart_cooldown
        self.service_name = service_name
        self.alert_cooldown = alert_cooldown
        self.webhook = webhook
        self.crash_webhook = crash_webhook


        self.heartbeat = {'last_update': time.time(), 'lock': threading.Lock()}


        self.running = {'value': True, 'lock': threading.Lock()}
        self.alert_state = {'alert_sent': False, 'last_alert_time': 0, 'lock': threading.Lock()}
        self.restart_state = {'last_restart_attempt': 0, 'restart_count': 0, 'lock': threading.Lock()}


        self.watchdog_thread: Optional[threading.Thread] = None

    def update_heartbeat(self) -> None:
        """Update the heartbeat timestamp (call this in the main monitoring loop)."""
        with self.heartbeat['lock']:
            self.heartbeat['last_update'] = time.time()

    def is_running_as_systemd_service(self) -> bool:
        """Check if the process is running as a systemd service."""
        if not is_systemd_available():
            return False


        if os.getenv('INVOCATION_ID'):
            return True


        try:
            parent_pid = os.getppid()
            if parent_pid > 0:
                parent_process = psutil.Process(parent_pid)
                parent_name = parent_process.name().lower()
                if 'systemd' in parent_name:
                    return True
        except (psutil.NoSuchProcess, psutil.AccessDenied):
            pass

        return False

    def attempt_service_restart(self) -> bool:
        """Attempt to restart the service using systemctl."""
        if not is_systemd_available():
            logger.warning("[WATCHDOG] Cannot restart: systemd not available")
            return False

        if not self.is_running_as_systemd_service():
            logger.warning("[WATCHDOG] Cannot restart: not running as systemd service")
            return False

        try:
            logger.info(f"[WATCHDOG] Attempting to restart service: {self.service_name}")
            result = subprocess.run(
                ['systemctl', 'restart', f'{self.service_name}.service'],
                capture_output=True,
                text=True,
                timeout=30,
                check=False
            )

            if result.returncode == 0:
                logger.info("[WATCHDOG] Service restart command executed successfully")
                return True
            else:
                logger.error(f"[WATCHDOG] Failed to restart service: {result.stderr}")
                return False
        except subprocess.TimeoutExpired:
            logger.error("[WATCHDOG] Service restart command timed out")
            return False
        except Exception as e:
            logger.error(f"[WATCHDOG] Error attempting service restart: {e}")
            return False

    def _watchdog_loop(self) -> None:
        """Watchdog thread that detects if monitoring loop has stopped."""
        while True:
            with self.running['lock']:
                if not self.running['value']:
                    break

            time.sleep(self.timeout)

            with self.heartbeat['lock']:
                time_since_heartbeat = time.time() - self.heartbeat['last_update']

            if time_since_heartbeat > self.timeout:
                current_time = time.time()

                with self.alert_state['lock']:
                    time_since_last_alert = current_time - self.alert_state['last_alert_time']
                    alert_sent = self.alert_state['alert_sent']

                if not alert_sent or time_since_last_alert >= self.alert_cooldown:
                    logger.critical(
                        f"[WATCHDOG] CRITICAL: Monitoring loop appears to have stopped! "
                        f"No heartbeat for {time_since_heartbeat:.1f}s (timeout: {self.timeout:.1f}s)"
                    )

                    target_webhook = self.crash_webhook if self.crash_webhook else self.webhook
                    restart_attempted = False
                    restart_success = False


                    if self.auto_restart:
                        with self.restart_state['lock']:
                            time_since_last_restart = current_time - self.restart_state['last_restart_attempt']

                            if time_since_last_restart >= self.restart_cooldown:
                                logger.info("[WATCHDOG] Auto-restart enabled, attempting to restart service...")
                                restart_attempted = True
                                restart_success = self.attempt_service_restart()

                                self.restart_state['last_restart_attempt'] = current_time
                                self.restart_state['restart_count'] += 1
                            else:
                                remaining_cooldown = self.restart_cooldown - time_since_last_restart
                                logger.warning(
                                    f"[WATCHDOG] Auto-restart on cooldown ({remaining_cooldown:.1f}s remaining)"
                                )

                    if target_webhook:
                        try:
                            fields = [
                                {
                                    'name': 'Time Since Last Heartbeat',
                                    'value': f"{time_since_heartbeat:.1f} seconds",
                                    'inline': True
                                },
                                {
                                    'name': 'Watchdog Timeout',
                                    'value': f"{self.timeout:.1f} seconds",
                                    'inline': True
                                },
                                {
                                    'name': 'Status',
                                    'value': 'CRITICAL - Monitoring may have crashed',
                                    'inline': False
                                },
                            ]

                            if restart_attempted:
                                restart_status = "SUCCESS" if restart_success else "FAILED"
                                fields.append({
                                    'name': 'Auto-Restart Attempt',
                                    'value': f"{restart_status} - Service restart {'succeeded' if restart_success else 'failed'}",
                                    'inline': False
                                })
                                if self.auto_restart:
                                    with self.restart_state['lock']:
                                        fields.append({
                                            'name': 'Total Restart Attempts',
                                            'value': str(self.restart_state['restart_count']),
                                            'inline': True
                                        })

                            success = target_webhook.send_embed(
                                title='CRITICAL: Monitoring Loop Stopped',
                                description=(
                                    f'Monitoring loop has stopped responding. '
                                    f'No heartbeat detected for {time_since_heartbeat:.1f} seconds.' +
                                    (f' Auto-restart {"succeeded" if restart_success else "failed"}.' if restart_attempted else '')
                                ),
                                fields=fields,
                                footer='Server Monitor - Watchdog Alert',
                                timestamp=True
                            )
                            if success:
                                webhook_name = "Crash webhook" if self.crash_webhook else "Discord"
                                logger.info(f"    Discord watchdog alert sent to {webhook_name}")
                        except Exception as e:
                            logger.error(f"Error sending watchdog alert: {e}")

                    with self.alert_state['lock']:
                        self.alert_state['alert_sent'] = True
                        self.alert_state['last_alert_time'] = current_time

    def start(self) -> None:
        """Start the watchdog thread."""
        if self.watchdog_thread is not None and self.watchdog_thread.is_alive():
            logger.warning("[WATCHDOG] Watchdog thread is already running")
            return

        with self.running['lock']:
            self.running['value'] = True

        self.watchdog_thread = threading.Thread(target=self._watchdog_loop, daemon=True)
        self.watchdog_thread.start()

        watchdog_info = f"Monitoring watchdog started (timeout: {self.timeout:.1f}s"
        if self.auto_restart:
            watchdog_info += f", auto-restart: ENABLED (cooldown: {self.restart_cooldown:.1f}s)"
        watchdog_info += ")"
        logger.info(watchdog_info)

    def stop(self) -> None:
        """Stop the watchdog thread."""
        with self.running['lock']:
            self.running['value'] = False

        if self.watchdog_thread and self.watchdog_thread.is_alive():
            self.watchdog_thread.join(timeout=5.0)
            if self.watchdog_thread.is_alive():
                logger.warning("[WATCHDOG] Watchdog thread did not stop within timeout")

    def get_restart_count(self) -> int:
        """Get the total number of restart attempts."""
        with self.restart_state['lock']:
            return self.restart_state['restart_count']

    def reset_alert_state(self) -> None:
        """Reset the alert state (call when monitoring resumes)."""
        with self.alert_state['lock']:
            if self.alert_state['alert_sent']:
                self.alert_state['alert_sent'] = False
