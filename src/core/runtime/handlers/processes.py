"""
Alerts for individual processes using too much CPU or memory.
"""

from typing import Any

import psutil

from core.config import MAX_PROCESSES_DISPLAY
from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot


PROCESS_CHECK_INTERVAL = 3


class ProcessAlerts(Handler):
    """Every few cycles, alert on the top processes that exceed their thresholds."""

    log = get_logger(Component.PROCESSES)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.process_check_counter = 0

    def check(self, snap: Snapshot) -> None:
        """Scan the top CPU and memory processes and alert on offenders."""
        cfg = self.cfg
        self.process_check_counter += 1
        if self.process_check_counter < PROCESS_CHECK_INTERVAL:
            return
        self.process_check_counter = 0
        try:
            snap.top_cpu_processes, snap.top_memory_processes = self.ctx.process_monitor.get_top_cpu_and_memory(
                limit=MAX_PROCESSES_DISPLAY
            )
            self._alert(snap.top_cpu_processes, 'cpu', 'CPU', cfg.process_cpu_threshold, snap.current_time)
            self._alert(snap.top_memory_processes, 'memory', 'Memory', cfg.process_memory_threshold, snap.current_time)
        except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError) as e:
            self.log.debug(f"Process access failed: {e}")
        except Exception as e:
            self.log.debug(f"Process check failed: {e}")

    def _alert(self, processes: list[dict[str, Any]], resource: str, label: str, threshold: float, now: float) -> None:
        ctx = self.ctx
        for proc in processes:
            usage = proc.get(f'{resource}_percent', 0)
            if usage < threshold:
                continue
            pid = proc.get('pid')
            self.raise_alert(
                f'proc:{resource}_{pid}',
                now,
                f"Process {proc.get('name', 'Unknown')} (PID: {pid}): {label} {usage:.1f}% (threshold: {threshold:.1f}%)",
                lambda proc=proc: ctx.deliver(
                    ctx.webhook,
                    'send_process_alert',
                    "webhook send_process_alert",
                    process_info=proc,
                    resource_type=resource,
                    threshold=threshold,
                ),
                webhook=ctx.webhook,
            )
