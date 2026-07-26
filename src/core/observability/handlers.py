"""
Logging handlers that stay usable when the disk fills up.

A monitoring agent is most needed exactly when a host is out of space, so the
file handler degrades to stdout-only logging instead of raising.
"""

import logging
import sys
from logging.handlers import RotatingFileHandler
from pathlib import Path

import psutil

from core.observability.settings import DISK_FULL_THRESHOLD

ENOSPC = 28
REENABLE_MARGIN_PERCENT = 5.0


class DiskAwareRotatingFileHandler(RotatingFileHandler):
    """
    Rotating file handler that disables itself while the disk is critically full.

    File logging is suspended once usage reaches the threshold and resumed once
    usage drops a few points below it, so the agent keeps running and keeps
    logging to stdout during a disk-full incident.
    """

    def __init__(self, *args, disk_full_threshold: float = DISK_FULL_THRESHOLD, **kwargs):
        """
        Initialize the handler.

        Args:
            disk_full_threshold: Disk usage percentage at which file logging stops
        """
        self._file_logging_disabled = False
        self._disk_full_threshold = disk_full_threshold
        super().__init__(*args, **kwargs)

    def _check_disk_space(self) -> bool:
        """
        Check whether the log volume still has room.

        Returns:
            True when file logging may proceed
        """
        try:
            log_path = Path(self.baseFilename)
            if not log_path.exists():
                log_path = log_path.parent

            disk_percent = psutil.disk_usage(str(log_path)).percent

            if disk_percent >= self._disk_full_threshold:
                if not self._file_logging_disabled:
                    self._file_logging_disabled = True
                    print(
                        f"[LOGGER] Disk at {disk_percent:.1f}% - disabling file logging to prevent crashes",
                        file=sys.stderr,
                    )
                return False

            if self._file_logging_disabled and disk_percent < (self._disk_full_threshold - REENABLE_MARGIN_PERCENT):
                self._file_logging_disabled = False
                print(f"[LOGGER] Disk at {disk_percent:.1f}% - re-enabling file logging", file=sys.stderr)
            return not self._file_logging_disabled
        except Exception:
            return True

    def emit(self, record: logging.LogRecord) -> None:
        """
        Write a record, tolerating a full disk.

        Args:
            record: Log record to write
        """
        try:
            if not self._check_disk_space():
                return
            super().emit(record)
        except OSError as e:
            if e.errno == ENOSPC:
                if not self._file_logging_disabled:
                    self._file_logging_disabled = True
                    print(f"[LOGGER] Disk full error detected - disabling file logging: {e}", file=sys.stderr)
            else:
                self.handleError(record)
        except Exception:
            self.handleError(record)
