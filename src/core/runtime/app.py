"""
Monitoring application entry point: wires startup, the monitoring loop and shutdown together.
"""

import os
import time
from typing import Optional

from core.alerts import AlertGates
from core.config import DEFAULT_ALERT_BACKOFF, DEFAULT_ALERT_MAX_COOLDOWN, DEFAULT_ALERT_RECOVERY_SECONDS, get_env_float
from core.cpu import CpuMonitor
from core.disk import DiskMonitor
from core.freeze import FreezeMonitor
from core.gpu import GpuMonitor
from core.kernel import KernelMonitor
from core.logs import LogMonitor
from core.memory import MemoryMonitor
from core.metrics import MetricsCollector
from core.network import NetworkMonitor
from core.observability import Component, get_logger
from core.processes import ProcessMonitor
from core.qbittorrent import QBittorrentMonitor
from core.runtime.context import MonitorContext
from core.runtime.loop import MonitoringLoop
from core.runtime.settings import RuntimeSettings, load_settings
from core.runtime.shutdown import ShutdownState, finalize, handle_crash, handle_interrupt
from core.runtime.startup import (
    build_monitors,
    connect_webhooks,
    detect_configuration,
    log_startup_summary,
    send_startup_notifications,
    start_web_ui,
)
from core.sensors import SensorsMonitor
from core.system import SystemMonitor
from core.watchdog import WatchdogMonitor

logger = get_logger(Component.CORE)


def _alert_gates(cfg: RuntimeSettings) -> AlertGates:
    """Build the shared alert throttle from the cooldown settings."""
    return AlertGates(
        base_cooldown=cfg.alert_cooldown,
        max_cooldown=get_env_float('ALERT_MAX_COOLDOWN', DEFAULT_ALERT_MAX_COOLDOWN, min_val=0.0),
        backoff=get_env_float('ALERT_BACKOFF', DEFAULT_ALERT_BACKOFF, min_val=1.0, max_val=10.0),
        # Several check intervals must pass with the condition absent before an
        # episode counts as recovered, otherwise slow polling would never debounce.
        recovery_seconds=max(
            get_env_float('ALERT_RECOVERY_SECONDS', DEFAULT_ALERT_RECOVERY_SECONDS, min_val=0.0),
            3 * cfg.check_interval,
        ),
    )


def run_monitor(
    cpu_monitor: Optional[CpuMonitor] = None,
    memory_monitor: Optional[MemoryMonitor] = None,
    system_monitor: Optional[SystemMonitor] = None,
    gpu_monitor: Optional[GpuMonitor] = None,
    disk_monitor: Optional[DiskMonitor] = None,
    network_monitor: Optional[NetworkMonitor] = None,
    sensors_monitor: Optional[SensorsMonitor] = None,
    process_monitor: Optional[ProcessMonitor] = None,
    qbittorrent_monitor: Optional[QBittorrentMonitor] = None,
    log_monitor: Optional[LogMonitor] = None,
    kernel_monitor: Optional[KernelMonitor] = None,
):
    """
    Run continuous server monitoring with Discord alerts until interrupted.

    Every monitor argument is optional; omitted ones are constructed. Tests inject fakes.
    """
    logger.info("Preserving bytecode cache on startup")
    logger.info("Starting Server Monitor...")

    detect_configuration()
    monitors = build_monitors(
        cpu_monitor=cpu_monitor,
        memory_monitor=memory_monitor,
        system_monitor=system_monitor,
        gpu_monitor=gpu_monitor,
        disk_monitor=disk_monitor,
        network_monitor=network_monitor,
        sensors_monitor=sensors_monitor,
        process_monitor=process_monitor,
        qbittorrent_monitor=qbittorrent_monitor,
        log_monitor=log_monitor,
        kernel_monitor=kernel_monitor,
    )
    qbittorrent_monitor_enabled = monitors.pop('qbittorrent_monitor_enabled')
    webhook, qbittorrent_webhook, disk_webhook, crash_webhook = connect_webhooks(qbittorrent_monitor_enabled)
    metrics_collector = MetricsCollector()

    cfg = load_settings()
    ctx = MonitorContext(
        cfg=cfg,
        **monitors,
        webhook=webhook,
        disk_webhook=disk_webhook,
        crash_webhook=crash_webhook,
        qbittorrent_webhook=qbittorrent_webhook,
        metrics_collector=metrics_collector,
        alert_gates=_alert_gates(cfg),
        error_log_file=os.getenv('ERROR_LOG_FILE', 'server_monitor_errors.log'),
    )

    ctx.watchdog_monitor = WatchdogMonitor(
        timeout=cfg.monitoring_watchdog_timeout,
        auto_restart=cfg.watchdog_auto_restart,
        restart_cooldown=cfg.watchdog_restart_cooldown,
        service_name=cfg.systemd_service_name,
        alert_cooldown=cfg.alert_cooldown,
        webhook=webhook,
        crash_webhook=crash_webhook,
    )
    ctx.watchdog_monitor.start()

    if cfg.freeze_monitor_enabled:
        ctx.freeze_monitor = FreezeMonitor(check_interval=cfg.check_interval)
        ctx.freeze_monitor.set_freeze_threshold(cfg.freeze_threshold)
        ctx.freeze_monitor.set_heartbeat_timeout(cfg.freeze_heartbeat_timeout)

    qbittorrent_connected = log_startup_summary(ctx)
    web_server_started = start_web_ui(ctx)
    send_startup_notifications(ctx, qbittorrent_connected)
    logger.info("Starting continuous monitoring (Press Ctrl+C to stop)")

    started_at = time.time()
    state = ShutdownState()
    state.install()
    loop = MonitoringLoop(ctx, started_at)

    try:
        loop.run()
    except KeyboardInterrupt:
        handle_interrupt(ctx, state, started_at, web_server_started)
    except Exception as e:
        handle_crash(ctx, state, e, started_at, web_server_started)
    finally:
        finalize(state)
