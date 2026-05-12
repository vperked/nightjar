"""
Temperature analysis logic.

Analyzes system metrics to determine likely causes of high CPU temperature.
"""

from typing import Dict, List, Optional, Tuple
from util.util import BYTES_PER_MB, BYTES_PER_GB
from core.config import MAX_REASONS_DISPLAY, MAX_PROCESSES_DISPLAY


def analyze_temperature_reason(
    cpu_percent: float,
    cpu_freq: Dict[str, float],
    cpu_per_core: List[float],
    top_processes: List[Dict],
    load_avg: Optional[Tuple[float, float, float]],
    fan_speeds: Optional[List[Dict]] = None,
    disk_io: Optional[Dict] = None,
    network_io: Optional[Dict] = None,
    gpu_temp: Optional[float] = None
) -> str:
    """
    Analyze the likely reason for high CPU temperature.

    Args:
        cpu_percent: Overall CPU usage percentage
        cpu_freq: CPU frequency information dictionary
        cpu_per_core: Per-core CPU usage percentages
        top_processes: List of top CPU-consuming processes
        load_avg: System load average tuple (1min, 5min, 15min) or None
        fan_speeds: Optional list of fan speed information
        disk_io: Optional disk I/O statistics
        network_io: Optional network I/O statistics
        gpu_temp: Optional GPU temperature for comparison

    Returns:
        Human-readable string describing the likely cause
    """
    reasons = []

    if cpu_percent >= 80:
        reasons.append(f"Very high CPU usage ({cpu_percent:.1f}%)")
    elif cpu_percent >= 60:
        reasons.append(f"High CPU usage ({cpu_percent:.1f}%)")
    elif cpu_percent >= 40:
        reasons.append(f"Moderate CPU usage ({cpu_percent:.1f}%)")

    freq_current = cpu_freq.get('current', 0)
    freq_max = cpu_freq.get('max', 0)
    if freq_max > 0:
        freq_percent = (freq_current / freq_max) * 100
        if freq_percent >= 95:
            reasons.append(f"CPU running at maximum frequency ({freq_current:.0f} MHz)")
        elif freq_percent >= 85:
            reasons.append(f"CPU running at high frequency ({freq_percent:.0f}% of max)")

    if cpu_per_core and len(cpu_per_core) > 0:
        max_core = max(cpu_per_core)
        if max_core >= 90:
            high_cores = sum(1 for core in cpu_per_core if core >= 80)
            if high_cores == 1:
                reasons.append("Single core under heavy load (single-threaded workload)")
            elif high_cores <= len(cpu_per_core) // 2:
                reasons.append(f"{high_cores} cores under heavy load")

    download_processes = []
    download_keywords = ['qbittorrent', 'transmission', 'deluge', 'rtorrent', 'aria2',
                        'wget', 'curl', 'sabnzbd', 'nzbget', 'youtube-dl', 'yt-dlp',
                        'plex', 'jellyfin', 'emby', 'radarr', 'sonarr', 'lidarr',
                        'jackett', 'prowlarr', 'download', 'torrent']

    if top_processes:
        for proc in top_processes:
            proc_name = proc.get('name', '').lower()
            proc_cmdline = ' '.join(proc.get('cmdline', [])).lower() if proc.get('cmdline') else ''
            for keyword in download_keywords:
                if keyword in proc_name or keyword in proc_cmdline:
                    download_processes.append(proc)
                    break

    if download_processes:
        download_names = [p.get('name', 'Unknown') for p in download_processes[:MAX_REASONS_DISPLAY]]
        if len(download_names) == 1:
            reasons.append(f"Download/media activity: {download_names[0]}")
        else:
            reasons.append(f"Download/media activity: {', '.join(download_names[:2])}")

    if top_processes:
        high_cpu_procs = [p for p in top_processes[:MAX_PROCESSES_DISPLAY] if p.get('cpu_percent', 0) > 5 and p not in download_processes]
        if high_cpu_procs:
            proc_details = []
            for proc in high_cpu_procs[:MAX_REASONS_DISPLAY]:
                name = proc.get('name', 'Unknown')
                cpu_usage = proc.get('cpu_percent', 0)
                proc_details.append(f"{name} ({cpu_usage:.1f}%)")
            if len(proc_details) == 1:
                reasons.append(f"Process: {proc_details[0]}")
            else:
                reasons.append(f"Top processes: {', '.join(proc_details)}")

    if load_avg:
        load_1min = load_avg[0]
        cpu_count = len(cpu_per_core) if cpu_per_core else 1
        if load_1min > cpu_count * 1.5:
            reasons.append(f"High system load ({load_1min:.1f} vs {cpu_count} cores)")
        elif load_1min > cpu_count:
            reasons.append(f"Elevated system load ({load_1min:.1f} vs {cpu_count} cores)")

    if fan_speeds:
        fan_speed_list = [f.get('speed_rpm', 0) for f in fan_speeds if f.get('speed_rpm', 0) > 0]
        if fan_speed_list:
            avg_fan_speed = sum(fan_speed_list) / len(fan_speed_list)
            min_fan_speed = min(fan_speed_list)
            if min_fan_speed < 500:
                reasons.append(f"Very low fan speeds (min: {min_fan_speed:.0f} RPM) - cooling issue likely")
            elif avg_fan_speed < 1000:
                reasons.append(f"Low fan speeds (avg: {avg_fan_speed:.0f} RPM) - possible cooling issue")
        else:
            reasons.append("No fan speed data available - cannot verify cooling")

    if disk_io:
        read_bytes = disk_io.get('read_bytes', 0)
        write_bytes = disk_io.get('write_bytes', 0)
        if write_bytes > 5 * BYTES_PER_GB:
            gb_written = write_bytes / BYTES_PER_GB
            if download_processes:
                reasons.append(f"Writing downloaded data to disk ({gb_written:.1f} GB written)")
            else:
                reasons.append(f"High disk write activity ({gb_written:.1f} GB written)")
        elif read_bytes > 5 * BYTES_PER_GB:
            gb_read = read_bytes / BYTES_PER_GB
            reasons.append(f"High disk read activity ({gb_read:.1f} GB read)")
        elif read_bytes > 0 or write_bytes > 0:
            total_io = read_bytes + write_bytes
            if total_io > 10 * BYTES_PER_GB:
                reasons.append("High disk I/O activity detected")

    if network_io:
        bytes_sent = network_io.get('bytes_sent', 0)
        bytes_recv = network_io.get('bytes_recv', 0)
        if bytes_recv > 50 * BYTES_PER_MB:
            mb_recv = bytes_recv / BYTES_PER_MB
            if download_processes:
                reasons.append(f"Active download in progress ({mb_recv:.1f} MB received)")
            else:
                reasons.append(f"High network download activity ({mb_recv:.1f} MB received)")
        elif bytes_sent > 50 * BYTES_PER_MB:
            mb_sent = bytes_sent / BYTES_PER_MB
            reasons.append(f"High network upload activity ({mb_sent:.1f} MB sent)")
        total_net = bytes_sent + bytes_recv
        if total_net > 100 * BYTES_PER_MB and not download_processes:
            reasons.append("High network activity detected")

    if gpu_temp is not None and gpu_temp > 0:
        if gpu_temp > 70:
            reasons.append(f"GPU also hot ({gpu_temp:.1f}°C) - possible ambient temperature or case airflow issue")

    if not reasons:
        if cpu_percent > 30:
            reasons.append(f"Sustained CPU activity ({cpu_percent:.1f}%) without specific high-load process")
        elif cpu_percent > 10:
            reasons.append(f"Low-moderate CPU usage ({cpu_percent:.1f}%) with high temperature - check cooling system")
        else:
            reasons.append(f"Very low CPU usage ({cpu_percent:.1f}%) with high temperature - likely cooling system failure or ambient temperature issue")

    return " | ".join(reasons[:4])
