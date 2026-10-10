"""
Log size checks and automatic log cleanup.
"""

from typing import Any, Optional

from core.format import format_bytes
from core.observability import Component, get_logger, safe_log
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.timeout import run_with_timeout


PRESSURE_CHECK_INTERVAL = 5
LogInfo = dict[str, Any]


def _largest_per_path(logs: list[LogInfo]) -> list[LogInfo]:
    """Deduplicate log entries by path, keeping the largest reading of each."""
    by_path: dict[str, LogInfo] = {}
    for info in logs:
        path = info.get('path', '')
        if path and (path not in by_path or info.get('size_bytes', 0) > by_path[path].get('size_bytes', 0)):
            by_path[path] = info
    return list(by_path.values())


class LogCleanup(Handler):
    """Trims or empties oversized logs and reports what was cleaned."""

    log = get_logger(Component.LOGS)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.log_check_counter = 0
        self.last_log_alert_time: dict[str, float] = {}
        self.cleaned_logs = set()

    def check(self, snap: Snapshot) -> None:
        """Check log sizes every few cycles (more often while the disk is filling) and clean."""
        cfg = self.cfg
        self.log_check_counter += 1
        root = next((p for p in snap.all_partitions if p.get('mountpoint') == '/'), None)
        root_percent = root['percent'] if root else snap.disk_percent
        interval = cfg.log_check_interval if root_percent < cfg.disk_cleanup_threshold else PRESSURE_CHECK_INTERVAL
        if self.log_check_counter < interval:
            return

        self.log_check_counter = 0
        try:
            self._check_logs(snap, aggressive=root_percent >= cfg.disk_aggressive_cleanup_threshold)
        except Exception as e:
            safe_log(self.log.debug, f"Log size check failed: {e}")
        self.ctx.heartbeat()

    def _check_logs(self, snap: Snapshot, aggressive: bool) -> None:
        log_monitor = self.ctx.log_monitor
        rotated = run_with_timeout(
            lambda: log_monitor.cleanup_rotated_logs(force=aggressive),
            timeout=60.0,
            default_value={'files_removed': 0, 'total_size_cleared': 0},
            operation_name="Rotated log cleanup",
        )
        if rotated.get('files_removed', 0) > 0:
            self.log.warning(
                f"[CLEANUP] Removed {rotated['files_removed']} rotated log archive(s), "
                f"freed {format_bytes(rotated['total_size_cleared'])}"
            )

        sizes = run_with_timeout(
            log_monitor.get_all_log_sizes, timeout=10.0, default_value={}, operation_name="Get all log sizes"
        )
        if aggressive:
            logs_to_check = self._all_logs(sizes)
            safe_log(self.log.info, f"[LOG CHECK] Disk at {snap.disk_percent:.1f}% - cleaning ALL logs proactively")
        else:
            logs_to_check = sizes.get('exceeded_logs', [])

        cleaned_batch = []
        for log_info in logs_to_check:
            cleaned = self._clean_one(log_info.get('path', 'Unknown'), snap, aggressive)
            if cleaned is not None:
                cleaned_batch.append(cleaned)

        self._report(cleaned_batch, sizes.get('threshold_formatted', 'N/A'))
        # A log that dropped back under the threshold may be cleaned again if it regrows.
        still_exceeded = {log.get('path') for log in logs_to_check}
        self.cleaned_logs = {path for path in self.cleaned_logs if path not in still_exceeded}

    def _all_logs(self, sizes: dict[str, Any]) -> list[LogInfo]:
        """Every log under /var/log plus the monitored ones, for aggressive cleanup."""
        files = run_with_timeout(
            lambda: self.ctx.log_monitor.get_all_log_files_in_directory('/var/log'),
            timeout=30.0,
            default_value=[],
            operation_name="Get all log files in directory",
        )
        monitored = []
        if sizes.get('application_log'):
            monitored.append(sizes['application_log'])
        monitored.extend(sizes.get('system_logs', []))
        if sizes.get('journal_log'):
            monitored.append(sizes['journal_log'])
        return _largest_per_path(files + monitored)

    def _log_size(self, path: str, operation_name: str) -> Optional[LogInfo]:
        return run_with_timeout(
            lambda: self.ctx.log_monitor.get_log_size(path),
            timeout=5.0,
            default_value=None,
            operation_name=operation_name,
        )

    def _clean_one(self, path: str, snap: Snapshot, aggressive: bool) -> Optional[LogInfo]:
        """Clean one log if it is due. Returns its post-cleanup info for the report, or None."""
        log_monitor = self.ctx.log_monitor
        now = snap.current_time
        if path in self.cleaned_logs:
            return None
        current = self._log_size(path, f"Get log size for {path}")
        if not current:
            return None

        size = current.get('size_bytes', 0)
        threshold = log_monitor.threshold_bytes
        if not aggressive and size <= threshold:
            safe_log(self.log.debug, f"Skipping {path}: {format_bytes(size)} <= {format_bytes(threshold)}")
            return None
        if now - self.last_log_alert_time.get(path, 0) < self.cfg.alert_cooldown:
            return None

        if aggressive:
            safe_log(
                self.log.info, f"    Proactive cleanup (disk {snap.disk_percent:.1f}%): {path} ({format_bytes(size)})"
            )
        else:
            safe_log(self.log.info, f"    Cleaning {path}: {format_bytes(size)} > {format_bytes(threshold)}")
        cleaned = run_with_timeout(
            lambda: log_monitor.cleanup_log(current, force=aggressive),
            timeout=30.0,
            default_value=False,
            operation_name=f"Cleanup log {path}",
        )
        self.last_log_alert_time[path] = now
        if not cleaned:
            safe_log(self.log.error, f"    Log cleanup failed for {path} - check permissions (may need root/sudo)")
            return None

        safe_log(self.log.info, f"    Log cleanup successful for {path} (was {format_bytes(size)})")
        self.cleaned_logs.add(path)
        after = self._log_size(path, f"Get log size after cleanup for {path}")
        if after:
            safe_log(self.log.info, f"    Log size after cleanup: {after.get('size_formatted', 'N/A')}")
            result = after
        else:
            safe_log(self.log.warning, f"    Could not get log size after cleanup for {path}")
            result = current
        result['original_size_bytes'] = size
        result['original_size_formatted'] = format_bytes(size)
        return result

    def _report(self, cleaned_batch: list[LogInfo], threshold_formatted: str) -> None:
        ctx = self.ctx
        if not cleaned_batch:
            return
        count = len(cleaned_batch)
        if not ctx.webhook:
            self.log.warning(f"{count} log file(s) exceeded threshold and were auto-cleaned (no webhook configured)")
            return
        self.log.warning(f"[ALERT] {count} log file(s) exceeded threshold and were auto-cleaned")
        if ctx.deliver(
            ctx.webhook,
            'send_batched_log_cleanup_alert',
            "Batched log cleanup alert webhook",
            cleaned_logs=cleaned_batch,
            threshold_formatted=threshold_formatted,
        ):
            self.log.info(f"    Discord batched log cleanup alert sent for {count} log(s)")
        else:
            self.log.warning("    Discord batched log cleanup alert failed")
