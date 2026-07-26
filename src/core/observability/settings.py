"""
Environment-driven settings for the logging system.

This module deliberately parses the environment itself instead of using
core.config: core.config depends on logging, so importing it here would create
an import cycle.
"""

import os
from typing import Optional

from dotenv import load_dotenv

load_dotenv()

DEFAULT_LOG_LEVEL = 'INFO'
DEFAULT_LOG_FILE = 'server_monitor.log'
DEFAULT_LOG_MAX_BYTES = 10 * 1024 * 1024
DEFAULT_LOG_BACKUP_COUNT = 5
DEFAULT_DISK_FULL_THRESHOLD = 95.0

LOG_FORMAT = '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
LOG_DATE_FORMAT = '%Y-%m-%d %H:%M:%S'

VALID_LEVELS = frozenset({'DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'})


def _env_int(key: str, default: int, minimum: int = 0) -> int:
    """
    Read an integer setting, falling back to the default on any invalid value.

    Args:
        key: Environment variable name
        default: Value used when unset or invalid
        minimum: Smallest accepted value

    Returns:
        Parsed integer setting
    """
    raw = os.getenv(key)
    if not raw:
        return default
    try:
        value = int(raw)
    except (TypeError, ValueError):
        return default
    return value if value >= minimum else default


def _env_float(key: str, default: float, minimum: float, maximum: float) -> float:
    """
    Read a float setting, falling back to the default on any invalid value.

    Args:
        key: Environment variable name
        default: Value used when unset or invalid
        minimum: Smallest accepted value
        maximum: Largest accepted value

    Returns:
        Parsed float setting
    """
    raw = os.getenv(key)
    if not raw:
        return default
    try:
        value = float(raw)
    except (TypeError, ValueError):
        return default
    return value if minimum <= value <= maximum else default


def resolve_level(level: Optional[str] = None) -> str:
    """
    Resolve the effective log level name.

    Args:
        level: Explicit level override, if any

    Returns:
        A valid logging level name, defaulting to INFO
    """
    candidate = (level or os.getenv('LOG_LEVEL', DEFAULT_LOG_LEVEL) or '').strip().upper()
    return candidate if candidate in VALID_LEVELS else DEFAULT_LOG_LEVEL


LOG_LEVEL = resolve_level()
LOG_FILE = os.getenv('LOG_FILE', DEFAULT_LOG_FILE) or DEFAULT_LOG_FILE
LOG_MAX_BYTES = _env_int('LOG_MAX_BYTES', DEFAULT_LOG_MAX_BYTES, minimum=1024)
LOG_BACKUP_COUNT = _env_int('LOG_BACKUP_COUNT', DEFAULT_LOG_BACKUP_COUNT, minimum=0)
DISK_FULL_THRESHOLD = _env_float('DISK_FULL_THRESHOLD', DEFAULT_DISK_FULL_THRESHOLD, 1.0, 100.0)
