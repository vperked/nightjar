"""
Logging system for the server monitor.

Usage:

    from core.observability import Component, get_logger

    logger = get_logger(Component.DISK)
    logger.info("Disk at 91%%")     # ServerMonitor.Disk - INFO - Disk at 91%

All component loggers are children of a single 'ServerMonitor' root that owns
the console and rotating file handlers. Component names must come from
core.observability.Component so log output stays filterable.
"""

from core.observability.components import KNOWN_COMPONENTS, ROOT, Component
from core.observability.handlers import DiskAwareRotatingFileHandler
from core.observability.loggers import (
    configure_logging,
    get_logger,
    is_file_logging_available,
    safe_log,
)
from core.observability.settings import (
    LOG_BACKUP_COUNT,
    LOG_FILE,
    LOG_LEVEL,
    LOG_MAX_BYTES,
)

__all__ = [
    'Component',
    'DiskAwareRotatingFileHandler',
    'KNOWN_COMPONENTS',
    'LOG_BACKUP_COUNT',
    'LOG_FILE',
    'LOG_LEVEL',
    'LOG_MAX_BYTES',
    'ROOT',
    'configure_logging',
    'get_logger',
    'is_file_logging_available',
    'safe_log',
]
