"""
Logger factory for the server monitor.

Handlers are attached once to the single root logger ('ServerMonitor'). Every
component logger is a child of that root and propagates to it, so there is
exactly one console handler and one rotating file handler for the whole process
regardless of how many components ask for a logger.
"""

import logging
import sys
import threading
from pathlib import Path
from typing import Callable, Optional

from core.observability.components import ROOT
from core.observability.handlers import DiskAwareRotatingFileHandler
from core.observability.redaction import RedactingFormatter
from core.observability.settings import (
    LOG_BACKUP_COUNT,
    LOG_DATE_FORMAT,
    LOG_FILE,
    LOG_FORMAT,
    LOG_LEVEL,
    LOG_MAX_BYTES,
    resolve_level,
)

ENOSPC = 28

_configure_lock = threading.Lock()
_configured = False
_file_logging_available = False


def configure_logging(level: Optional[str] = None, force: bool = False) -> logging.Logger:
    """
    Configure the root application logger. Safe to call repeatedly.

    Args:
        level: Optional level override, otherwise LOG_LEVEL from the environment
        force: Rebuild handlers even if logging is already configured

    Returns:
        The configured root logger
    """
    global _configured, _file_logging_available

    root = logging.getLogger(ROOT)

    with _configure_lock:
        if _configured and not force:
            if level is not None:
                _apply_level(root, resolve_level(level))
            return root

        for handler in list(root.handlers):
            root.removeHandler(handler)
            try:
                handler.close()
            except Exception:
                pass

        effective_level = resolve_level(level or LOG_LEVEL)
        numeric_level = getattr(logging, effective_level, logging.INFO)
        root.setLevel(numeric_level)
        root.propagate = False

        formatter = RedactingFormatter(fmt=LOG_FORMAT, datefmt=LOG_DATE_FORMAT)

        console_handler = logging.StreamHandler(sys.stdout)
        console_handler.setLevel(numeric_level)
        console_handler.setFormatter(formatter)
        root.addHandler(console_handler)

        _file_logging_available = False
        try:
            log_path = Path(LOG_FILE)
            if log_path.parent != Path(''):
                log_path.parent.mkdir(parents=True, exist_ok=True)

            file_handler = DiskAwareRotatingFileHandler(
                LOG_FILE,
                maxBytes=LOG_MAX_BYTES,
                backupCount=LOG_BACKUP_COUNT,
            )
            file_handler.setLevel(numeric_level)
            file_handler.setFormatter(formatter)
            root.addHandler(file_handler)
            _file_logging_available = True
        except (OSError, PermissionError) as e:
            print(f"[LOGGER] File logging unavailable ({e}); continuing with console only", file=sys.stderr)

        _configured = True

    return root


def _apply_level(root: logging.Logger, level_name: str) -> None:
    """
    Apply a level to the root logger and its handlers.

    Args:
        root: Root logger
        level_name: Validated level name
    """
    numeric_level = getattr(logging, level_name, logging.INFO)
    root.setLevel(numeric_level)
    for handler in root.handlers:
        handler.setLevel(numeric_level)


def get_logger(component: Optional[str] = None) -> logging.Logger:
    """
    Get the logger for a component.

    Args:
        component: A name from core.observability.Component. Omit for the root logger,
            which is intended for the application entry point only.

    Returns:
        Logger named 'ServerMonitor' or 'ServerMonitor.<Component>'
    """
    configure_logging()

    if not component or component == ROOT:
        return logging.getLogger(ROOT)

    return logging.getLogger(f'{ROOT}.{component}')


def is_file_logging_available() -> bool:
    """
    Report whether a log file handler was successfully attached.

    Returns:
        True when logs are also being written to LOG_FILE
    """
    configure_logging()
    return _file_logging_available


def safe_log(logger_func: Callable[..., None], message: str, *args, **kwargs) -> None:
    """
    Call a logger method, tolerating a full disk.

    Use this on paths that must keep running during a disk-full incident, such as
    cleanup routines.

    Args:
        logger_func: Bound logger method, e.g. logger.info
        message: Message to log
        *args: Extra positional arguments for the logger method
        **kwargs: Extra keyword arguments for the logger method
    """
    try:
        logger_func(message, *args, **kwargs)
    except OSError as e:
        if e.errno == ENOSPC:
            print(f"[LOGGER] Disk full - could not log: {message}", file=sys.stderr)
        else:
            print(f"[LOGGER] Logging error: {e} - Message: {message}", file=sys.stderr)
    except Exception as e:
        print(f"[LOGGER] Unexpected logging error: {e} - Message: {message}", file=sys.stderr)
