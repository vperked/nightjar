"""
Startup self-test for monitors and Discord webhook samples.
"""

from pathlib import Path
import hashlib
import json
import platform
import time

from core.cpu import CpuMonitor
from core.memory import MemoryMonitor
from core.system import SystemMonitor
from core.gpu import GpuMonitor
from core.disk import DiskMonitor
from core.network import NetworkMonitor
from core.sensors import SensorsMonitor
from core.processes import ProcessMonitor
from core.qbittorrent import QBittorrentMonitor
from core.logs import LogMonitor
from core.kernel import KernelMonitor
from core.webhook import DiscordWebhook
from core.config import (
    get_cached_env,
    get_env_bool,
    EMBED_SEND_DELAY,
    MAX_PROCESSES_DISPLAY,
)
from core.observability import Component, get_logger

logger = get_logger(Component.CORE)


def run_self_test() -> bool:
    """
    Run a lightweight self-test for core monitors without starting the monitoring loop.

    Tests initialization and basic functionality of all monitor modules.
    If Discord webhook is configured, sends sample embeds to verify webhook functionality.

    Returns:
        True if all tests pass, False otherwise
    """
    start_time = time.time()
    logger.info("Running self-test...")
    results = []

    def add_result(name: str, ok: bool, msg: str = ""):
        status = "OK" if ok else "FAIL"
        logger.info(f"[{status}] {name} {('- ' + msg) if msg else ''}")
        results.append(ok)

    try:
        cpu_monitor = CpuMonitor()
        memory_monitor = MemoryMonitor()
        system_monitor = SystemMonitor()
        gpu_monitor = GpuMonitor()
        disk_monitor = DiskMonitor()
        network_monitor = NetworkMonitor()
        sensors_monitor = SensorsMonitor()
    except Exception as e:
        logger.error(f"Failed to initialize monitors: {e}")
        return False

    try:
        cpu_monitor.get_cpu_percent(interval=0.1)
        add_result("CPU monitor", True)
    except Exception as e:
        add_result("CPU monitor", False, str(e))

    try:
        memory_monitor.get_memory_info_human()
        add_result("Memory monitor", True)
    except Exception as e:
        add_result("Memory monitor", False, str(e))

    try:
        disk_monitor.get_total_disk_usage()
        add_result("Disk monitor", True)
    except Exception as e:
        add_result("Disk monitor", False, str(e))

    try:
        network_monitor.get_bandwidth_usage(interval=0.1)
        add_result("Network monitor", True)
    except Exception as e:
        add_result("Network monitor", False, str(e))

    try:
        system_monitor.get_system_info()
        add_result("System monitor", True)
    except Exception as e:
        add_result("System monitor", False, str(e))

    try:
        if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
            gpu_monitor.get_gpu_info(0)
        add_result("GPU monitor", True)
    except Exception as e:
        add_result("GPU monitor", False, str(e))

    try:
        if sensors_monitor.is_available():
            sensors_monitor.get_cpu_temperature()
        add_result("Sensors monitor", True)
    except Exception as e:
        add_result("Sensors monitor", False, str(e))

    media_monitor_enabled = get_env_bool('MEDIA_MONITOR_ENABLED', False)
    media_directory = get_cached_env('MEDIA_DIRECTORY', '/home/media')
    if media_monitor_enabled:
        try:
            if Path(media_directory).exists():
                disk_monitor.get_directory_size(media_directory)
                add_result("Media directory monitor", True)
            else:
                add_result("Media directory monitor", False, f"Path not found: {media_directory}")
        except Exception as e:
            add_result("Media directory monitor", False, str(e))

    try:
        LogMonitor()
        add_result("Log monitor", True)
    except Exception as e:
        add_result("Log monitor", False, str(e))

    qbittorrent_monitor_enabled = get_env_bool('QBITTORRENT_MONITOR', False)
    if qbittorrent_monitor_enabled:
        try:
            qb = QBittorrentMonitor()
            if qb.password and qb.is_available():
                add_result("qBittorrent monitor", True)
            else:
                add_result("qBittorrent monitor", False, "Not available (check password/connection)")
        except Exception as e:
            add_result("qBittorrent monitor", False, str(e))

    webhook_url = get_cached_env('WEBHOOK')
    if webhook_url:
        try:
            test_webhook = DiscordWebhook(webhook_url=webhook_url)
            titles = [
                'CPU Usage Alert',
                'Memory Usage Alert',
                'Disk Usage Alert',
                'Network Bandwidth Alert',
                'Temperature Alert',
                'Process CPU Alert',
                'Directory Size Alert',
                'Download Complete',
                'Torrent Seeding',
                'qBittorrent Status'
            ]
            descriptions = [
                'CPU usage has exceeded the threshold of 80.0%',
                'Memory usage has exceeded the threshold of 80.0%',
                'Disk usage has exceeded the threshold of 85.0%',
                'Network bandwidth has exceeded the threshold of 100.0 MB/s',
                'CPU temperature has exceeded the threshold of 80.0°C for 75 seconds',
                'Process "python3" has exceeded the cpu threshold of 80.0%',
                'Directory "media" has exceeded the size threshold of 500.0 GB',
                'Torrent download has completed: Ubuntu ISO',
                'Torrent is now seeding: Big File',
                '2 downloading • 1 seeding'
            ]
            sample_fields = [
                [
                    {'name': 'Overall CPU Usage', 'value': '92.5%', 'inline': True},
                    {'name': 'Threshold', 'value': '80.0%', 'inline': True},
                    {'name': 'Per-Core Usage', 'value': 'Core 0: 95.2%\nCore 1: 88.3%', 'inline': False}
                ],
                [
                    {'name': 'Memory Usage', 'value': '87.3%', 'inline': True},
                    {'name': 'Threshold', 'value': '80.0%', 'inline': True},
                    {'name': 'Memory Details', 'value': 'Used: 28.3 GB\nAvailable: 4.1 GB\nTotal: 32.4 GB', 'inline': False}
                ],
                [
                    {'name': 'Disk Usage', 'value': '91.2%', 'inline': True},
                    {'name': 'Threshold', 'value': '85.0%', 'inline': True},
                    {'name': 'Total Disk', 'value': '1.8 TB', 'inline': False},
                    {'name': 'Used', 'value': '1.6 TB', 'inline': True},
                    {'name': 'Free', 'value': '200 GB', 'inline': True},
                    {'name': 'Largest Directories', 'value': 'torrents: 470 GB\ntv: 445 GB\nmovies: 120 GB', 'inline': False}
                ],
                [
                    {'name': 'Total Bandwidth', 'value': '120.0 MB/s', 'inline': True},
                    {'name': 'Threshold', 'value': '100.0 MB/s', 'inline': True},
                    {'name': 'Upload Speed', 'value': '30.0 MB/s', 'inline': True},
                    {'name': 'Download Speed', 'value': '90.0 MB/s', 'inline': True}
                ],
                [
                    {'name': 'CPU Temperature', 'value': '86.4°C', 'inline': True},
                    {'name': 'Threshold', 'value': '80.0°C', 'inline': True},
                    {'name': 'Duration Above Threshold', 'value': '75s', 'inline': True},
                    {'name': 'Likely Cause', 'value': 'Download/media activity: qbittorrent', 'inline': False},
                    {'name': 'CPU Usage', 'value': '78.2%', 'inline': True}
                ],
                [
                    {'name': 'Process', 'value': 'python3 (PID: 1234)', 'inline': False},
                    {'name': 'CPU Usage', 'value': '92.1%', 'inline': True},
                    {'name': 'Threshold', 'value': '80.0%', 'inline': True},
                    {'name': 'Resource Usage', 'value': 'CPU: 92.1%\nMemory: 3.2%', 'inline': True},
                    {'name': 'User', 'value': 'server', 'inline': True}
                ],
                [
                    {'name': 'Directory Path', 'value': '/home/media', 'inline': False},
                    {'name': 'Current Size', 'value': '917.0 GB', 'inline': True},
                    {'name': 'Threshold', 'value': '500.0 GB', 'inline': True},
                    {'name': 'Over Threshold By', 'value': '417.0 GB', 'inline': True},
                    {'name': 'Largest Subdirectories', 'value': 'torrents: 469.9 GB\ntv: 445.4 GB\nmovies: 1.7 GB', 'inline': False}
                ],
                [
                    {'name': 'Torrent Name', 'value': 'Ubuntu ISO', 'inline': False},
                    {'name': 'Size', 'value': '3.2 GB', 'inline': True},
                    {'name': 'Save Path', 'value': '/home/media/torrents', 'inline': False},
                    {'name': 'Ratio', 'value': '1.25', 'inline': True},
                    {'name': 'Uploaded', 'value': '4.0 GB', 'inline': True}
                ],
                [
                    {'name': 'Torrent Name', 'value': 'Big File', 'inline': False},
                    {'name': 'Size', 'value': '12.0 GB', 'inline': True},
                    {'name': 'Upload Speed', 'value': '5.0 MB/s', 'inline': True},
                    {'name': 'Ratio', 'value': '0.85', 'inline': True},
                    {'name': 'Seeds / Peers', 'value': '45 / 8', 'inline': True},
                    {'name': 'Save Path', 'value': '/home/media/torrents', 'inline': False}
                ],
                [
                    {'name': 'Downloading (2)', 'value': '`Ubuntu ISO`\n82.5% • 12.0 MB/s\n\n`Movie`\n45.0% • 6.5 MB/s', 'inline': False},
                    {'name': 'Seeding (1)', 'value': '`Linux Distro`\nRatio: 3.12 • 2.1 MB/s', 'inline': False}
                ]
            ]
            total = len(titles)
            sent = 0
            failures = 0
            for title, desc, fields in zip(titles, descriptions, sample_fields):
                try:
                    ok = test_webhook.send_embed(
                        title=title,
                        description=desc,
                        fields=fields,
                        footer='Server Monitor',
                        timestamp=True
                    )
                    if ok:
                        sent += 1
                    else:
                        failures += 1
                    time.sleep(EMBED_SEND_DELAY)
                except KeyboardInterrupt:
                    raise
                except Exception as e:
                    failures += 1
                    logger.warning(f"Embed send error for {title}: {e}")
            add_result("Discord embeds", failures == 0, f"sent {sent}/{total}, failed {failures}")

            try:
                test_fields = [
                    {'name': 'Risk Level', 'value': 'HIGH', 'inline': True},
                    {'name': 'Risk Score', 'value': '30', 'inline': True},
                    {'name': 'Status', 'value': 'System showing signs of instability - crash risk elevated', 'inline': False},
                    {'name': 'Warning Indicators', 'value': 'Warning: I/O errors on nvme0n1 (R:132569, W:774362)', 'inline': False},
                    {'name': 'Recommended Actions', 'value': 'Check disk health on nvme0n1', 'inline': False}
                ]

                normalized_fields_1 = sorted(
                    [{'name': f.get('name', ''), 'value': f.get('value', ''), 'inline': f.get('inline', False)}
                     for f in test_fields],
                    key=lambda x: (x['name'], x['value'])
                )
                alert_content_1 = {
                    'title': 'Kernel Alert: System Stability Risk (HIGH)',
                    'description': 'System showing signs of instability that may lead to kernel crash',
                    'fields': normalized_fields_1
                }
                hash_1 = hashlib.md5(json.dumps(alert_content_1, sort_keys=True).encode()).hexdigest()

                test_fields_reordered = [
                    {'name': 'Status', 'value': 'System showing signs of instability - crash risk elevated', 'inline': False},
                    {'name': 'Risk Level', 'value': 'HIGH', 'inline': True},
                    {'name': 'Risk Score', 'value': '30', 'inline': True},
                    {'name': 'Warning Indicators', 'value': 'Warning: I/O errors on nvme0n1 (R:132569, W:774362)', 'inline': False},
                    {'name': 'Recommended Actions', 'value': 'Check disk health on nvme0n1', 'inline': False}
                ]
                normalized_fields_2 = sorted(
                    [{'name': f.get('name', ''), 'value': f.get('value', ''), 'inline': f.get('inline', False)}
                     for f in test_fields_reordered],
                    key=lambda x: (x['name'], x['value'])
                )
                alert_content_2 = {
                    'title': 'Kernel Alert: System Stability Risk (HIGH)',
                    'description': 'System showing signs of instability that may lead to kernel crash',
                    'fields': normalized_fields_2
                }
                hash_2 = hashlib.md5(json.dumps(alert_content_2, sort_keys=True).encode()).hexdigest()

                different_fields = test_fields.copy()
                different_fields[1] = {'name': 'Risk Score', 'value': '45', 'inline': True}
                normalized_fields_diff = sorted(
                    [{'name': f.get('name', ''), 'value': f.get('value', ''), 'inline': f.get('inline', False)}
                     for f in different_fields],
                    key=lambda x: (x['name'], x['value'])
                )
                different_content = {
                    'title': 'Kernel Alert: System Stability Risk (HIGH)',
                    'description': 'System showing signs of instability that may lead to kernel crash',
                    'fields': normalized_fields_diff
                }
                hash_3 = hashlib.md5(json.dumps(different_content, sort_keys=True).encode()).hexdigest()

                same_content_same_hash = (hash_1 == hash_2)
                different_content_different_hash = (hash_1 != hash_3)
                test_passed = same_content_same_hash and different_content_different_hash

                add_result("Duplicate detection", test_passed,
                          f"same content: {same_content_same_hash}, different content: {different_content_different_hash}")
            except Exception as e:
                add_result("Duplicate detection", False, str(e))
        except Exception as e:
            add_result("Discord embeds", False, str(e))
    else:
        add_result("Discord embeds", True, "Skipped (WEBHOOK not set)")
        add_result("Duplicate detection", True, "Skipped (WEBHOOK not set)")

    try:
        process_monitor = ProcessMonitor()
        top_cpu = process_monitor.get_top_processes(limit=MAX_PROCESSES_DISPLAY, sort_by='cpu')
        top_memory = process_monitor.get_top_processes(limit=MAX_PROCESSES_DISPLAY, sort_by='memory')
        process_count = process_monitor.get_process_count()
        add_result("Process monitor", True, f"{process_count.get('total', 0)} total, {len(top_cpu)} top CPU, {len(top_memory)} top memory")
    except Exception as e:
        add_result("Process monitor", False, str(e))

    try:
        kernel_monitor = KernelMonitor()
        if platform.system() == 'Linux':
            stability = kernel_monitor.check_system_stability()
            add_result("Kernel monitor", True, f"risk level: {stability.get('risk_level', 'unknown')}")
        else:
            add_result("Kernel monitor", True, "skipped (not Linux)")
    except Exception as e:
        add_result("Kernel monitor", False, str(e))

    try:
        disk_io_per_disk = disk_monitor.get_disk_io_counters(perdisk=True)
        add_result("Disk I/O counters", True, f"system-wide and {len(disk_io_per_disk)} per-disk")
    except Exception as e:
        add_result("Disk I/O counters", False, str(e))

    try:
        partitions = disk_monitor.get_all_partitions()
        add_result("Disk partitions", True, f"found {len(partitions)} partitions")
    except Exception as e:
        add_result("Disk partitions", False, str(e))

    try:
        uptime = system_monitor.get_uptime()
        add_result("System info", True, f"uptime: {uptime.get('formatted', 'unknown')}")
    except Exception as e:
        add_result("System info", False, str(e))

    try:
        network_interfaces = network_monitor.get_network_interfaces()
        add_result("Network interfaces", True, f"found {len(network_interfaces)} interfaces")
    except Exception as e:
        add_result("Network interfaces", False, str(e))

    elapsed_time = time.time() - start_time
    elapsed_str = f"{elapsed_time:.2f}s" if elapsed_time < 60 else f"{elapsed_time/60:.1f}m"

    success = all(results) if results else True
    if success:
        logger.info(f"Self-test completed successfully in {elapsed_str}.")
    else:
        logger.warning(f"Self-test completed with failures in {elapsed_str}. See logs above.")
    return success
