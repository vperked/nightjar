"""
Startup: configuration detection, monitor and webhook construction, the startup banner and notices.
"""

import os
import sys
import time
from pathlib import Path
from typing import Any, Optional

from core.config import get_cached_env, get_env_bool, validate_config
from core.cpu import CpuMonitor
from core.detection import (
    apply_system_adaptations,
    detect_system_config,
    prompt_save_config,
    save_auto_detected_config,
)
from core.disk import DiskMonitor
from core.gpu import GpuMonitor
from core.kernel import KernelMonitor
from core.logs import LogMonitor
from core.memory import MemoryMonitor
from core.network import NetworkMonitor
from core.observability import Component, get_logger
from core.processes import ProcessMonitor
from core.qbittorrent import QBittorrentMonitor
from core.runtime.context import MonitorContext
from core.sensors import SensorsMonitor
from core.system import SystemMonitor
from core.timeout import send_webhook
from core.web import set_crash_webhook, set_monitors, start_server
from core.webhook import DiscordWebhook

logger = get_logger(Component.CORE)


def detect_configuration() -> None:
    """Run hardware detection, offer to save adaptations, and validate the environment (exits on error)."""
    thresholds_already_set = (
        os.getenv('CPU_THRESHOLD') is not None
        or os.getenv('MEMORY_THRESHOLD') is not None
        or os.getenv('DISK_THRESHOLD') is not None
    )

    logger.info("Detecting system capabilities...")
    detected_config = detect_system_config(skip_usage_analysis=thresholds_already_set)
    if detected_config['adaptations']:
        logger.info("System detection results:")
        for adaptation in detected_config['adaptations']:
            logger.info(f"  - {adaptation}")

    auto_detected_values = apply_system_adaptations(detected_config)

    if auto_detected_values:
        if prompt_save_config(auto_detected_values):
            save_auto_detected_config(auto_detected_values)
        else:
            logger.info("Auto-detected configuration not saved (user declined or non-interactive mode)")

    try:
        validate_config()
    except ValueError as e:
        logger.error(f"Configuration validation failed: {e}")
        sys.exit(1)


def build_monitors(
    cpu_monitor=None,
    memory_monitor=None,
    system_monitor=None,
    gpu_monitor=None,
    disk_monitor=None,
    network_monitor=None,
    sensors_monitor=None,
    process_monitor=None,
    qbittorrent_monitor=None,
    log_monitor=None,
    kernel_monitor=None,
) -> dict[str, Any]:
    """
    Construct any monitor that was not injected.

    Returns:
        Monitors by name, plus 'qbittorrent_monitor_enabled'
    """
    logger.info("Initializing monitors...")
    qbittorrent_monitor_enabled = get_env_bool('QBITTORRENT_MONITOR', False)

    if cpu_monitor is None:
        cpu_monitor = CpuMonitor()
    if memory_monitor is None:
        memory_monitor = MemoryMonitor()
    if system_monitor is None:
        system_monitor = SystemMonitor()
    if gpu_monitor is None:
        gpu_monitor = GpuMonitor()
    if disk_monitor is None:
        disk_monitor = DiskMonitor()
    if network_monitor is None:
        network_monitor = NetworkMonitor()
    if sensors_monitor is None:
        sensors_monitor = SensorsMonitor()
    if process_monitor is None:
        process_monitor = ProcessMonitor()
    if qbittorrent_monitor is None and qbittorrent_monitor_enabled:
        qbittorrent_monitor = QBittorrentMonitor()
        logger.info("qBittorrent monitoring enabled")
    elif not qbittorrent_monitor_enabled:
        logger.info("qBittorrent monitoring disabled (set QBITTORRENT_MONITOR=true to enable)")
    if log_monitor is None:
        log_monitor = LogMonitor()
    if kernel_monitor is None:
        kernel_monitor = KernelMonitor()
        logger.info("Kernel monitoring enabled")
    logger.info("Monitors initialized")

    return {
        'cpu_monitor': cpu_monitor,
        'memory_monitor': memory_monitor,
        'system_monitor': system_monitor,
        'gpu_monitor': gpu_monitor,
        'disk_monitor': disk_monitor,
        'network_monitor': network_monitor,
        'sensors_monitor': sensors_monitor,
        'process_monitor': process_monitor,
        'qbittorrent_monitor': qbittorrent_monitor,
        'log_monitor': log_monitor,
        'kernel_monitor': kernel_monitor,
        'qbittorrent_monitor_enabled': qbittorrent_monitor_enabled,
    }


def connect_webhooks(
    qbittorrent_monitor_enabled: bool,
) -> tuple[Optional[Any], Optional[Any], Optional[Any], Optional[Any]]:
    """
    Connect the configured Discord webhooks.

    Returns:
        (main, qBittorrent, disk, crash) webhooks; each is None when not configured
    """
    webhook = None
    logger.info("Connecting to Discord webhook...")
    try:
        webhook_url = get_cached_env('WEBHOOK')
        if webhook_url and not webhook_url.startswith('https://discord.com/api/webhooks/'):
            logger.warning("WEBHOOK URL does not match expected Discord webhook format")
        webhook = DiscordWebhook()
        logger.info("Discord webhook connected")
    except ValueError:
        logger.info("Discord webhook not configured")

    qbittorrent_webhook = None
    if qbittorrent_monitor_enabled:
        qbittorrent_webhook_url = get_cached_env('QBITTORRENT_WEBHOOK')
        if qbittorrent_webhook_url:
            try:
                qbittorrent_webhook = DiscordWebhook(webhook_url=qbittorrent_webhook_url)
                logger.info("qBittorrent Discord webhook connected")
            except ValueError:
                logger.warning("qBittorrent webhook URL invalid")
            except Exception as e:
                logger.warning(f"Failed to connect to qBittorrent webhook: {e}")
        else:
            logger.debug("qBittorrent webhook not configured")

    disk_webhook = None
    disk_webhook_url = get_cached_env('DISK_WEBHOOK')
    if disk_webhook_url:
        try:
            disk_webhook = DiscordWebhook(webhook_url=disk_webhook_url)
            logger.info("Disk Discord webhook connected")
        except ValueError:
            logger.warning("Disk webhook URL invalid")
        except Exception as e:
            logger.warning(f"Failed to connect to disk webhook: {e}")
    else:
        logger.debug("Disk webhook not configured")

    web_ui_enabled = get_env_bool('WEB_UI_ENABLED', True)

    crash_webhook = None
    crash_webhook_url = get_cached_env('CRASH_WEBHOOK')
    if crash_webhook_url:
        try:
            crash_webhook = DiscordWebhook(webhook_url=crash_webhook_url)
            logger.info("Crash Discord webhook connected")
            if web_ui_enabled:
                set_crash_webhook(crash_webhook)
        except ValueError:
            logger.warning("Crash webhook URL invalid")
        except Exception as e:
            logger.warning(f"Failed to connect to crash webhook: {e}")
    else:
        logger.debug("Crash webhook not configured")

    return webhook, qbittorrent_webhook, disk_webhook, crash_webhook


def log_startup_summary(ctx: MonitorContext) -> bool:
    """
    Log the system summary and effective thresholds, and probe qBittorrent.

    Returns:
        True when qBittorrent answered and reported a summary
    """
    cfg = ctx.cfg
    logger.info("Gathering system information...")
    start_time = time.time()
    system_info = ctx.system_monitor.get_system_info()
    system_summary = ctx.system_monitor.get_system_summary()
    elapsed_time = time.time() - start_time
    logger.info(f"System information gathered ({elapsed_time:.2f}s)")

    memory_info = ctx.memory_monitor.get_memory_info_human()
    total_disk = ctx.disk_monitor.get_total_disk_usage()

    logger.info(
        f"System: {system_info['hostname']} | {system_info['platform']} {system_info['platform_release']} | {system_info['architecture']}"
    )
    logger.info(f"Uptime: {system_summary['uptime']}")
    logger.info(
        f"CPU: {ctx.cpu_monitor.cpu_count} logical, {ctx.cpu_monitor.cpu_count_physical} physical | Threshold: {cfg.cpu_threshold:.1f}%"
    )
    logger.info(f"Memory: {memory_info['total']} | Threshold: {cfg.memory_threshold:.1f}%")
    logger.info(
        f"Disk: {total_disk['percent']:.1f}% used | Threshold: {cfg.disk_threshold:.1f}% | Early Warning: {cfg.disk_early_warning_threshold:.1f}% | Cleanup: {cfg.disk_cleanup_threshold:.1f}% | Emergency: {cfg.disk_emergency_threshold:.1f}%"
    )
    if cfg.monitor_individual_partitions:
        logger.info("Individual partition monitoring: Enabled")

    if ctx.gpu_monitor.nvidia_smi_available:
        gpu_data_startup = ctx.gpu_monitor.get_gpu_comprehensive(0)
        gpu_count = ctx.gpu_monitor.get_gpu_count()
        gpu_name = ""
        if gpu_data_startup:
            gpu_name = f" | {gpu_data_startup.get('info', {}).get('name', 'N/A')}"
        logger.info(f"GPU: {gpu_count} detected | Threshold: {cfg.gpu_threshold:.1f}%{gpu_name}")

    if ctx.sensors_monitor.is_available():
        cpu_temp = ctx.sensors_monitor.get_cpu_temperature()
        logger.info(
            f"Sensors: {ctx.sensors_monitor.format_temperature(cpu_temp)} | Threshold: {cfg.temp_threshold:.1f}C | Alert Duration: {cfg.temp_alert_duration:.1f}s"
        )

    logger.info(
        f"Processes: CPU threshold {cfg.process_cpu_threshold:.1f}% | Memory threshold {cfg.process_memory_threshold:.1f}%"
    )
    logger.info(f"Network: Bandwidth threshold {cfg.network_bandwidth_threshold:.1f} MB/s")
    if cfg.freeze_monitor_enabled:
        logger.info(
            f"Freeze Detection: Threshold {cfg.freeze_threshold:.1f}s | Heartbeat timeout {cfg.freeze_heartbeat_timeout:.1f}s"
        )
    else:
        logger.info("Freeze Detection: disabled")
    if cfg.media_monitor_enabled:
        if Path(cfg.media_directory).exists():
            logger.info(
                f"Directory Monitoring: {cfg.media_directory} | Threshold: {cfg.media_directory_threshold_gb:.1f} GB"
            )
        else:
            logger.warning(
                f"Directory Monitoring enabled but path not found: {cfg.media_directory}. Skipping directory monitoring."
            )
    else:
        logger.info("Directory Monitoring disabled (set MEDIA_MONITOR_ENABLED=true to enable)")

    ctx.log_monitor.set_threshold(cfg.log_threshold_mb)
    logger.info(
        f"Log Monitoring: Threshold: {cfg.log_threshold_mb:.1f} MB | Check interval: {cfg.log_check_interval} cycles | Auto-clean: Enabled"
    )

    if cfg.kernel_monitor_enabled:
        logger.info(
            f"Kernel Monitoring: Crash check interval: {cfg.kernel_check_interval} cycles | Stability check: {cfg.kernel_stability_check_interval} cycles | Lookback: {cfg.kernel_check_since_minutes} minutes"
        )
    else:
        logger.info("Kernel Monitoring: Disabled (set KERNEL_MONITOR_ENABLED=true to enable)")

    if cfg.system_update_enabled:
        update_mode = 'apply upgrades' if cfg.system_update_apply else 'check only (no install)'
        logger.info(
            f"System updates: every {cfg.system_update_interval_seconds / 3600:.1f}h | {update_mode} | command timeout {cfg.system_update_timeout}s"
        )
    else:
        logger.info("System updates: disabled (set SYSTEM_UPDATE_ENABLED=true to enable)")

    qbittorrent_connected = False
    if ctx.qbittorrent_monitor:
        if ctx.qbittorrent_monitor.password:
            logger.debug("Checking qBittorrent connection")
            if ctx.qbittorrent_monitor.is_available():
                try:
                    summary = ctx.qbittorrent_monitor.get_torrent_summary()
                    logger.info(
                        f"qBittorrent: Connected | {summary['total']} torrents ({summary['downloading']} downloading, {summary['seeding']} seeding)"
                    )
                    qbittorrent_connected = True
                except Exception as e:
                    logger.warning(f"qBittorrent: Connected but error getting summary: {e}")
            else:
                error_msg = getattr(ctx.qbittorrent_monitor, '_last_error', None) or "Unknown error"
                logger.warning(f"qBittorrent: Not available - {error_msg}")
        else:
            logger.info("qBittorrent: Not configured (set QBITTORRENT_PASSWORD to enable)")
    else:
        logger.info("qBittorrent: Monitoring disabled")

    logger.info(f"Settings: Check every {cfg.check_interval:.1f}s | Cooldown {cfg.alert_cooldown:.1f}s")
    return qbittorrent_connected


def start_web_ui(ctx: MonitorContext) -> bool:
    """
    Start the dashboard when enabled.

    Returns:
        True when the web server thread was started
    """
    cfg = ctx.cfg
    web_server_started = False

    if cfg.web_ui_enabled:
        logger.info("Starting web server...")
        try:
            set_monitors(
                ctx.cpu_monitor,
                ctx.memory_monitor,
                ctx.system_monitor,
                ctx.gpu_monitor,
                ctx.disk_monitor,
                ctx.network_monitor,
                ctx.sensors_monitor,
                processes=ctx.process_monitor,
            )
            start_server(port=cfg.web_port, host=cfg.web_host)
            web_server_started = True
            logger.info(f"Web server started successfully on port {cfg.web_port}")
            logger.info(f"Access the web UI at http://localhost:{cfg.web_port}")
        except Exception as e:
            logger.warning(f"Failed to start web server: {e}")
    else:
        logger.info("Web UI disabled (set WEB_UI_ENABLED=true to enable)")

    return web_server_started


def send_startup_notifications(ctx: MonitorContext, qbittorrent_connected: bool) -> None:
    """Send the system-info embed and the current torrent list, when those webhooks are configured."""
    if ctx.webhook:
        logger.info("Sending system info to Discord...")
        try:
            start_time = time.time()
            all_info = ctx.system_monitor.get_all_info(include_top_processes=True)
            elapsed_time = time.time() - start_time
            success = send_webhook(
                lambda: ctx.webhook.send_system_info_comprehensive(all_info),
                operation_name="webhook send_system_info_comprehensive",
            )
            if success:
                logger.info(f"System info sent to Discord ({elapsed_time:.2f}s)")
            else:
                logger.warning("Failed to send system info to Discord")
        except KeyboardInterrupt:
            raise
        except Exception as e:
            logger.error(f"Error sending system info: {e}")

        if qbittorrent_connected and ctx.qbittorrent_webhook:
            try:
                all_startup_torrents = ctx.qbittorrent_monitor.get_all_torrents()
                downloading = ctx.qbittorrent_monitor.get_downloading_torrents(all_startup_torrents)
                seeding = ctx.qbittorrent_monitor.get_seeding_torrents(all_startup_torrents)

                if downloading or seeding:
                    logger.info(
                        f"Sending qBittorrent status to Discord ({len(downloading)} downloading, {len(seeding)} seeding)..."
                    )
                    success = send_webhook(
                        lambda: ctx.qbittorrent_webhook.send_torrent_list(downloading, seeding),
                        operation_name="qbittorrent_webhook send_torrent_list",
                    )
                    if success:
                        logger.info("qBittorrent status sent to Discord")
                    else:
                        logger.warning("Failed to send qBittorrent status to Discord")
                else:
                    logger.debug("No active torrents to report")
            except Exception as e:
                logger.warning(f"Error sending qBittorrent status to Discord: {e}")
