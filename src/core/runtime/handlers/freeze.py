"""
System freeze alerts.
"""

from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot


FREEZE_CHECK_INTERVAL = 1


class FreezeCheck(Handler):
    """Alert when the freeze monitor reports the system as unresponsive."""

    log = get_logger(Component.FREEZE)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.freeze_check_counter = 0

    def check(self, snap: Snapshot) -> None:
        """Ask the freeze monitor for its verdict and alert if frozen."""
        ctx = self.ctx
        if not (self.cfg.freeze_monitor_enabled and ctx.freeze_monitor):
            return
        self.freeze_check_counter += 1
        if self.freeze_check_counter < FREEZE_CHECK_INTERVAL:
            return
        self.freeze_check_counter = 0
        try:
            status = ctx.freeze_monitor.check_freeze_status()
            if status['is_frozen']:
                self.raise_alert(
                    'freeze',
                    snap.current_time,
                    f"System Freeze Detected: {', '.join(status['reasons'])}",
                    lambda: ctx.deliver(
                        ctx.webhook,
                        'send_freeze_alert',
                        "Freeze alert webhook",
                        freeze_status=status,
                        system_info=self._system_info(snap),
                    ),
                    webhook=ctx.webhook,
                    cooldown_label='Freeze',
                    sent_message="    Discord freeze alert sent",
                    failed_message="    Discord freeze alert failed",
                )
        except (ValueError, OSError) as e:
            self.log.debug(f"Freeze status check failed: {e}")
        except Exception as e:
            self.log.warning(f"Unexpected error checking freeze status: {e}")

    def _system_info(self, snap: Snapshot):
        try:
            return {
                'uptime': self.ctx.system_monitor.get_uptime(),
                'cpu': {'cpu_percent': snap.cpu_percent},
                'memory': {'memory_percent': snap.memory_percent},
            }
        except Exception:
            return None
