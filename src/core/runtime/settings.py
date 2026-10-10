"""
Runtime settings: every tunable the monitoring loop reads, loaded and clamped once at startup.
"""

import platform
from dataclasses import dataclass

from core.config import (
    DEFAULT_CPU_THRESHOLD,
    DEFAULT_MEMORY_THRESHOLD,
    DEFAULT_GPU_THRESHOLD,
    DEFAULT_DISK_THRESHOLD,
    DEFAULT_DISK_EARLY_WARNING_THRESHOLD,
    DEFAULT_DISK_CLEANUP_THRESHOLD,
    DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD,
    DEFAULT_DISK_EMERGENCY_THRESHOLD,
    DEFAULT_TEMP_THRESHOLD,
    DEFAULT_TEMP_ALERT_DURATION,
    DEFAULT_NETWORK_BANDWIDTH_THRESHOLD,
    DEFAULT_PROCESS_CPU_THRESHOLD,
    DEFAULT_PROCESS_MEMORY_THRESHOLD,
    DEFAULT_CHECK_INTERVAL,
    DEFAULT_ALERT_COOLDOWN,
    DEFAULT_FREEZE_THRESHOLD,
    DEFAULT_FREEZE_HEARTBEAT_TIMEOUT,
    DEFAULT_MEDIA_DIRECTORY_THRESHOLD_GB,
    DEFAULT_MEDIA_CHECK_INTERVAL,
    DEFAULT_KERNEL_CHECK_INTERVAL,
    DEFAULT_KERNEL_STABILITY_CHECK_INTERVAL,
    DEFAULT_KERNEL_CHECK_SINCE_MINUTES,
    DEFAULT_MONITORING_WATCHDOG_TIMEOUT,
    DEFAULT_WATCHDOG_AUTO_RESTART,
    DEFAULT_WATCHDOG_RESTART_COOLDOWN,
    DEFAULT_STALLED_TORRENT_THRESHOLD,
    DEFAULT_STALLED_TORRENT_ACTION_COOLDOWN,
    DEFAULT_STALLED_TORRENT_RECOVERY_GRACE,
    DEFAULT_STALLED_TORRENT_AUTO_HANDLE,
    DEFAULT_STALLED_TORRENT_RETRY_BACKOFF,
    DEFAULT_STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS,
    DEFAULT_SEEDING_RATIO_LIMIT,
    DEFAULT_SEEDING_RATIO_ACTION_COOLDOWN,
    DEFAULT_SEEDING_RATIO_RETRY_BACKOFF,
    DEFAULT_SEEDING_RATIO_DELETE_FILES,
    DEFAULT_INACTIVE_TORRENT_AUTO_REMOVE,
    DEFAULT_SYSTEM_UPDATE_INTERVAL_SECONDS,
    DEFAULT_SYSTEM_UPDATE_TIMEOUT,
    DEFAULT_SYSTEM_UPDATE_ENABLED,
    DEFAULT_SYSTEM_UPDATE_APPLY,
    DEFAULT_WEB_HOST,
    DEFAULT_WEB_PORT,
    clamp_threshold,
    get_cached_env,
    get_env_bool,
    get_env_float,
    get_env_int,
)
from core.format import BYTES_PER_GB
from core.observability import Component, get_logger

logger = get_logger(Component.CORE)


@dataclass(frozen=True)
class RuntimeSettings:
    """Validated configuration for one monitoring run."""

    web_ui_enabled: bool
    web_port: int
    web_host: str
    metrics_report_interval: float
    cpu_threshold: float
    memory_threshold: float
    gpu_threshold: float
    disk_threshold: float
    disk_early_warning_threshold: float
    disk_cleanup_threshold: float
    disk_aggressive_cleanup_threshold: float
    disk_emergency_threshold: float
    monitor_individual_partitions: bool
    temp_threshold: float
    temp_alert_duration: float
    network_bandwidth_threshold: float
    process_cpu_threshold: float
    process_memory_threshold: float
    check_interval: float
    alert_cooldown: float
    monitoring_watchdog_timeout: float
    watchdog_auto_restart: bool
    watchdog_restart_cooldown: float
    systemd_service_name: str
    freeze_threshold: float
    freeze_heartbeat_timeout: float
    freeze_monitor_enabled: bool
    media_directory: str
    media_directory_threshold_gb: float
    media_directory_threshold_bytes: int
    media_check_interval: int
    media_monitor_enabled: bool
    is_windows: bool
    log_threshold_mb: float
    log_check_interval: int
    kernel_check_interval: int
    kernel_stability_check_interval: int
    kernel_check_since_minutes: int
    kernel_monitor_enabled: bool
    system_update_enabled: bool
    system_update_interval_seconds: float
    system_update_apply: bool
    system_update_timeout: int
    stalled_threshold: float
    stalled_action_cooldown: float
    stalled_recovery_grace: float
    stalled_auto_handle: bool
    stalled_retry_backoff: float
    stalled_max_recovery_attempts: int
    seeding_ratio_limit: float
    seeding_ratio_action_cooldown: float
    seeding_ratio_retry_backoff: float
    seeding_ratio_delete_files: bool
    inactive_torrent_auto_remove: bool


def load_settings() -> RuntimeSettings:
    """
    Read, clamp and cross-validate every runtime setting from the environment.

    Returns:
        Immutable settings. Out-of-range thresholds are adjusted with a logged warning.
    """
    web_ui_enabled = get_env_bool('WEB_UI_ENABLED', True)
    web_port = get_env_int('WEB_PORT', DEFAULT_WEB_PORT, min_val=1, max_val=65535)
    web_host = get_cached_env('WEB_HOST', DEFAULT_WEB_HOST) or DEFAULT_WEB_HOST
    metrics_report_interval = get_env_float('METRICS_REPORT_INTERVAL', 3600.0)
    cpu_threshold = get_env_float('CPU_THRESHOLD', DEFAULT_CPU_THRESHOLD)
    memory_threshold = get_env_float('MEMORY_THRESHOLD', DEFAULT_MEMORY_THRESHOLD)
    gpu_threshold = get_env_float('GPU_THRESHOLD', DEFAULT_GPU_THRESHOLD)
    disk_threshold = get_env_float('DISK_THRESHOLD', DEFAULT_DISK_THRESHOLD)
    disk_early_warning_threshold = get_env_float('DISK_EARLY_WARNING_THRESHOLD', DEFAULT_DISK_EARLY_WARNING_THRESHOLD)
    disk_cleanup_threshold = get_env_float('DISK_CLEANUP_THRESHOLD', DEFAULT_DISK_CLEANUP_THRESHOLD)
    disk_aggressive_cleanup_threshold = get_env_float(
        'DISK_AGGRESSIVE_CLEANUP_THRESHOLD', DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD
    )
    disk_emergency_threshold = get_env_float('DISK_EMERGENCY_THRESHOLD', DEFAULT_DISK_EMERGENCY_THRESHOLD)
    monitor_individual_partitions = get_env_bool('MONITOR_INDIVIDUAL_PARTITIONS', True)
    temp_threshold = get_env_float('TEMP_THRESHOLD', DEFAULT_TEMP_THRESHOLD)
    temp_alert_duration = get_env_float('TEMP_ALERT_DURATION', DEFAULT_TEMP_ALERT_DURATION)
    network_bandwidth_threshold = get_env_float('NETWORK_BANDWIDTH_THRESHOLD', DEFAULT_NETWORK_BANDWIDTH_THRESHOLD)
    process_cpu_threshold = get_env_float('PROCESS_CPU_THRESHOLD', DEFAULT_PROCESS_CPU_THRESHOLD)
    process_memory_threshold = get_env_float('PROCESS_MEMORY_THRESHOLD', DEFAULT_PROCESS_MEMORY_THRESHOLD)
    check_interval = get_env_float('CHECK_INTERVAL', DEFAULT_CHECK_INTERVAL)
    alert_cooldown = get_env_float('ALERT_COOLDOWN', DEFAULT_ALERT_COOLDOWN)
    monitoring_watchdog_timeout = get_env_float('MONITORING_WATCHDOG_TIMEOUT', DEFAULT_MONITORING_WATCHDOG_TIMEOUT)
    watchdog_auto_restart = get_env_bool('WATCHDOG_AUTO_RESTART', DEFAULT_WATCHDOG_AUTO_RESTART)
    watchdog_restart_cooldown = get_env_float('WATCHDOG_RESTART_COOLDOWN', DEFAULT_WATCHDOG_RESTART_COOLDOWN)
    systemd_service_name = get_cached_env('SYSTEMD_SERVICE_NAME', 'server-monitor')
    freeze_threshold = get_env_float('FREEZE_THRESHOLD', DEFAULT_FREEZE_THRESHOLD)
    freeze_heartbeat_timeout = get_env_float('FREEZE_HEARTBEAT_TIMEOUT', DEFAULT_FREEZE_HEARTBEAT_TIMEOUT)
    freeze_monitor_enabled = get_env_bool('FREEZE_MONITOR_ENABLED', False)

    media_directory = get_cached_env('MEDIA_DIRECTORY', '/home/media')
    if media_directory and ('..' in media_directory or media_directory.startswith(('/proc', '/sys'))):
        logger.warning(f"MEDIA_DIRECTORY contains suspicious path components: {media_directory}")
        media_directory = '/home/media'
    media_directory_threshold_gb = get_env_float('MEDIA_DIRECTORY_THRESHOLD_GB', DEFAULT_MEDIA_DIRECTORY_THRESHOLD_GB)
    media_directory_threshold_bytes = int(media_directory_threshold_gb * BYTES_PER_GB)
    media_check_interval = get_env_int('MEDIA_CHECK_INTERVAL', DEFAULT_MEDIA_CHECK_INTERVAL)
    media_monitor_enabled = get_env_bool('MEDIA_MONITOR_ENABLED', False)
    cpu_threshold = clamp_threshold(cpu_threshold, 0.0, 100.0)
    memory_threshold = clamp_threshold(memory_threshold, 0.0, 100.0)
    gpu_threshold = clamp_threshold(gpu_threshold, 0.0, 100.0)
    disk_threshold = clamp_threshold(disk_threshold, 0.0, 100.0)
    disk_early_warning_threshold = clamp_threshold(disk_early_warning_threshold, 0.0, 100.0)
    disk_cleanup_threshold = clamp_threshold(disk_cleanup_threshold, 0.0, 100.0)
    disk_aggressive_cleanup_threshold = clamp_threshold(disk_aggressive_cleanup_threshold, 0.0, 100.0)
    disk_emergency_threshold = clamp_threshold(disk_emergency_threshold, 90.0, 99.0)
    if disk_aggressive_cleanup_threshold < disk_cleanup_threshold:
        disk_aggressive_cleanup_threshold = disk_cleanup_threshold
        logger.warning(
            f"Aggressive cleanup threshold adjusted to {disk_aggressive_cleanup_threshold:.1f}% "
            f"(must not be below the cleanup threshold)"
        )
    if disk_early_warning_threshold >= disk_threshold:
        disk_early_warning_threshold = max(0.0, disk_threshold - 5.0)
        logger.warning(
            f"Early warning threshold adjusted to {disk_early_warning_threshold:.1f}% (must be less than main threshold)"
        )
    if disk_cleanup_threshold >= disk_threshold:
        disk_cleanup_threshold = max(0.0, disk_threshold - 10.0)
        logger.warning(
            f"Cleanup threshold adjusted to {disk_cleanup_threshold:.1f}% (must be less than main threshold)"
        )
    if disk_emergency_threshold <= disk_threshold:
        disk_emergency_threshold = min(99.0, disk_threshold + 10.0)
        logger.warning(
            f"Emergency threshold adjusted to {disk_emergency_threshold:.1f}% (must be greater than main threshold)"
        )
    temp_threshold = max(0.0, temp_threshold)
    temp_alert_duration = max(0.0, temp_alert_duration)
    process_cpu_threshold = clamp_threshold(process_cpu_threshold, 0.0, 100.0)
    process_memory_threshold = clamp_threshold(process_memory_threshold, 0.0, 100.0)
    check_interval = max(0.1, check_interval)
    alert_cooldown = max(0.0, alert_cooldown)
    freeze_threshold = max(10.0, freeze_threshold)
    freeze_heartbeat_timeout = max(20.0, freeze_heartbeat_timeout)
    is_windows = platform.system() == 'Windows'
    log_threshold_mb = get_env_float('LOG_THRESHOLD_MB', 100.0)
    log_check_interval = get_env_int('LOG_CHECK_INTERVAL', 20)
    kernel_check_interval = get_env_int('KERNEL_CHECK_INTERVAL', DEFAULT_KERNEL_CHECK_INTERVAL)
    kernel_stability_check_interval = get_env_int(
        'KERNEL_STABILITY_CHECK_INTERVAL', DEFAULT_KERNEL_STABILITY_CHECK_INTERVAL
    )
    kernel_check_since_minutes = get_env_int('KERNEL_CHECK_SINCE_MINUTES', DEFAULT_KERNEL_CHECK_SINCE_MINUTES)
    kernel_monitor_enabled = get_env_bool('KERNEL_MONITOR_ENABLED', True)
    system_update_enabled = get_env_bool('SYSTEM_UPDATE_ENABLED', DEFAULT_SYSTEM_UPDATE_ENABLED)
    system_update_interval_seconds = get_env_float(
        'SYSTEM_UPDATE_INTERVAL_SECONDS',
        DEFAULT_SYSTEM_UPDATE_INTERVAL_SECONDS,
        min_val=300.0,
        max_val=2592000.0,
    )
    system_update_apply = get_env_bool('SYSTEM_UPDATE_APPLY', DEFAULT_SYSTEM_UPDATE_APPLY)
    system_update_timeout = get_env_int(
        'SYSTEM_UPDATE_TIMEOUT',
        DEFAULT_SYSTEM_UPDATE_TIMEOUT,
        min_val=60,
        max_val=86400,
    )
    stalled_threshold = get_env_float(
        'STALLED_TORRENT_THRESHOLD',
        DEFAULT_STALLED_TORRENT_THRESHOLD,
        min_val=30.0,
        max_val=86400.0,
    )
    stalled_action_cooldown = get_env_float(
        'STALLED_TORRENT_ACTION_COOLDOWN',
        DEFAULT_STALLED_TORRENT_ACTION_COOLDOWN,
        min_val=60.0,
        max_val=86400.0,
    )
    stalled_recovery_grace = get_env_float(
        'STALLED_TORRENT_RECOVERY_GRACE',
        DEFAULT_STALLED_TORRENT_RECOVERY_GRACE,
        min_val=0.0,
        max_val=3600.0,
    )
    stalled_auto_handle = get_env_bool(
        'STALLED_TORRENT_AUTO_HANDLE',
        DEFAULT_STALLED_TORRENT_AUTO_HANDLE,
    )
    stalled_retry_backoff = get_env_float(
        'STALLED_TORRENT_RETRY_BACKOFF',
        DEFAULT_STALLED_TORRENT_RETRY_BACKOFF,
        min_val=5.0,
        max_val=3600.0,
    )
    stalled_max_recovery_attempts = get_env_int(
        'STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS',
        DEFAULT_STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS,
        min_val=1,
        max_val=20,
    )
    seeding_ratio_limit = get_env_float(
        'SEEDING_RATIO_LIMIT',
        DEFAULT_SEEDING_RATIO_LIMIT,
        min_val=0.0,
        max_val=100.0,
    )
    seeding_ratio_action_cooldown = get_env_float(
        'SEEDING_RATIO_ACTION_COOLDOWN',
        DEFAULT_SEEDING_RATIO_ACTION_COOLDOWN,
        min_val=60.0,
        max_val=86400.0,
    )
    seeding_ratio_retry_backoff = get_env_float(
        'SEEDING_RATIO_RETRY_BACKOFF',
        DEFAULT_SEEDING_RATIO_RETRY_BACKOFF,
        min_val=5.0,
        max_val=3600.0,
    )
    seeding_ratio_delete_files = get_env_bool(
        'SEEDING_RATIO_DELETE_FILES',
        DEFAULT_SEEDING_RATIO_DELETE_FILES,
    )
    inactive_torrent_auto_remove = get_env_bool(
        'INACTIVE_TORRENT_AUTO_REMOVE',
        DEFAULT_INACTIVE_TORRENT_AUTO_REMOVE,
    )

    return RuntimeSettings(
        web_ui_enabled=web_ui_enabled,
        web_port=web_port,
        web_host=web_host,
        metrics_report_interval=metrics_report_interval,
        cpu_threshold=cpu_threshold,
        memory_threshold=memory_threshold,
        gpu_threshold=gpu_threshold,
        disk_threshold=disk_threshold,
        disk_early_warning_threshold=disk_early_warning_threshold,
        disk_cleanup_threshold=disk_cleanup_threshold,
        disk_aggressive_cleanup_threshold=disk_aggressive_cleanup_threshold,
        disk_emergency_threshold=disk_emergency_threshold,
        monitor_individual_partitions=monitor_individual_partitions,
        temp_threshold=temp_threshold,
        temp_alert_duration=temp_alert_duration,
        network_bandwidth_threshold=network_bandwidth_threshold,
        process_cpu_threshold=process_cpu_threshold,
        process_memory_threshold=process_memory_threshold,
        check_interval=check_interval,
        alert_cooldown=alert_cooldown,
        monitoring_watchdog_timeout=monitoring_watchdog_timeout,
        watchdog_auto_restart=watchdog_auto_restart,
        watchdog_restart_cooldown=watchdog_restart_cooldown,
        systemd_service_name=systemd_service_name,
        freeze_threshold=freeze_threshold,
        freeze_heartbeat_timeout=freeze_heartbeat_timeout,
        freeze_monitor_enabled=freeze_monitor_enabled,
        media_directory=media_directory,
        media_directory_threshold_gb=media_directory_threshold_gb,
        media_directory_threshold_bytes=media_directory_threshold_bytes,
        media_check_interval=media_check_interval,
        media_monitor_enabled=media_monitor_enabled,
        is_windows=is_windows,
        log_threshold_mb=log_threshold_mb,
        log_check_interval=log_check_interval,
        kernel_check_interval=kernel_check_interval,
        kernel_stability_check_interval=kernel_stability_check_interval,
        kernel_check_since_minutes=kernel_check_since_minutes,
        kernel_monitor_enabled=kernel_monitor_enabled,
        system_update_enabled=system_update_enabled,
        system_update_interval_seconds=system_update_interval_seconds,
        system_update_apply=system_update_apply,
        system_update_timeout=system_update_timeout,
        stalled_threshold=stalled_threshold,
        stalled_action_cooldown=stalled_action_cooldown,
        stalled_recovery_grace=stalled_recovery_grace,
        stalled_auto_handle=stalled_auto_handle,
        stalled_retry_backoff=stalled_retry_backoff,
        stalled_max_recovery_attempts=stalled_max_recovery_attempts,
        seeding_ratio_limit=seeding_ratio_limit,
        seeding_ratio_action_cooldown=seeding_ratio_action_cooldown,
        seeding_ratio_retry_backoff=seeding_ratio_retry_backoff,
        seeding_ratio_delete_files=seeding_ratio_delete_files,
        inactive_torrent_auto_remove=inactive_torrent_auto_remove,
    )
