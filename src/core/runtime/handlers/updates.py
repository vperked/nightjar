"""
Background OS package updates on a fixed interval.
"""

import threading
import time

from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.updates import run_system_update



class SystemUpdateScheduler(Handler):
    """Starts the update job in a background thread once the interval has elapsed."""

    log = get_logger(Component.UPDATES)

    def __init__(self, ctx, started_at: float):
        super().__init__(ctx)
        self.last_run = started_at
        self.thread = None

    def _run_job(self) -> None:
        cfg = self.cfg
        try:
            r = run_system_update(apply=cfg.system_update_apply, command_timeout=cfg.system_update_timeout)
            if r.get('ok'):
                self.log.info(f"[SYSTEM_UPDATE] {r.get('message', 'ok')}")
            else:
                self.log.warning(f"[SYSTEM_UPDATE] {r.get('message', 'failed')}")
        except Exception as ex:
            self.log.warning(f"[SYSTEM_UPDATE] unexpected error: {ex}")
        finally:
            self.last_run = time.time()

    def check(self, snap: Snapshot) -> None:
        """Start an update run when enabled, idle and due."""
        if not self.cfg.system_update_enabled:
            return
        if self.thread is not None and self.thread.is_alive():
            return
        if (snap.current_time - self.last_run) >= self.cfg.system_update_interval_seconds:
            self.thread = threading.Thread(target=self._run_job, daemon=True, name='SystemUpdate')
            self.thread.start()
