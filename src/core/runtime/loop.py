"""
The monitoring loop: one cycle collects a snapshot and runs every handler in a fixed order.
"""

import time
import traceback

from core.observability import Component, get_logger
from core.runtime.collect import Collector
from core.runtime.context import MonitorContext
from core.runtime.handlers.disk import DiskAlerts, DiskCleanup, PartitionMaintenance
from core.runtime.handlers.freeze import FreezeCheck
from core.runtime.handlers.kernel import KernelChecks
from core.runtime.handlers.logs import LogCleanup
from core.runtime.handlers.media import MediaCheck
from core.runtime.handlers.processes import ProcessAlerts
from core.runtime.handlers.resources import CpuAlert, GpuAlert, MemoryAlert, NetworkAlert, TemperatureAlert
from core.runtime.handlers.torrents import TorrentWatcher
from core.runtime.handlers.updates import SystemUpdateScheduler
from core.runtime.snapshot import Snapshot
from core.timeout import send_webhook

logger = get_logger(Component.CORE)


class MonitoringLoop:
    """Owns the handlers and runs them once per check interval until interrupted."""

    def __init__(self, ctx: MonitorContext, started_at: float):
        self.ctx = ctx
        self.cfg = ctx.cfg
        self.last_metrics_report_time = 0
        self.collector = Collector(ctx)
        self.disk_cleanup = DiskCleanup(ctx)
        self.cpu = CpuAlert(ctx)
        self.memory = MemoryAlert(ctx)
        self.gpu = GpuAlert(ctx)
        self.disk_alerts = DiskAlerts(ctx)
        self.partitions = PartitionMaintenance(ctx)
        self.temperature = TemperatureAlert(ctx)
        self.network = NetworkAlert(ctx)
        self.processes = ProcessAlerts(ctx)
        self.freeze = FreezeCheck(ctx)
        self.media = MediaCheck(ctx)
        self.torrents = TorrentWatcher(ctx)
        self.logs = LogCleanup(ctx)
        self.kernel = KernelChecks(ctx)
        self.updates = SystemUpdateScheduler(ctx, started_at)

    def run(self) -> None:
        """Run cycles forever; a failing cycle is logged and reported, never fatal."""
        ctx = self.ctx
        while True:
            try:
                self.run_cycle()
            except Exception as e:
                logger.error(f"Unhandled error in monitoring loop iteration: {e}")
                logger.error(f"Traceback: {traceback.format_exc()}")
                ctx.report_error(e, context="Monitoring loop iteration", include_traceback=True)
                logger.warning("Continuing monitoring despite error...")

                ctx.heartbeat()

            time.sleep(self.cfg.check_interval)

    def run_cycle(self) -> None:
        """Collect one snapshot and run every handler. The order is deliberate: it is the alert order."""
        ctx = self.ctx
        ctx.heartbeat()
        if ctx.watchdog_monitor:
            ctx.watchdog_monitor.reset_alert_state()

        snap = Snapshot(current_time=time.time())
        self._report_metrics(snap.current_time)
        self._freeze_heartbeat()

        self.collector.collect_core(snap)
        self.disk_cleanup.check(snap)
        self.collector.collect_peripherals(snap)
        self.collector.log_status_line(snap)

        self.cpu.check(snap)
        self.memory.check(snap)
        self.cpu.log_hot_cores(snap)
        self.gpu.check(snap)
        self.disk_alerts.alert_partitions(snap)
        self.disk_alerts.alert_total(snap)
        self.partitions.check(snap)
        self.temperature.check(snap)
        self.network.check(snap)
        self.processes.check(snap)
        self.freeze.check(snap)
        self.media.check(snap)
        self.torrents.check(snap)
        self.logs.check(snap)
        self.kernel.check_stability(snap)
        self.kernel.check_crashes(snap)
        self.updates.check(snap)

    def _report_metrics(self, current_time: float) -> None:
        """Send the periodic metrics summary."""
        cfg = self.cfg
        ctx = self.ctx
        if ctx.webhook and (current_time - self.last_metrics_report_time) >= cfg.metrics_report_interval:
            try:
                metrics_summary = ctx.metrics_collector.get_summary()
                success = send_webhook(
                    lambda metrics_summary=metrics_summary: ctx.webhook.send_metrics_report(metrics_summary),
                    operation_name="Metrics report webhook",
                )
                ctx.metrics_collector.record_webhook_result(success)
                if success:
                    logger.info("Metrics report sent to Discord")
                    self.last_metrics_report_time = current_time
                else:
                    logger.warning("Failed to send metrics report to Discord")
            except Exception as e:
                logger.error(f"Error sending metrics report: {e}")
                ctx.metrics_collector.record_error('metrics_report')

    def _freeze_heartbeat(self) -> None:
        cfg = self.cfg
        ctx = self.ctx
        try:
            if cfg.freeze_monitor_enabled and ctx.freeze_monitor:
                ctx.freeze_monitor.update_heartbeat()
        except Exception as e:
            logger.warning(f"Error updating freeze monitor heartbeat: {e}")
