"""
Reads the monitors once per cycle and fills a Snapshot.
"""

import time

import psutil

from core.observability import Component, get_logger
from core.runtime.context import MonitorContext
from core.runtime.snapshot import Snapshot
from core.timeout import run_with_timeout

logger = get_logger(Component.CORE)

NETWORK_CHECK_INTERVAL = 2


class Collector:
    """Gathers CPU, memory, GPU, disk, temperature and network readings."""

    def __init__(self, ctx: MonitorContext):
        self.ctx = ctx
        self.cfg = ctx.cfg
        self.network_check_counter = 0

    def collect_core(self, snap: Snapshot) -> None:
        """CPU, memory, GPU and disk readings; failures are reported and replaced by zeros."""
        ctx = self.ctx
        try:
            ctx.metrics_collector.start_monitor_execution('cpu')
            snap.cpu_per_core = ctx.cpu_monitor.get_cpu_percent_per_core(interval=0.0)
            snap.cpu_percent = sum(snap.cpu_per_core) / len(snap.cpu_per_core) if snap.cpu_per_core else 0.0
            ctx.metrics_collector.end_monitor_execution('cpu')
        except Exception as e:
            ctx.metrics_collector.end_monitor_execution('cpu')
            ctx.metrics_collector.record_error('cpu_monitor')
            logger.error(f"Error getting CPU metrics: {e}")
            ctx.report_error(e, context="CPU metrics collection", include_traceback=True)
            snap.cpu_percent = 0.0
            snap.cpu_per_core = []

        ctx.heartbeat()

        try:
            ctx.metrics_collector.start_monitor_execution('memory')
            snap.memory_info = ctx.memory_monitor.get_memory_info()
            snap.memory_percent = snap.memory_info.get('percent', 0.0) if snap.memory_info else 0.0
            snap.swap_info = ctx.memory_monitor.get_swap_info()
            ctx.metrics_collector.end_monitor_execution('memory')
        except Exception as e:
            ctx.metrics_collector.end_monitor_execution('memory')
            ctx.metrics_collector.record_error('memory_monitor')
            logger.error(f"Error getting memory metrics: {e}")
            ctx.report_error(e, context="Memory metrics collection", include_traceback=True)
            snap.memory_percent = 0.0
            snap.memory_info = {}
            snap.swap_info = {}

        ctx.heartbeat()

        snap.gpu_utilization = None
        snap.gpu_data = None
        try:
            if ctx.gpu_monitor.nvidia_smi_available:
                snap.gpu_data = run_with_timeout(
                    lambda: ctx.gpu_monitor.get_gpu_comprehensive(0),
                    timeout=20.0,
                    default_value={},
                    operation_name="GPU comprehensive data collection",
                )
                if snap.gpu_data:
                    snap.gpu_utilization = snap.gpu_data.get('utilization')
        except Exception as e:
            logger.debug(f"GPU metrics failed: {e}")

        try:
            ctx.metrics_collector.start_monitor_execution('disk')
            snap.all_partitions = run_with_timeout(
                ctx.disk_monitor.get_all_partitions,
                timeout=10.0,
                default_value=[],
                operation_name="Disk partitions collection",
            )

            snap.total_disk = ctx.disk_monitor.get_total_disk_usage(partitions=snap.all_partitions)
            snap.disk_percent = snap.total_disk.get('percent', 0.0) if snap.total_disk else 0.0
            ctx.metrics_collector.end_monitor_execution('disk')
        except Exception as e:
            ctx.metrics_collector.end_monitor_execution('disk')
            ctx.metrics_collector.record_error('disk_monitor')
            logger.error(f"Error getting disk usage: {e}")
            ctx.report_error(e, context="Disk usage collection", include_traceback=True)
            snap.all_partitions = []
            snap.total_disk = {}
            snap.disk_percent = 0.0

        ctx.heartbeat()

    def collect_peripherals(self, snap: Snapshot) -> None:
        """Temperature, network bandwidth, disk I/O and the freeze-monitor feed."""
        cfg = self.cfg
        ctx = self.ctx
        snap.cpu_temp = None
        try:
            if ctx.sensors_monitor.is_available():
                snap.cpu_temp = ctx.sensors_monitor.get_cpu_temperature()
        except Exception as e:
            logger.debug(f"Temperature failed: {e}")
            snap.cpu_temp = None

        ctx.heartbeat()

        snap.network_bandwidth = None
        snap.bandwidth = None
        self.network_check_counter += 1
        snap.disk_io_data = None
        snap.network_io_data = None
        if self.network_check_counter >= NETWORK_CHECK_INTERVAL:
            self.network_check_counter = 0
            try:
                snap.bandwidth = ctx.network_monitor.get_bandwidth_usage(interval=1.0)
                if snap.bandwidth:
                    total_bandwidth = snap.bandwidth.get('bytes_sent_per_sec', 0) + snap.bandwidth.get(
                        'bytes_recv_per_sec', 0
                    )
                    mb_per_sec = 1024 * 1024
                    if mb_per_sec > 0:
                        snap.network_bandwidth = total_bandwidth / mb_per_sec
                snap.network_io_data = ctx.network_monitor.get_total_network_io()
                ctx.heartbeat()
            except (ValueError, ZeroDivisionError) as e:
                logger.debug(f"Network bandwidth failed: {e}")
            except Exception as e:
                logger.debug(f"Network bandwidth failed: {e}")

        try:
            snap.disk_io_data = ctx.system_monitor.get_disk_io()
        except (OSError, psutil.Error) as e:
            logger.debug(f"Disk I/O failed: {e}")

        if cfg.freeze_monitor_enabled and ctx.freeze_monitor:
            ctx.freeze_monitor.update_metrics(
                cpu_percent=snap.cpu_percent,
                memory_percent=snap.memory_percent,
                disk_io=snap.disk_io_data,
                network_io=snap.network_io_data,
            )

        ctx.heartbeat()

    def log_status_line(self, snap: Snapshot) -> None:
        """Log the one-line summary of this cycle's readings."""
        cfg = self.cfg
        ctx = self.ctx
        timestamp = time.strftime("%Y-%m-%d %H:%M:%S")

        disk_status_parts = []
        try:
            if snap.all_partitions:
                for partition in snap.all_partitions:
                    mountpoint = partition.get('mountpoint', partition.get('device', 'unknown'))
                    percent = partition.get('percent', 0.0)
                    if cfg.is_windows:
                        display_name = mountpoint.rstrip('\\').split('\\')[-1] or mountpoint[:2]
                        if display_name.endswith(':'):
                            display_name = display_name[:-1]
                    else:
                        display_name = mountpoint.split('/')[-1] if mountpoint != '/' else 'root'
                        if not display_name:
                            display_name = mountpoint
                    disk_status_parts.append(f"{display_name}: {percent:.1f}%")
            else:
                disk_status_parts.append(f"{snap.disk_percent:.1f}%")
        except Exception as e:
            logger.debug(f"Disk status format failed: {e}")
            disk_status_parts.append(f"{snap.disk_percent:.1f}%")

        disk_status = " | ".join(disk_status_parts) if disk_status_parts else f"{snap.disk_percent:.1f}%"

        metrics_parts = [
            f"[{timestamp}] CPU: {snap.cpu_percent:.1f}%",
            f"Memory: {snap.memory_percent:.1f}%",
            f"Disk: {disk_status}",
        ]
        if snap.gpu_utilization is not None:
            metrics_parts.append(f"GPU: {snap.gpu_utilization['gpu']:.1f}%")
        if snap.cpu_temp is not None:
            metrics_parts.append(f"Temp: {snap.cpu_temp:.1f}C")
        if snap.network_bandwidth is not None:
            metrics_parts.append(f"Network: {snap.network_bandwidth:.1f} MB/s")

        metrics_str = " | ".join(metrics_parts)
        logger.info(metrics_str)

        ctx.heartbeat()
