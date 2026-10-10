"""
Base class for monitoring handlers.
"""

from typing import Any, Callable, Optional

from core.observability import Component, get_logger
from core.runtime.context import MonitorContext


class Handler:
    """A unit of per-cycle monitoring work that owns its own state.

    Subclasses override `log` to log under their own 'ServerMonitor.<Component>' name.
    """

    log = get_logger(Component.CORE)

    def __init__(self, ctx: MonitorContext):
        self.ctx = ctx
        self.cfg = ctx.cfg

    def raise_alert(
        self,
        gate_key: str,
        now: float,
        message: str,
        send: Callable[[], bool],
        *,
        webhook: Optional[Any],
        cooldown_label: Optional[str] = None,
        alert_type: Optional[str] = None,
        record_result: bool = False,
        sent_message: str = "    Discord alert sent",
        failed_message: str = "    Discord alert failed",
    ) -> bool:
        """
        Run the standard alert flow for a condition that is currently true.

        The alert gate decides whether this occurrence may notify. When it may, the message
        is logged as "[ALERT] ..." and `send` is called if `webhook` is configured, or the
        message is logged plainly otherwise. Either way the gate records the alert.

        Args:
            gate_key: AlertGate key for this condition
            now: Current time
            message: Log line describing the condition (without the "[ALERT] " prefix)
            send: Zero-argument callable that delivers the alert and returns success
            webhook: Destination; when None only the log line is written
            cooldown_label: When set, a suppressed alert logs "<label> cooldown: Ns" at debug
            alert_type: When set, counted in the metrics report
            record_result: Count the webhook outcome in the metrics report
            sent_message: Logged after a successful send
            failed_message: Logged after a failed send

        Returns:
            True when the alert went out (or was logged), False when it was suppressed
        """
        ctx = self.ctx
        gate = ctx.alert_gates.get(gate_key, now)
        if not gate.should_alert(now):
            if cooldown_label:
                self.log.debug(f"{cooldown_label} cooldown: {gate.seconds_until_next(now):.0f}s")
            return False

        if webhook:
            self.log.warning(f"[ALERT] {message}")
            if alert_type:
                ctx.metrics_collector.record_alert(alert_type)
            success = send()
            if record_result:
                ctx.metrics_collector.record_webhook_result(success)
            if success:
                self.log.info(sent_message)
            else:
                self.log.warning(failed_message)
        else:
            self.log.warning(message)
        gate.mark_sent(now)
        return True
