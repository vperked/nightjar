"""
Disk handlers: automatic cleanup, per-partition and overall usage alerts.
"""

import contextlib
from pathlib import Path
from typing import Any

from core.config import (
    DISK_CLEANUP_COOLDOWN_HIGH,
    DISK_CLEANUP_COOLDOWN_LOW,
    DISK_CLEANUP_COOLDOWN_MEDIUM,
    DISK_CLEANUP_COOLDOWN_NORMAL,
    DISK_EMERGENCY_COOLDOWN,
    LOG_CLEANUP_INTERVAL_SECONDS,
    MAX_DIRECTORIES_DISPLAY,
)
from core.format import format_bytes
from core.observability import Component, get_logger, safe_log
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.timeout import run_with_timeout


TARGET_FREE_PERCENT = 20.0
_EMPTY_STATS = {'total_size_cleared': 0, 'files_removed': 0, 'dirs_removed': 0, 'logs_cleaned': 0}
_TIMED_OUT_STATS = {**_EMPTY_STATS, 'errors': ['cleanup timed out']}

Field = dict[str, Any]


def _field(name: str, value: str, inline: bool = True) -> Field:
    return {'name': name, 'value': value, 'inline': inline}


def _cleanup_counts(stats: dict[str, Any]) -> str:
    return f"({stats['files_removed']} files, {stats['dirs_removed']} dirs, {stats.get('logs_cleaned', 0)} logs)"


def _usage_fields(stats: dict[str, Any], fallback_percent: float) -> list[Field]:
    """Before/after usage, space cleared and files removed, shared by every cleanup embed."""
    return [
        _field('Usage Before', f"{stats.get('initial_usage', fallback_percent):.1f}%"),
        _field('Usage After', f"{stats.get('final_usage', fallback_percent):.1f}%"),
        _field('Space Cleared', format_bytes(stats['total_size_cleared'])),
        _field('Files Removed', str(stats['files_removed'])),
    ]


def _errors_field(stats: dict[str, Any], during: str) -> list[Field]:
    if not stats['errors']:
        return []
    return [_field('Errors', f"{len(stats['errors'])} error(s) occurred during {during}", inline=False)]


def _largest_directories_field(largest_dirs: list[dict[str, Any]], label: str) -> list[Field]:
    if not largest_dirs:
        return []
    lines = [f"{i}. `{Path(d['path']).name}`: {d['size']}" for i, d in enumerate(largest_dirs, 1)]
    return [_field(f'Largest Directories ({label})', '\n'.join(lines), inline=False)]


def _cleanup_cooldown(disk_percent: float) -> float:
    """Clean more often the fuller the disk is."""
    if disk_percent >= 80.0:
        return DISK_CLEANUP_COOLDOWN_HIGH
    if disk_percent >= 70.0:
        return DISK_CLEANUP_COOLDOWN_MEDIUM
    if disk_percent >= 60.0:
        return DISK_CLEANUP_COOLDOWN_LOW
    return DISK_CLEANUP_COOLDOWN_NORMAL


class DiskCleanup(Handler):
    """Runs log, emergency and automatic cleanups when overall disk usage is high."""

    log = get_logger(Component.DISK)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.last_disk_cleanup_time = 0

    def check(self, snap: Snapshot) -> None:
        """Pick and run the cleanup tier that matches the current disk usage."""
        cfg = self.cfg
        now = snap.current_time
        if snap.disk_percent >= cfg.disk_cleanup_threshold and self._since_cleanup(now) >= LOG_CLEANUP_INTERVAL_SECONDS:
            self._clean_logs(snap)

        if snap.disk_percent >= cfg.disk_emergency_threshold:
            if self._since_cleanup(now) >= DISK_EMERGENCY_COOLDOWN:
                self._emergency_cleanup(snap)
        elif snap.disk_percent >= cfg.disk_cleanup_threshold:
            cooldown = _cleanup_cooldown(snap.disk_percent)
            if self._since_cleanup(now) >= cooldown:
                self._routine_cleanup(snap)
            else:
                self.log.debug(f"Disk cleanup cooldown: {cooldown - self._since_cleanup(now):.0f}s")

    def _since_cleanup(self, now: float) -> float:
        return now - self.last_disk_cleanup_time

    def _clean_logs(self, snap: Snapshot) -> None:
        ctx = self.ctx
        self.log.warning(f"[CLEANUP] Disk at {snap.disk_percent:.1f}% - starting log cleanup...")
        try:
            stats = run_with_timeout(
                lambda: ctx.disk_monitor.cleanup_logs_and_restart_services(log_monitor=ctx.log_monitor),
                timeout=300.0,
                default_value={'logs_cleaned': 0, 'total_size_cleared': 0},
                operation_name="Log cleanup operation",
            )
            ctx.heartbeat()

            if stats.get('logs_cleaned', 0) > 0:
                self.log.warning(
                    f"[CLEANUP] Cleared {stats['logs_cleaned']} log file(s), "
                    f"freed {format_bytes(stats['total_size_cleared'])}"
                )
                final_disk = run_with_timeout(
                    lambda: ctx.disk_monitor.get_disk_usage('/'),
                    timeout=5.0,
                    default_value=None,
                    operation_name="Final disk usage check",
                )
                if final_disk:
                    self.log.info(f"[CLEANUP] Disk usage after log cleanup: {final_disk['percent']:.1f}%")
        except Exception as e:
            self.log.error(f"[CLEANUP] Error during log cleanup: {e}")
            ctx.report_error(e, context="Log cleanup operation", include_traceback=True)
        self.last_disk_cleanup_time = snap.current_time

    def _emergency_cleanup(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        threshold = cfg.disk_emergency_threshold
        safe_log(
            self.log.critical,
            f"[EMERGENCY] Disk usage at {snap.disk_percent:.1f}% (emergency threshold: {threshold:.1f}%) "
            f"- CRITICAL! Starting emergency cleanup...",
        )
        try:
            stats = run_with_timeout(
                lambda: ctx.disk_monitor.emergency_cleanup_disk_space(mountpoint='/', log_monitor=ctx.log_monitor),
                timeout=600.0,
                default_value={**_EMPTY_STATS, 'errors': []},
                operation_name="Emergency disk cleanup operation",
            )
            ctx.heartbeat()

            if stats['total_size_cleared'] > 0:
                self._report_emergency_cleanup(snap, stats)
            else:
                safe_log(self.log.critical, "[EMERGENCY] No space could be freed! Disk may crash if it reaches 100%!")
                if ctx.disk_webhook:
                    ctx.deliver(
                        ctx.disk_webhook,
                        'send_embed',
                        "Critical disk cleanup failed webhook",
                        title='CRITICAL: Disk Cleanup Failed',
                        description=f'Disk at {snap.disk_percent:.1f}% and emergency cleanup could not free space! '
                        f'Manual intervention required immediately!',
                        fields=[
                            _field('Current Usage', f"{snap.disk_percent:.1f}%", inline=False),
                            _field('Emergency Threshold', f"{threshold:.1f}%"),
                            _field('Status', 'CRITICAL - Manual intervention required', inline=False),
                        ],
                        footer='Server Monitor - Critical Alert',
                        timestamp=True,
                    )
        except Exception as e:
            safe_log(self.log.critical, f"[EMERGENCY] Error during emergency disk cleanup: {e}")
            with contextlib.suppress(Exception):
                ctx.report_error(e, context="Emergency disk cleanup operation", include_traceback=True)
        self.last_disk_cleanup_time = snap.current_time

    def _report_emergency_cleanup(self, snap: Snapshot, stats: dict[str, Any]) -> None:
        ctx = self.ctx
        threshold = self.cfg.disk_emergency_threshold
        still_full = 'Disk still critically full after cleanup! Manual intervention required!'
        safe_log(
            self.log.critical,
            f"[EMERGENCY] Emergency cleanup cleared {format_bytes(stats['total_size_cleared'])} {_cleanup_counts(stats)}",
        )
        if stats.get('final_usage'):
            safe_log(self.log.critical, f"[EMERGENCY] Disk usage after emergency cleanup: {stats['final_usage']:.1f}%")
            if stats['final_usage'] >= threshold:
                safe_log(self.log.critical, f"[EMERGENCY] {still_full}")

        if not ctx.disk_webhook:
            return
        fields = [
            _field('EMERGENCY CLEANUP', 'Disk critically full - aggressive cleanup performed', inline=False),
            *_usage_fields(stats, snap.disk_percent),
            _field('Logs Cleaned', str(stats.get('logs_cleaned', 0))),
        ]
        if stats.get('final_usage', snap.disk_percent) >= threshold:
            fields.append(_field('CRITICAL', still_full, inline=False))
        fields += _errors_field(stats, 'emergency cleanup')

        if ctx.deliver(
            ctx.disk_webhook,
            'send_embed',
            "Emergency cleanup webhook",
            title='EMERGENCY: Disk Critically Full',
            description='Emergency cleanup performed to prevent disk from reaching 100%',
            fields=fields,
            footer='Server Monitor - Emergency',
            timestamp=True,
        ):
            safe_log(self.log.info, "    Discord emergency notification sent")

    def _routine_cleanup(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        self.log.warning(
            f"[CLEANUP] Disk usage at {snap.disk_percent:.1f}% (cleanup threshold: {cfg.disk_cleanup_threshold:.1f}%), "
            f"starting automatic cleanup..."
        )
        try:
            stats = run_with_timeout(
                lambda: ctx.disk_monitor.cleanup_disk_space(
                    mountpoint='/', target_free_percent=TARGET_FREE_PERCENT, log_monitor=ctx.log_monitor
                ),
                timeout=300.0,
                default_value=dict(_EMPTY_STATS),
                operation_name="Disk cleanup operation",
            )
            if stats['total_size_cleared'] > 0:
                self.log.info(f"[CLEANUP] Cleaned {format_bytes(stats['total_size_cleared'])} {_cleanup_counts(stats)}")
                if stats.get('final_usage'):
                    self.log.info(f"[CLEANUP] Disk usage after cleanup: {stats['final_usage']:.1f}%")
                if ctx.disk_webhook:
                    fields = [
                        *_usage_fields(stats, snap.disk_percent),
                        _field('Directories Removed', str(stats['dirs_removed'])),
                        *_errors_field(stats, 'cleanup'),
                    ]
                    if ctx.deliver(
                        ctx.disk_webhook,
                        'send_embed',
                        "Disk cleanup webhook",
                        title='Disk Cleanup Performed',
                        description='Automatic disk cleanup completed to prevent disk from filling up',
                        fields=fields,
                        footer='Server Monitor',
                        timestamp=True,
                    ):
                        self.log.info("    Discord cleanup notification sent")
            else:
                self.log.info("[CLEANUP] No files were cleaned (disk may need manual intervention)")
        except Exception as e:
            self.log.error(f"[CLEANUP] Error during automatic disk cleanup: {e}")
            ctx.report_error(e, context="Automatic disk cleanup operation", include_traceback=True)
        self.last_disk_cleanup_time = snap.current_time


class DiskAlerts(Handler):
    """Alerts on partitions and on overall disk usage."""

    log = get_logger(Component.DISK)

    def alert_partitions(self, snap: Snapshot) -> None:
        """Alert for every partition at or above the disk threshold."""
        try:
            for partition in snap.all_partitions:
                if partition.get('percent', 0.0) >= self.cfg.disk_threshold:
                    self._alert_partition(partition, snap.current_time)
        except Exception as e:
            self.log.debug(f"Individual disk check failed: {e}")

    def _alert_partition(self, partition: dict[str, Any], now: float) -> None:
        cfg, ctx = self.cfg, self.ctx
        mountpoint = partition.get('mountpoint', partition.get('device', 'unknown'))
        percent = partition.get('percent', 0.0)
        gate = ctx.alert_gates.get(f'partition:{mountpoint}', now)
        if not gate.should_alert(now):
            self.log.debug(f"Disk {mountpoint} cooldown: {gate.seconds_until_next(now):.0f}s")
            return

        if ctx.disk_webhook:
            self.log.warning(f"[ALERT] Disk {mountpoint}: {percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
            fields = [
                _field('Mountpoint', mountpoint),
                _field('Usage', f'{percent:.1f}%'),
                _field('Threshold', f'{cfg.disk_threshold:.1f}%'),
                _field('Total Capacity', format_bytes(partition.get('total', 0)), inline=False),
                _field('Used', format_bytes(partition.get('used', 0))),
                _field('Free', format_bytes(partition.get('free', 0))),
                *_largest_directories_field(self._largest_directories(mountpoint), mountpoint),
            ]
            self._send_alert(
                fields,
                title=f'Disk Usage Alert: {mountpoint}',
                description=f'Disk usage on {mountpoint} has exceeded the threshold of {cfg.disk_threshold:.1f}%',
            )
        else:
            self.log.warning(f"Disk {mountpoint}: {percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
        gate.mark_sent(now)

    def alert_total(self, snap: Snapshot) -> None:
        """Alert on overall usage when no per-partition data is available."""
        cfg, ctx = self.cfg, self.ctx
        now = snap.current_time
        if snap.all_partitions or snap.disk_percent < cfg.disk_threshold:
            return
        gate = ctx.alert_gates.get('disk', now)
        if not gate.should_alert(now):
            self.log.debug(f"Disk cooldown: {gate.seconds_until_next(now):.0f}s")
            return

        if ctx.disk_webhook:
            self.log.warning(f"[ALERT] Disk: {snap.disk_percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
            largest_dirs = []
            try:
                root_partition = ctx.disk_monitor.get_disk_usage('/')
                if root_partition and root_partition['percent'] >= cfg.disk_threshold:
                    largest_dirs = ctx.disk_monitor.get_largest_directories_human('/', limit=MAX_DIRECTORIES_DISPLAY)
            except Exception as e:
                self.log.debug(f"Largest directories failed: {e}")
            fields = [
                _field('Overall Usage', f'{snap.disk_percent:.1f}%'),
                _field('Threshold', f'{cfg.disk_threshold:.1f}%'),
                _field('Total Capacity', format_bytes(snap.total_disk.get('total', 0)), inline=False),
                _field('Used', format_bytes(snap.total_disk.get('used', 0))),
                _field('Free', format_bytes(snap.total_disk.get('free', 0))),
                *_largest_directories_field(largest_dirs, 'root'),
            ]
            self._send_alert(
                fields,
                title='Disk Usage Alert',
                description=f'Disk usage has exceeded the threshold of {cfg.disk_threshold:.1f}%',
            )
        else:
            self.log.warning(f"Disk: {snap.disk_percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
        gate.mark_sent(now)

    def _largest_directories(self, mountpoint: str) -> list[dict[str, Any]]:
        try:
            return self.ctx.disk_monitor.get_largest_directories_human(mountpoint, limit=MAX_DIRECTORIES_DISPLAY)
        except Exception as e:
            self.log.debug(f"Largest directories for {mountpoint} failed: {e}")
            return []

    def _send_alert(self, fields: list[Field], title: str, description: str) -> None:
        ctx = self.ctx
        success = ctx.deliver(
            ctx.disk_webhook,
            'send_embed',
            "disk_webhook send_embed",
            title=title,
            description=description,
            fields=fields,
            footer='Server Monitor',
            timestamp=True,
        )
        if success:
            self.log.info("    Discord alert sent")
        else:
            self.log.warning("    Discord alert failed")


class PartitionMaintenance(Handler):
    """Per-partition emergency cleanup, automatic cleanup and early warnings."""

    log = get_logger(Component.DISK)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.last_partition_cleanup_times = {}

    def check(self, snap: Snapshot) -> None:
        """Clean up and warn for each partition according to its own usage."""
        if not self.cfg.monitor_individual_partitions:
            return
        try:
            for partition in snap.all_partitions:
                self._maintain(partition, snap.current_time)
            for partition in snap.all_partitions:
                if partition.get('is_nvme'):
                    self._nvme_early_warning(partition, snap.current_time)
        except Exception as e:
            self.log.debug(f"Individual partition check failed: {e}")

    def _maintain(self, partition: dict[str, Any], now: float) -> None:
        cfg = self.cfg
        key = partition.get('mountpoint', partition.get('device', 'unknown'))
        percent = partition['percent']
        since_cleanup = now - self.last_partition_cleanup_times.get(key, 0)

        if percent >= cfg.disk_emergency_threshold:
            if since_cleanup >= DISK_EMERGENCY_COOLDOWN:
                self._emergency_cleanup(partition, key, now)
        elif percent >= cfg.disk_cleanup_threshold and since_cleanup >= DISK_CLEANUP_COOLDOWN_NORMAL:
            self._routine_cleanup(partition, key, now)

        if percent >= cfg.disk_threshold:
            self._threshold_alert(partition, key, now)
        elif percent >= cfg.disk_early_warning_threshold:
            self._early_warning(partition, key, now)

    def _run_cleanup(self, operation_name: str, cleanup) -> dict[str, Any]:
        self.ctx.heartbeat()
        stats = run_with_timeout(
            cleanup, timeout=300.0, default_value=dict(_TIMED_OUT_STATS), operation_name=operation_name
        ) or dict(_TIMED_OUT_STATS)
        self.ctx.heartbeat()
        return stats

    def _emergency_cleanup(self, partition: dict[str, Any], key: str, now: float) -> None:
        cfg, ctx = self.cfg, self.ctx
        percent = partition['percent']
        mountpoint = partition.get('mountpoint', '/')
        self.log.critical(
            f"[EMERGENCY] Partition {key}: {percent:.1f}% (emergency threshold: {cfg.disk_emergency_threshold:.1f}%) "
            f"- CRITICAL! Starting emergency cleanup..."
        )
        try:
            stats = self._run_cleanup(
                f"Emergency cleanup for {key}",
                lambda: ctx.disk_monitor.emergency_cleanup_disk_space(
                    mountpoint=mountpoint, log_monitor=ctx.log_monitor
                ),
            )
            if stats['total_size_cleared'] > 0:
                self.log.critical(
                    f"[EMERGENCY] Partition {key}: Emergency cleanup cleared "
                    f"{format_bytes(stats['total_size_cleared'])} {_cleanup_counts(stats)}"
                )
                if stats.get('final_usage'):
                    self.log.critical(
                        f"[EMERGENCY] Partition {key} usage after emergency cleanup: {stats['final_usage']:.1f}%"
                    )
                if ctx.disk_webhook:
                    fields = [
                        _field('EMERGENCY CLEANUP', f'Partition {key} critically full', inline=False),
                        *_usage_fields(stats, percent),
                        _field('Logs Cleaned', str(stats.get('logs_cleaned', 0))),
                    ]
                    if stats.get('final_usage', percent) >= cfg.disk_emergency_threshold:
                        fields.append(
                            _field(
                                'CRITICAL',
                                'Partition still critically full after cleanup! Manual intervention required!',
                                inline=False,
                            )
                        )
                    if ctx.deliver(
                        ctx.disk_webhook,
                        'send_embed',
                        "disk_webhook send_embed",
                        title='EMERGENCY: Partition Critically Full',
                        description=f'Emergency cleanup performed for partition {key}',
                        fields=fields,
                        footer='Server Monitor - Emergency',
                        timestamp=True,
                    ):
                        self.log.info(f"    Discord emergency notification sent for {key}")
            else:
                self.log.critical(f"[EMERGENCY] Partition {key}: No space could be freed!")
        except Exception as e:
            self.log.critical(f"[EMERGENCY] Error during emergency cleanup for partition {key}: {e}")
        self.last_partition_cleanup_times[key] = now

    def _routine_cleanup(self, partition: dict[str, Any], key: str, now: float) -> None:
        cfg, ctx = self.cfg, self.ctx
        percent = partition['percent']
        mountpoint = partition.get('mountpoint', '/')
        self.log.warning(
            f"[CLEANUP] Partition {key}: {percent:.1f}% (cleanup threshold: {cfg.disk_cleanup_threshold:.1f}%), "
            f"starting automatic cleanup..."
        )
        try:
            stats = self._run_cleanup(
                f"Disk cleanup for {key}",
                lambda: ctx.disk_monitor.cleanup_disk_space(
                    mountpoint=mountpoint, target_free_percent=TARGET_FREE_PERCENT, log_monitor=ctx.log_monitor
                ),
            )
            if stats['total_size_cleared'] > 0:
                self.log.info(
                    f"[CLEANUP] Partition {key}: Cleaned {format_bytes(stats['total_size_cleared'])} "
                    f"{_cleanup_counts(stats)}"
                )
                if stats.get('final_usage'):
                    self.log.info(f"[CLEANUP] Partition {key} usage after cleanup: {stats['final_usage']:.1f}%")
                if ctx.disk_webhook:
                    fields = [
                        _field('Partition', key, inline=False),
                        *_usage_fields(stats, percent),
                        _field('Directories Removed', str(stats['dirs_removed'])),
                        *_errors_field(stats, 'cleanup'),
                    ]
                    if ctx.deliver(
                        ctx.disk_webhook,
                        'send_embed',
                        "disk_webhook send_embed",
                        title='Partition Cleanup Performed',
                        description=f'Automatic cleanup completed for partition {key}',
                        fields=fields,
                        footer='Server Monitor',
                        timestamp=True,
                    ):
                        self.log.info(f"    Discord partition cleanup notification sent for {key}")
            else:
                self.log.info(f"[CLEANUP] Partition {key}: No files were cleaned")
        except Exception as e:
            self.log.error(f"[CLEANUP] Error during automatic cleanup for partition {key}: {e}")
        self.last_partition_cleanup_times[key] = now

    def _threshold_alert(self, partition: dict[str, Any], key: str, now: float) -> None:
        # Shares its gate with DiskAlerts.alert_partitions, which runs first in the same cycle,
        # so in practice this only logs the cooldown; it alerts if that handler failed first.
        cfg, ctx = self.cfg, self.ctx
        percent = partition['percent']
        gate = ctx.alert_gates.get(f'partition:{key}', now)
        if not gate.should_alert(now):
            self.log.debug(f"Partition {key} cooldown: {gate.seconds_until_next(now):.0f}s")
            return
        if ctx.disk_webhook:
            self.log.warning(f"[ALERT] Partition {key}: {percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
            if ctx.deliver(
                ctx.disk_webhook,
                'send_partition_alert',
                "disk_webhook send_partition_alert",
                partition=partition,
                threshold=cfg.disk_threshold,
                is_early_warning=False,
            ):
                self.log.info(f"    Discord partition alert sent for {key}")
            else:
                self.log.warning(f"    Discord partition alert failed for {key}")
        else:
            self.log.warning(f"Partition {key}: {percent:.1f}% (threshold: {cfg.disk_threshold:.1f}%)")
        gate.mark_sent(now)

    def _early_warning(self, partition: dict[str, Any], key: str, now: float) -> None:
        self._send_early_warning(
            partition, f'partition-early:{key}', f"Partition {key}", "partition early warning", key, now
        )

    def _nvme_early_warning(self, partition: dict[str, Any], now: float) -> None:
        if partition['percent'] < self.cfg.disk_early_warning_threshold:
            return
        key = partition.get('mountpoint', partition.get('device', 'unknown'))
        self._send_early_warning(partition, f'nvme:{key}', f"NVMe {key}", "NVMe early warning", key, now)

    def _send_early_warning(
        self, partition: dict[str, Any], gate_key: str, label: str, kind: str, key: str, now: float
    ) -> None:
        cfg, ctx = self.cfg, self.ctx
        gate = ctx.alert_gates.get(gate_key, now)
        if not gate.should_alert(now):
            return
        if ctx.disk_webhook:
            self.log.warning(
                f"[WARNING] {label}: {partition['percent']:.1f}% "
                f"(early warning: {cfg.disk_early_warning_threshold:.1f}%)"
            )
            if ctx.deliver(
                ctx.disk_webhook,
                'send_partition_alert',
                "disk_webhook send_partition_alert",
                partition=partition,
                threshold=cfg.disk_early_warning_threshold,
                is_early_warning=True,
            ):
                self.log.info(f"    Discord {kind} sent for {key}")
            else:
                self.log.warning(f"    Discord {kind} failed for {key}")
        gate.mark_sent(now)
