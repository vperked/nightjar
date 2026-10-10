"""
Threshold alerts for CPU, memory, GPU, temperature and network.
"""

from typing import Any

from core.format import format_bytes_per_second
from core.observability import Component, get_logger
from core.runtime.handlers.base import Handler
from core.runtime.snapshot import Snapshot
from core.temperature import analyze_temperature_reason



class CpuAlert(Handler):
    """Alert when overall CPU usage crosses the threshold."""

    log = get_logger(Component.CPU)

    def check(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        if snap.cpu_percent < cfg.cpu_threshold:
            return
        self.raise_alert(
            'cpu',
            snap.current_time,
            f"CPU: {snap.cpu_percent:.1f}% (threshold: {cfg.cpu_threshold:.1f}%)",
            lambda: ctx.deliver(
                ctx.webhook,
                'send_cpu_alert',
                "webhook send_cpu_alert",
                cpu_percent=snap.cpu_percent,
                threshold=cfg.cpu_threshold,
                per_core=snap.cpu_per_core,
            ),
            webhook=ctx.webhook,
            cooldown_label='CPU',
            alert_type='cpu',
            record_result=True,
        )

    def log_hot_cores(self, snap: Snapshot) -> None:
        """Debug-log individual cores at or above the threshold."""
        threshold = self.cfg.cpu_threshold
        hot = [f"Core {i}: {usage:.1f}%" for i, usage in enumerate(snap.cpu_per_core) if usage >= threshold]
        if hot:
            self.log.debug(f"High core usage: {', '.join(hot)}")


class MemoryAlert(Handler):
    """Alert when memory usage crosses the threshold."""

    log = get_logger(Component.MEMORY)

    def check(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        if snap.memory_percent < cfg.memory_threshold:
            return
        self.raise_alert(
            'memory',
            snap.current_time,
            f"Memory: {snap.memory_percent:.1f}% (threshold: {cfg.memory_threshold:.1f}%)",
            lambda: ctx.deliver(
                ctx.webhook,
                'send_memory_alert',
                "webhook send_memory_alert",
                memory_percent=snap.memory_percent,
                threshold=cfg.memory_threshold,
                memory_info=snap.memory_info,
                swap_info=snap.swap_info,
            ),
            webhook=ctx.webhook,
            cooldown_label='Memory',
            alert_type='memory',
            record_result=True,
        )


class GpuAlert(Handler):
    """Alert when GPU utilization crosses the threshold."""

    log = get_logger(Component.GPU)

    def check(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        if snap.gpu_utilization is None or snap.gpu_utilization['gpu'] < cfg.gpu_threshold:
            return
        gpu = snap.gpu_data or {}
        self.raise_alert(
            'gpu',
            snap.current_time,
            f"GPU: {snap.gpu_utilization['gpu']:.1f}% (threshold: {cfg.gpu_threshold:.1f}%)",
            lambda: ctx.deliver(
                ctx.webhook,
                'send_gpu_alert',
                "webhook send_gpu_alert",
                gpu_utilization=snap.gpu_utilization['gpu'],
                threshold=cfg.gpu_threshold,
                gpu_info=gpu.get('info'),
                memory_info=gpu.get('memory'),
                temperature=gpu.get('temperature'),
                power=gpu.get('power'),
            ),
            webhook=ctx.webhook if snap.gpu_data else None,
            cooldown_label='GPU',
            alert_type='gpu',
        )


class TemperatureAlert(Handler):
    """Alert when the CPU stays hot for the configured duration."""

    log = get_logger(Component.TEMPERATURE)

    def __init__(self, ctx):
        super().__init__(ctx)
        self.temp_high_start_time = None

    def check(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        now = snap.current_time
        if snap.cpu_temp is None or snap.cpu_temp < cfg.temp_threshold:
            self.temp_high_start_time = None
            return

        if self.temp_high_start_time is None:
            self.temp_high_start_time = now
        duration = now - self.temp_high_start_time
        if duration < cfg.temp_alert_duration:
            self.log.debug(
                f"Temperature {snap.cpu_temp:.1f}C, waiting {cfg.temp_alert_duration - duration:.0f}s before alert"
            )
            return

        def send() -> bool:
            details = self._diagnose(snap)
            self.log.info(f"    Likely cause: {details['reason']}")
            return ctx.deliver(
                ctx.webhook,
                'send_temperature_alert',
                "webhook send_temperature_alert",
                temperature=snap.cpu_temp,
                threshold=cfg.temp_threshold,
                duration=duration,
                reason=details['reason'],
                cpu_percent=snap.cpu_percent,
                cpu_freq=details['cpu_freq'],
                top_processes=details['top_processes'],
                fan_speeds=details['fans'],
            )

        self.raise_alert(
            'temp',
            now,
            f"Temperature: {snap.cpu_temp:.1f}C (threshold: {cfg.temp_threshold:.1f}C) for {duration:.0f}s",
            send,
            webhook=ctx.webhook,
            cooldown_label='Temperature',
            alert_type='temperature',
            record_result=True,
        )

    def _diagnose(self, snap: Snapshot) -> dict[str, Any]:
        """Gather what is needed to explain the heat: frequency, load, top processes, fans and I/O."""
        ctx = self.ctx
        cpu_freq = ctx.cpu_monitor.get_cpu_freq()
        load_avg = ctx.cpu_monitor.get_cpu_load_average()
        if snap.top_cpu_processes is None:
            snap.top_cpu_processes = ctx.process_monitor.get_top_processes(limit=5, sort_by='cpu')
        fans = ctx.sensors_monitor.get_fan_speeds() or []

        disk_io = snap.disk_io_data
        if disk_io is None:
            try:
                disk_io = ctx.system_monitor.get_disk_io()
            except Exception:
                pass
        network_io = snap.network_io_data
        if network_io is None:
            try:
                network_io = ctx.network_monitor.get_total_network_io()
            except Exception:
                pass

        reason = analyze_temperature_reason(
            cpu_percent=snap.cpu_percent,
            cpu_freq=cpu_freq,
            cpu_per_core=snap.cpu_per_core,
            top_processes=snap.top_cpu_processes,
            load_avg=load_avg,
            fan_speeds=fans,
            disk_io=disk_io,
            network_io=network_io,
            gpu_temp=snap.gpu_data.get('temperature') if snap.gpu_data else None,
        )
        return {'reason': reason, 'cpu_freq': cpu_freq, 'top_processes': snap.top_cpu_processes, 'fans': fans}


class NetworkAlert(Handler):
    """Alert when total network bandwidth crosses the threshold."""

    log = get_logger(Component.NETWORK)

    def check(self, snap: Snapshot) -> None:
        cfg, ctx = self.cfg, self.ctx
        threshold = cfg.network_bandwidth_threshold
        if snap.network_bandwidth is None or snap.network_bandwidth < threshold:
            return

        def send() -> bool:
            fields = [
                {'name': 'Total Bandwidth', 'value': f'{snap.network_bandwidth:.1f} MB/s', 'inline': True},
                {'name': 'Threshold', 'value': f'{threshold:.1f} MB/s', 'inline': True},
            ]
            if snap.bandwidth:
                fields += [
                    {
                        'name': 'Upload Speed',
                        'value': format_bytes_per_second(snap.bandwidth.get('bytes_sent_per_sec', 0)),
                        'inline': True,
                    },
                    {
                        'name': 'Download Speed',
                        'value': format_bytes_per_second(snap.bandwidth.get('bytes_recv_per_sec', 0)),
                        'inline': True,
                    },
                ]
            return ctx.deliver(
                ctx.webhook,
                'send_embed',
                "webhook send_embed",
                title='Network Bandwidth Alert',
                description=f'Network bandwidth has exceeded the threshold of {threshold:.1f} MB/s',
                fields=fields,
                footer='Server Monitor',
                timestamp=True,
            )

        self.raise_alert(
            'network',
            snap.current_time,
            f"Network: {snap.network_bandwidth:.1f} MB/s (threshold: {threshold:.1f} MB/s)",
            send,
            webhook=ctx.webhook,
            cooldown_label='Network',
        )
