"""
Media directory size alerts.
"""

from pathlib import Path

from core.format import format_bytes
from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.timeout import run_with_timeout



class MediaCheck(Handler):
    """Periodically measure the media directory and alert when it grows past the threshold."""

    log = get_logger(Component.DISK)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.media_check_counter = 0

    def check(self, snap: Snapshot) -> None:
        """Measure the media directory every few cycles and alert when it is too large."""
        cfg = self.cfg
        self.media_check_counter += 1
        if not (
            cfg.media_monitor_enabled
            and self.media_check_counter >= cfg.media_check_interval
            and Path(cfg.media_directory).exists()
        ):
            return
        self.media_check_counter = 0
        try:
            self._measure(snap.current_time)
        except Exception as e:
            self.log.debug(f"Directory size check failed: {e}")

    def _measure(self, now: float) -> None:
        cfg, ctx = self.cfg, self.ctx
        directory, threshold = cfg.media_directory, cfg.media_directory_threshold_bytes
        size_info = run_with_timeout(
            lambda: ctx.disk_monitor.get_directory_size(directory),
            timeout=60.0,
            default_value=None,
            operation_name="Get media directory size",
        )
        if not size_info or size_info['size'] < threshold:
            return

        def send() -> bool:
            return ctx.deliver(
                ctx.disk_webhook,
                'send_directory_size_alert',
                "Directory size alert webhook",
                directory_path=directory,
                size_bytes=size_info['size'],
                threshold_bytes=threshold,
                subdirectories=self._subdirectories(),
            )

        self.raise_alert(
            'media',
            now,
            f"Directory {directory}: {format_bytes(size_info['size'])} (threshold: {format_bytes(threshold)})",
            send,
            webhook=ctx.disk_webhook,
            cooldown_label='Directory',
            sent_message="    Discord directory alert sent",
            failed_message="    Discord directory alert failed",
        )

    def _subdirectories(self):
        """Human-readable sizes of the media directory's immediate children, largest first."""
        disk = self.ctx.disk_monitor
        subdirs = run_with_timeout(
            lambda: disk.get_subdirectories_sizes(self.cfg.media_directory),
            timeout=60.0,
            default_value=[],
            operation_name="Get media subdirectories sizes",
        )
        human = (disk.get_directory_size_human(s.get('path', ''), size_info=s) for s in subdirs)
        return [h for h in human if h]
