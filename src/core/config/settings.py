"""
Configuration management and environment variable access.

Provides centralized configuration access with caching, validation, and type conversion.
"""

import os
from typing import Optional
from core.observability import Component, get_logger

logger = get_logger(Component.CONFIG)

MAX_ENV_VALUE_LENGTH = 10000

_env_cache = {}
_typed_env_cache = {}

DEFAULT_CPU_THRESHOLD = 80.0
DEFAULT_MEMORY_THRESHOLD = 80.0
DEFAULT_GPU_THRESHOLD = 80.0
DEFAULT_DISK_THRESHOLD = 80.0
DEFAULT_DISK_EARLY_WARNING_THRESHOLD = 75.0
DEFAULT_DISK_CLEANUP_THRESHOLD = 85.0
DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD = 92.0
DEFAULT_DISK_EMERGENCY_THRESHOLD = 95.0
DEFAULT_TEMP_THRESHOLD = 80.0
DEFAULT_TEMP_ALERT_DURATION = 30.0
DEFAULT_NETWORK_BANDWIDTH_THRESHOLD = 1000.0
DEFAULT_PROCESS_CPU_THRESHOLD = 50.0
DEFAULT_PROCESS_MEMORY_THRESHOLD = 10.0
DEFAULT_CHECK_INTERVAL = 5.0
DEFAULT_ALERT_COOLDOWN = 300.0
DEFAULT_ALERT_MAX_COOLDOWN = 3600.0
DEFAULT_ALERT_BACKOFF = 2.0
DEFAULT_ALERT_RECOVERY_SECONDS = 300.0
DEFAULT_FREEZE_THRESHOLD = 30.0
DEFAULT_FREEZE_HEARTBEAT_TIMEOUT = 60.0
DEFAULT_MEDIA_DIRECTORY_THRESHOLD_GB = 500.0
DEFAULT_MEDIA_CHECK_INTERVAL = 10
DEFAULT_KERNEL_CHECK_INTERVAL = 30
DEFAULT_KERNEL_STABILITY_CHECK_INTERVAL = 10
DEFAULT_KERNEL_CHECK_SINCE_MINUTES = 60
DEFAULT_MONITORING_WATCHDOG_TIMEOUT = 30.0
DEFAULT_WATCHDOG_AUTO_RESTART = False
DEFAULT_WATCHDOG_RESTART_COOLDOWN = 300.0
DEFAULT_STALLED_TORRENT_THRESHOLD = 300.0
DEFAULT_STALLED_TORRENT_ACTION_COOLDOWN = 1800.0
DEFAULT_STALLED_TORRENT_RECOVERY_GRACE = 120.0
DEFAULT_STALLED_TORRENT_AUTO_HANDLE = True
DEFAULT_STALLED_TORRENT_RETRY_BACKOFF = 60.0
DEFAULT_STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS = 2
DEFAULT_SEEDING_RATIO_LIMIT = 0.0
DEFAULT_SEEDING_RATIO_ACTION_COOLDOWN = 1800.0
DEFAULT_SEEDING_RATIO_RETRY_BACKOFF = 60.0
DEFAULT_SEEDING_RATIO_DELETE_FILES = False
DEFAULT_INACTIVE_TORRENT_AUTO_REMOVE = False
DEFAULT_SYSTEM_UPDATE_INTERVAL_SECONDS = 86400.0
DEFAULT_SYSTEM_UPDATE_TIMEOUT = 3600
DEFAULT_SYSTEM_UPDATE_ENABLED = False
DEFAULT_SYSTEM_UPDATE_APPLY = False

DEFAULT_WEB_HOST = '127.0.0.1'
DEFAULT_WEB_PORT = 8080
DEFAULT_WEB_ALLOWED_ORIGIN = 'null'

EMBED_SEND_DELAY = 0.2
MAX_TRACKED_TORRENTS = 1000

MAX_PROCESSES_DISPLAY = 5
MAX_REASONS_DISPLAY = 3
MAX_INDICATORS_DISPLAY = 8
MAX_RECOMMENDATIONS_DISPLAY = 5
MAX_CRASH_TYPES_DISPLAY = 5
MAX_HARDWARE_ERRORS_DISPLAY = 5
MAX_DIRECTORIES_DISPLAY = 5

DEFAULT_ROTATED_LOG_RETENTION_DAYS = 7.0

LOG_CLEANUP_INTERVAL_SECONDS = 1800
DISK_CLEANUP_COOLDOWN_HIGH = 300
DISK_CLEANUP_COOLDOWN_MEDIUM = 900
DISK_CLEANUP_COOLDOWN_LOW = 1800
DISK_CLEANUP_COOLDOWN_NORMAL = 3600
DISK_EMERGENCY_COOLDOWN = 300


def get_cached_env(key: str, default: Optional[str] = None) -> Optional[str]:
    """
    Get environment variable with caching to avoid repeated lookups.

    Environment variables are cached after first access to improve performance.
    Oversized values are rejected before caching.

    Args:
        key: Environment variable name
        default: Default value if variable is not set (default: None)

    Returns:
        Environment variable value or default if not set
    """
    if key not in _env_cache:
        value = os.getenv(key, default)
        if value and len(value) > MAX_ENV_VALUE_LENGTH:
            # Values are never passed through a shell, so shell metacharacters are legal
            # (tokens and passwords routinely contain them). Only bound the size.
            logger.warning(
                f"Environment variable {key} exceeds {MAX_ENV_VALUE_LENGTH} characters. Falling back to default."
            )
            value = default
        _env_cache[key] = value
    return _env_cache[key]


def get_env_float(key: str, default: float, min_val: Optional[float] = None, max_val: Optional[float] = None) -> float:
    """
    Safely get float value from environment variable with optional range validation.

    Handles empty strings, None values, and invalid conversions gracefully.
    Returns default value on any error. Validates range if min_val/max_val provided.

    Args:
        key: Environment variable name
        default: Default value to return if variable is not set or invalid
        min_val: Optional minimum allowed value
        max_val: Optional maximum allowed value

    Returns:
        Float value from environment variable or default
    """
    cache_key = ('f', key, default, min_val, max_val)
    if cache_key in _typed_env_cache:
        return _typed_env_cache[cache_key]

    value = get_cached_env(key, str(default))
    if not value or value.strip() == '':
        _typed_env_cache[cache_key] = default
        return default
    try:
        float_value = float(value)

        if min_val is not None and float_value < min_val:
            logger.warning(f"{key}={float_value} is below minimum {min_val}, using default {default}")
            _typed_env_cache[cache_key] = default
            return default
        if max_val is not None and float_value > max_val:
            logger.warning(f"{key}={float_value} is above maximum {max_val}, using default {default}")
            _typed_env_cache[cache_key] = default
            return default

        _typed_env_cache[cache_key] = float_value
        return float_value
    except (ValueError, TypeError):
        _typed_env_cache[cache_key] = default
        return default


def get_env_int(key: str, default: int, min_val: Optional[int] = None, max_val: Optional[int] = None) -> int:
    """
    Safely get integer value from environment variable with optional range validation.

    Handles empty strings, None values, and invalid conversions gracefully.
    Returns default value on any error. Validates range if min_val/max_val provided.

    Args:
        key: Environment variable name
        default: Default value to return if variable is not set or invalid
        min_val: Optional minimum allowed value
        max_val: Optional maximum allowed value

    Returns:
        Integer value from environment variable or default
    """
    cache_key = ('i', key, default, min_val, max_val)
    if cache_key in _typed_env_cache:
        return _typed_env_cache[cache_key]

    value = get_cached_env(key, str(default))
    if not value or value.strip() == '':
        _typed_env_cache[cache_key] = default
        return default
    try:
        int_value = int(value)

        if min_val is not None and int_value < min_val:
            logger.warning(f"{key}={int_value} is below minimum {min_val}, using default {default}")
            _typed_env_cache[cache_key] = default
            return default
        if max_val is not None and int_value > max_val:
            logger.warning(f"{key}={int_value} is above maximum {max_val}, using default {default}")
            _typed_env_cache[cache_key] = default
            return default

        _typed_env_cache[cache_key] = int_value
        return int_value
    except (ValueError, TypeError):
        _typed_env_cache[cache_key] = default
        return default


def clamp_threshold(value: float, min_val: float, max_val: float) -> float:
    """
    Clamp a threshold value between minimum and maximum bounds.

    Args:
        value: Value to clamp
        min_val: Minimum allowed value
        max_val: Maximum allowed value

    Returns:
        Clamped value within [min_val, max_val] range
    """
    return max(min_val, min(max_val, value))


def get_env_bool(key: str, default: bool = False) -> bool:
    """
    Safely get boolean value from environment variable.

    Accepts various string representations: 'true', '1', 'yes', 'on' for True,
    and 'false', '0', 'no', 'off', '' for False. Case-insensitive.

    Args:
        key: Environment variable name
        default: Default value to return if variable is not set or invalid (default: False)

    Returns:
        Boolean value from environment variable or default
    """
    cache_key = ('b', key, default)
    if cache_key in _typed_env_cache:
        return _typed_env_cache[cache_key]

    value = get_cached_env(key, str(default).lower())
    if not value or value.strip() == '':
        _typed_env_cache[cache_key] = default
        return default
    value_lower = value.strip().lower()
    if value_lower in ('true', '1', 'yes', 'on'):
        _typed_env_cache[cache_key] = True
        return True
    if value_lower in ('false', '0', 'no', 'off', ''):
        _typed_env_cache[cache_key] = False
        return False
    logger.warning(f"Invalid boolean value '{value}' for {key}, defaulting to {default}")
    _typed_env_cache[cache_key] = default
    return default


def validate_config() -> None:
    """
    Validate configuration at startup.

    Checks required environment variables and validates their values.
    Raises ValueError if critical configuration is missing or invalid.
    """
    errors = []
    warnings = []

    webhook_url = os.getenv('WEBHOOK')
    if not webhook_url:
        warnings.append("WEBHOOK environment variable not set - Discord notifications will be disabled")
    elif not webhook_url.startswith('https://discord.com/api/webhooks/'):
        warnings.append(f"WEBHOOK URL does not match expected Discord webhook format: {webhook_url[:50]}...")

    numeric_vars = {
        'CPU_THRESHOLD': (0.0, 100.0),
        'MEMORY_THRESHOLD': (0.0, 100.0),
        'GPU_THRESHOLD': (0.0, 100.0),
        'DISK_THRESHOLD': (0.0, 100.0),
        'TEMP_THRESHOLD': (0.0, 150.0),
        'CHECK_INTERVAL': (1.0, 3600.0),
    }

    for var_name, (min_val, max_val) in numeric_vars.items():
        value = os.getenv(var_name)
        if value:
            try:
                float_val = float(value)
                if not (min_val <= float_val <= max_val):
                    errors.append(f"{var_name}={float_val} is outside valid range [{min_val}, {max_val}]")
            except ValueError:
                errors.append(f"{var_name}={value} is not a valid number")

    boolean_vars = [
        'QBITTORRENT_MONITOR',
        'MEDIA_MONITOR_ENABLED',
        'INSTALL_SYSTEMD',
        'STALLED_TORRENT_AUTO_HANDLE',
        'SEEDING_RATIO_DELETE_FILES',
        'INACTIVE_TORRENT_AUTO_REMOVE',
    ]
    for var_name in boolean_vars:
        value = os.getenv(var_name)
        if value and value.lower() not in ('true', 'false', '1', '0', 'yes', 'no', 'on', 'off', ''):
            warnings.append(f"{var_name}={value} is not a valid boolean value (use true/false)")

    path_vars = ['MEDIA_DIRECTORY', 'LOG_FILE', 'ALERT_LOG_PATH']
    for var_name in path_vars:
        value = os.getenv(var_name)
        if value:
            if not isinstance(value, str) or len(value) > 1000:
                errors.append(f"{var_name} path is invalid or too long")
            elif '..' in value or '\x00' in value:
                errors.append(f"{var_name} contains invalid characters")

    for warning in warnings:
        logger.warning(f"Configuration warning: {warning}")

    if errors:
        error_msg = "Configuration validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
        logger.error(error_msg)
        raise ValueError(error_msg)

    if not warnings:
        logger.info("Configuration validation passed")
