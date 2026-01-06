from monitor.cpu import CpuMonitor
from monitor.memory import MemoryMonitor
from monitor.system import SystemMonitor
from monitor.gpu import GpuMonitor
from monitor.disk import DiskMonitor
from monitor.network import NetworkMonitor
from monitor.sensors import SensorsMonitor
from monitor.processes import ProcessMonitor
from monitor.freeze import FreezeMonitor
from monitor.qbittorrent import QBittorrentMonitor
from discord.webhook import DiscordWebhook
from util.util import format_bytes, format_bytes_per_second
from util.systemd import install_systemd_service, check_systemd_available, is_root
from cache.cache import clear_cache_on_startup, clear_cache_on_exit
from logger import get_logger
from web.server import set_monitors, start_server, stop_server
from typing import Optional, Tuple, Dict, List
import os
import sys
import time
import psutil
from dotenv import load_dotenv

load_dotenv()

logger = get_logger()


def analyze_temperature_reason(cpu_percent: float, cpu_freq: Dict, cpu_per_core: List[float],
                               top_processes: List[Dict], load_avg: Optional[Tuple[float, float, float]],
                               fan_speeds: Optional[List[Dict]] = None, 
                               disk_io: Optional[Dict] = None,
                               network_io: Optional[Dict] = None,
                               gpu_temp: Optional[float] = None) -> str:
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
    
    if cpu_per_core:
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
        download_names = [p.get('name', 'Unknown') for p in download_processes[:3]]
        if len(download_names) == 1:
            reasons.append(f"Download/media activity: {download_names[0]}")
        else:
            reasons.append(f"Download/media activity: {', '.join(download_names[:2])}")
    
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
        download_names = [p.get('name', 'Unknown') for p in download_processes[:3]]
        if len(download_names) == 1:
            reasons.append(f"Download/media activity: {download_names[0]}")
        else:
            reasons.append(f"Download/media activity: {', '.join(download_names[:2])}")
    
    if top_processes:
        high_cpu_procs = [p for p in top_processes[:5] if p.get('cpu_percent', 0) > 5 and p not in download_processes]
        if high_cpu_procs:
            proc_details = []
            for proc in high_cpu_procs[:3]:
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
        if write_bytes > 5 * 1024 * 1024 * 1024:
            gb_written = write_bytes / (1024 * 1024 * 1024)
            if download_processes:
                reasons.append(f"Writing downloaded data to disk ({gb_written:.1f} GB written)")
            else:
                reasons.append(f"High disk write activity ({gb_written:.1f} GB written)")
        elif read_bytes > 5 * 1024 * 1024 * 1024:
            gb_read = read_bytes / (1024 * 1024 * 1024)
            reasons.append(f"High disk read activity ({gb_read:.1f} GB read)")
        elif read_bytes > 0 or write_bytes > 0:
            total_io = read_bytes + write_bytes
            if total_io > 10 * 1024 * 1024 * 1024:
                reasons.append("High disk I/O activity detected")
    
    if network_io:
        bytes_sent = network_io.get('bytes_sent', 0)
        bytes_recv = network_io.get('bytes_recv', 0)
        if bytes_recv > 50 * 1024 * 1024:
            mb_recv = bytes_recv / (1024 * 1024)
            if download_processes:
                reasons.append(f"Active download in progress ({mb_recv:.1f} MB received)")
            else:
                reasons.append(f"High network download activity ({mb_recv:.1f} MB received)")
        elif bytes_sent > 50 * 1024 * 1024:
            mb_sent = bytes_sent / (1024 * 1024)
            reasons.append(f"High network upload activity ({mb_sent:.1f} MB sent)")
        total_net = bytes_sent + bytes_recv
        if total_net > 100 * 1024 * 1024 and not download_processes:
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


def main():
    """Main function for comprehensive server monitoring with Discord alerts."""
    install_systemd = os.getenv('INSTALL_SYSTEMD', 'false').lower() in ('true', '1', 'yes')
    if '--install-systemd' in sys.argv or install_systemd:
        logger.info("Systemd installation requested...")
        if check_systemd_available():
            service_name = os.getenv('SYSTEMD_SERVICE_NAME', 'server-monitor')
            force = '--force' in sys.argv or os.getenv('SYSTEMD_FORCE_INSTALL', 'false').lower() in ('true', '1', 'yes')
            success = install_systemd_service(service_name=service_name, force=force)
            if success:
                logger.info("Systemd service installed successfully!")
                logger.info("The service will start automatically on system boot.")
                logger.info("You can start it now with: sudo systemctl start server-monitor")
                logger.info("To check status: sudo systemctl status server-monitor")
                logger.info("To view logs: sudo journalctl -u server-monitor -f")
                sys.exit(0)
            else:
                logger.error("Failed to install systemd service.")
                logger.info("Make sure you're running with sudo: sudo python src/main.py --install-systemd")
                sys.exit(1)
        else:
            logger.error("Systemd is not available on this system.")
            logger.info("Systemd installation is only supported on Linux systems with systemd.")
            sys.exit(1)
    
    clear_cache_on_startup(verbose=True)
    
    logger.info("Starting Server Monitor...")
    logger.info("Initializing monitors...")
    cpu_monitor = CpuMonitor()
    memory_monitor = MemoryMonitor()
    system_monitor = SystemMonitor()
    gpu_monitor = GpuMonitor()
    disk_monitor = DiskMonitor()
    network_monitor = NetworkMonitor()
    sensors_monitor = SensorsMonitor()
    process_monitor = ProcessMonitor()
    qbittorrent_monitor = QBittorrentMonitor()
    logger.info("Monitors initialized")
    
    webhook = None
    logger.info("Connecting to Discord webhook...")
    try:
        webhook = DiscordWebhook()
        logger.info("Discord webhook connected")
    except ValueError:
        logger.info("Discord webhook not configured")
    
    cpu_threshold = float(os.getenv('CPU_THRESHOLD', '80.0'))
    memory_threshold = float(os.getenv('MEMORY_THRESHOLD', '80.0'))
    gpu_threshold = float(os.getenv('GPU_THRESHOLD', '80.0'))
    disk_threshold = float(os.getenv('DISK_THRESHOLD', '80.0'))
    temp_threshold = float(os.getenv('TEMP_THRESHOLD', '80.0'))
    temp_alert_duration = float(os.getenv('TEMP_ALERT_DURATION', '30.0'))
    network_bandwidth_threshold = float(os.getenv('NETWORK_BANDWIDTH_THRESHOLD', '1000.0'))
    process_cpu_threshold = float(os.getenv('PROCESS_CPU_THRESHOLD', '50.0'))
    process_memory_threshold = float(os.getenv('PROCESS_MEMORY_THRESHOLD', '10.0'))
    check_interval = float(os.getenv('CHECK_INTERVAL', '5.0'))
    alert_cooldown = float(os.getenv('ALERT_COOLDOWN', '300.0'))
    freeze_threshold = float(os.getenv('FREEZE_THRESHOLD', '30.0'))
    freeze_heartbeat_timeout = float(os.getenv('FREEZE_HEARTBEAT_TIMEOUT', '60.0'))
    
    media_directory = os.getenv('MEDIA_DIRECTORY', '/home/media')
    media_directory_threshold_gb = float(os.getenv('MEDIA_DIRECTORY_THRESHOLD_GB', '500.0'))
    media_directory_threshold_bytes = int(media_directory_threshold_gb * 1024 * 1024 * 1024)  # Convert GB to bytes
    media_check_interval = int(os.getenv('MEDIA_CHECK_INTERVAL', '10'))  # Check every N loop iterations
    
    cpu_threshold = max(0.0, min(100.0, cpu_threshold))
    memory_threshold = max(0.0, min(100.0, memory_threshold))
    gpu_threshold = max(0.0, min(100.0, gpu_threshold))
    disk_threshold = max(0.0, min(100.0, disk_threshold))
    temp_threshold = max(0.0, temp_threshold)
    temp_alert_duration = max(0.0, temp_alert_duration)
    process_cpu_threshold = max(0.0, min(100.0, process_cpu_threshold))
    process_memory_threshold = max(0.0, min(100.0, process_memory_threshold))
    check_interval = max(0.1, check_interval)
    alert_cooldown = max(0.0, alert_cooldown)
    freeze_threshold = max(10.0, freeze_threshold)
    freeze_heartbeat_timeout = max(20.0, freeze_heartbeat_timeout)
    
    freeze_monitor = FreezeMonitor(check_interval=check_interval)
    freeze_monitor.set_freeze_threshold(freeze_threshold)
    freeze_monitor.set_heartbeat_timeout(freeze_heartbeat_timeout)
    
    logger.info("Gathering system information...")
    start_time = time.time()
    system_info = system_monitor.get_system_info()
    system_summary = system_monitor.get_system_summary()
    elapsed_time = time.time() - start_time
    logger.info(f"System information gathered ({elapsed_time:.2f}s)")
    
    memory_info = memory_monitor.get_memory_info_human()
    total_disk = disk_monitor.get_total_disk_usage()
    
    logger.info(f"System: {system_info['hostname']} | {system_info['platform']} {system_info['platform_release']} | {system_info['architecture']}")
    logger.info(f"Uptime: {system_summary['uptime']}")
    logger.info(f"CPU: {cpu_monitor.cpu_count} logical, {cpu_monitor.cpu_count_physical} physical | Threshold: {cpu_threshold:.1f}%")
    logger.info(f"Memory: {memory_info['total']} | Threshold: {memory_threshold:.1f}%")
    logger.info(f"Disk: {total_disk['percent']:.1f}% used | Threshold: {disk_threshold:.1f}%")
    
    if gpu_monitor.nvidia_smi_available:
        gpu_count = gpu_monitor.get_gpu_count()
        gpu_name = ""
        if gpu_count > 0:
            gpu_info = gpu_monitor.get_gpu_info(0)
            gpu_name = f" | {gpu_info.get('name', 'N/A')}"
        logger.info(f"GPU: {gpu_count} detected | Threshold: {gpu_threshold:.1f}%{gpu_name}")
    
    if sensors_monitor.is_available():
        cpu_temp = sensors_monitor.get_cpu_temperature()
        logger.info(f"Sensors: {sensors_monitor.format_temperature(cpu_temp)} | Threshold: {temp_threshold:.1f}°C | Alert Duration: {temp_alert_duration:.1f}s")
    
    logger.info(f"Processes: CPU threshold {process_cpu_threshold:.1f}% | Memory threshold {process_memory_threshold:.1f}%")
    logger.info(f"Network: Bandwidth threshold {network_bandwidth_threshold:.1f} MB/s")
    logger.info(f"Freeze Detection: Threshold {freeze_threshold:.1f}s | Heartbeat timeout {freeze_heartbeat_timeout:.1f}s")
    if os.path.exists(media_directory):
        logger.info(f"Directory Monitoring: {media_directory} | Threshold: {media_directory_threshold_gb:.1f} GB")
    else:
        logger.warning(f"Directory Monitoring: {media_directory} not found, skipping directory monitoring")
    logger.info(f"Settings: Check every {check_interval:.1f}s | Cooldown {alert_cooldown:.1f}s")
    
    web_port = int(os.getenv('WEB_PORT', '8080'))
    logger.info("Starting web server...")
    try:
        set_monitors(cpu_monitor, memory_monitor, system_monitor, gpu_monitor,
                    disk_monitor, network_monitor, sensors_monitor)
        start_server(port=web_port)
        logger.info(f"Web server started successfully on port {web_port}")
        logger.info(f"Access the web UI at http://localhost:{web_port}")
    except Exception as e:
        logger.warning(f"Failed to start web server: {e}")
    
    if webhook:
        logger.info("Sending system info to Discord...")
        try:
            start_time = time.time()
            all_info = system_monitor.get_all_info(include_top_processes=True)
            elapsed_time = time.time() - start_time
            success = webhook.send_system_info_comprehensive(all_info)
            if success:
                logger.info(f"System info sent to Discord ({elapsed_time:.2f}s)")
            else:
                logger.warning("Failed to send system info to Discord")
        except KeyboardInterrupt:
            raise
        except Exception as e:
            logger.error(f"Error sending system info: {e}")
    
    logger.info("Starting continuous monitoring (Press Ctrl+C to stop)")
    
    last_cpu_alert_time = 0
    last_memory_alert_time = 0
    last_gpu_alert_time = 0
    last_disk_alert_time = 0
    last_temp_alert_time = 0
    last_freeze_alert_time = 0
    temp_high_start_time = None
    last_process_alert_time = {}
    cpu_alert_sent = False
    memory_alert_sent = False
    gpu_alert_sent = False
    disk_alert_sent = False
    temp_alert_sent = False
    network_alert_sent = False
    freeze_alert_sent = False
    media_alert_sent = False
    last_media_alert_time = 0
    process_check_counter = 0
    network_check_counter = 0
    freeze_check_counter = 0
    media_check_counter = 0
    qbittorrent_check_counter = 0
    NETWORK_CHECK_INTERVAL = 2
    PROCESS_CHECK_INTERVAL = 3
    FREEZE_CHECK_INTERVAL = 1
    QBITTORRENT_CHECK_INTERVAL = 2
    tracked_torrents = {}
    
    try:
        while True:
            freeze_monitor.update_heartbeat()
            
            cpu_percent = cpu_monitor.get_cpu_percent(interval=1.0)
            cpu_per_core = cpu_monitor.get_cpu_percent_per_core(interval=0.1)
            memory_percent = memory_monitor.get_memory_percent()
            memory_info = memory_monitor.get_memory_info()
            swap_info = memory_monitor.get_swap_info()
            
            gpu_utilization = None
            gpu_data = None
            if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
                gpu_utilization = gpu_monitor.get_gpu_utilization(0)
                gpu_data = gpu_monitor.get_gpu_comprehensive(0)
            
            total_disk = disk_monitor.get_total_disk_usage()
            disk_percent = total_disk.get('percent', 0.0)
            
            cpu_temp = None
            if sensors_monitor.is_available():
                cpu_temp = sensors_monitor.get_cpu_temperature()
                if cpu_temp is None:
                    cpu_temp = 0.0
            
            network_bandwidth = None
            network_check_counter += 1
            disk_io_data = None
            network_io_data = None
            if network_check_counter >= NETWORK_CHECK_INTERVAL:
                network_check_counter = 0
                try:
                    bandwidth = network_monitor.get_bandwidth_usage(interval=1.0)
                    if bandwidth:
                        total_bandwidth = bandwidth.get('bytes_sent_per_sec', 0) + bandwidth.get('bytes_recv_per_sec', 0)
                        mb_per_sec = 1024 * 1024
                        if mb_per_sec > 0:
                            network_bandwidth = total_bandwidth / mb_per_sec
                    network_io_data = network_monitor.get_total_network_io()
                except (ValueError, ZeroDivisionError) as e:
                    pass
                except Exception as e:
                    pass
            
            try:
                disk_io_data = system_monitor.get_disk_io()
            except Exception:
                pass
            
            freeze_monitor.update_metrics(
                cpu_percent=cpu_percent,
                memory_percent=memory_percent,
                disk_io=disk_io_data,
                network_io=network_io_data
            )
            
            current_time = time.time()
            timestamp = time.strftime("%Y-%m-%d %H:%M:%S")
            
            metrics_str = f"[{timestamp}] CPU: {cpu_percent:.1f}% | Memory: {memory_percent:.1f}% | Disk: {disk_percent:.1f}%"
            if gpu_utilization is not None:
                metrics_str += f" | GPU: {gpu_utilization['gpu']:.1f}%"
            if cpu_temp is not None:
                metrics_str += f" | Temp: {cpu_temp:.1f}°C"
            if network_bandwidth is not None:
                metrics_str += f" | Network: {network_bandwidth:.1f} MB/s"
            logger.info(metrics_str)
            
            if cpu_percent >= cpu_threshold:
                time_since_last_alert = current_time - last_cpu_alert_time
                
                if not cpu_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook:
                        logger.warning(f"[ALERT] CPU: {cpu_percent:.1f}% (threshold: {cpu_threshold:.1f}%)")
                        success = webhook.send_cpu_alert(
                            cpu_percent=cpu_percent,
                            threshold=cpu_threshold,
                            per_core=cpu_per_core
                        )
                        if success:
                            logger.info("    Discord alert sent")
                        else:
                            logger.warning("    Discord alert failed")
                    else:
                        logger.warning(f"CPU: {cpu_percent:.1f}% (threshold: {cpu_threshold:.1f}%)")
                    
                    last_cpu_alert_time = current_time
                    cpu_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"CPU alert cooldown: {remaining_cooldown:.0f}s")
            else:
                if cpu_alert_sent:
                    cpu_alert_sent = False
            
            if memory_percent >= memory_threshold:
                time_since_last_alert = current_time - last_memory_alert_time
                
                if not memory_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook:
                        logger.warning(f"[ALERT] Memory: {memory_percent:.1f}% (threshold: {memory_threshold:.1f}%)")
                        success = webhook.send_memory_alert(
                            memory_percent=memory_percent,
                            threshold=memory_threshold,
                            memory_info=memory_info,
                            swap_info=swap_info
                        )
                        if success:
                            logger.info("    Discord alert sent")
                        else:
                            logger.warning("    Discord alert failed")
                    else:
                        logger.warning(f"Memory: {memory_percent:.1f}% (threshold: {memory_threshold:.1f}%)")
                    
                    last_memory_alert_time = current_time
                    memory_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"Memory alert cooldown: {remaining_cooldown:.0f}s")
            else:
                if memory_alert_sent:
                    memory_alert_sent = False
            
            max_core_usage = max(cpu_per_core) if cpu_per_core else 0
            if max_core_usage >= cpu_threshold:
                high_cores = [f"Core {i}: {core_usage:.1f}%" for i, core_usage in enumerate(cpu_per_core) if core_usage >= cpu_threshold]
                if high_cores:
                    logger.debug(f"High core usage: {', '.join(high_cores)}")
            
            if gpu_utilization is not None and gpu_utilization['gpu'] >= gpu_threshold:
                time_since_last_alert = current_time - last_gpu_alert_time
                
                if not gpu_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook and gpu_data:
                        logger.warning(f"[ALERT] GPU: {gpu_utilization['gpu']:.1f}% (threshold: {gpu_threshold:.1f}%)")
                        success = webhook.send_gpu_alert(
                            gpu_utilization=gpu_utilization['gpu'],
                            threshold=gpu_threshold,
                            gpu_info=gpu_data.get('info'),
                            memory_info=gpu_data.get('memory'),
                            temperature=gpu_data.get('temperature'),
                            power=gpu_data.get('power')
                        )
                        if success:
                            logger.info("    Discord alert sent")
                        else:
                            logger.warning("    Discord alert failed")
                    else:
                        logger.warning(f"GPU: {gpu_utilization['gpu']:.1f}% (threshold: {gpu_threshold:.1f}%)")
                    
                    last_gpu_alert_time = current_time
                    gpu_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"GPU alert cooldown: {remaining_cooldown:.0f}s")
            else:
                if gpu_alert_sent:
                    gpu_alert_sent = False
            
            if disk_percent >= disk_threshold:
                time_since_last_alert = current_time - last_disk_alert_time
                
                if not disk_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook:
                        logger.warning(f"[ALERT] Disk: {disk_percent:.1f}% (threshold: {disk_threshold:.1f}%)")
                        
                        largest_dirs = []
                        try:
                            root_partition = disk_monitor.get_disk_usage('/')
                            if root_partition and root_partition['percent'] >= disk_threshold:
                                largest_dirs = disk_monitor.get_largest_directories_human('/', limit=5)
                        except Exception as e:
                            logger.debug(f"Could not get largest directories: {e}")
                        
                        fields = [
                            {'name': 'Disk Usage', 'value': f'{disk_percent:.1f}%', 'inline': True},
                            {'name': 'Threshold', 'value': f'{disk_threshold:.1f}%', 'inline': True},
                            {'name': 'Total Disk', 'value': format_bytes(total_disk['total']), 'inline': False},
                            {'name': 'Used', 'value': format_bytes(total_disk['used']), 'inline': True},
                            {'name': 'Free', 'value': format_bytes(total_disk['free']), 'inline': True},
                        ]
                        
                        if largest_dirs:
                            dir_list = []
                            for i, dir_info in enumerate(largest_dirs, 1):
                                dir_name = os.path.basename(dir_info['path'])
                                dir_size = dir_info['size']
                                dir_list.append(f"{i}. `{dir_name}`: {dir_size}")
                            
                            fields.append({
                                'name': 'Largest Directories',
                                'value': '\n'.join(dir_list) if dir_list else 'Unable to determine',
                                'inline': False
                            })
                        
                        success = webhook.send_embed(
                            title='Disk Usage Alert',
                            description=f'Disk usage has exceeded the threshold of {disk_threshold:.1f}%',
                            color=0xdc3545,
                            fields=fields,
                            footer='Server Monitor',
                            timestamp=True
                        )
                        if success:
                            logger.info("    Discord alert sent")
                        else:
                            logger.warning("    Discord alert failed")
                    else:
                        logger.warning(f"Disk: {disk_percent:.1f}% (threshold: {disk_threshold:.1f}%)")
                    
                    last_disk_alert_time = current_time
                    disk_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"Disk alert cooldown: {remaining_cooldown:.0f}s")
            else:
                if disk_alert_sent:
                    disk_alert_sent = False
            
            if cpu_temp is not None and cpu_temp >= temp_threshold:
                if temp_high_start_time is None:
                    temp_high_start_time = current_time
                
                temp_high_duration = current_time - temp_high_start_time
                time_since_last_alert = current_time - last_temp_alert_time
                
                if temp_high_duration >= temp_alert_duration:
                    if not temp_alert_sent or time_since_last_alert >= alert_cooldown:
                        if webhook:
                            cpu_freq_info = cpu_monitor.get_cpu_freq()
                            load_avg = cpu_monitor.get_cpu_load_average()
                            top_cpu_procs = process_monitor.get_top_processes(limit=5, sort_by='cpu')
                            fans = sensors_monitor.get_fan_speeds()
                            
                            gpu_temp = None
                            if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
                                gpu_data_temp = gpu_monitor.get_gpu_comprehensive(0)
                                gpu_temp = gpu_data_temp.get('temperature')
                            
                            temp_disk_io = disk_io_data
                            if temp_disk_io is None:
                                try:
                                    temp_disk_io = system_monitor.get_disk_io()
                                except Exception:
                                    pass
                            
                            temp_network_io = network_io_data
                            if temp_network_io is None:
                                try:
                                    temp_network_io = network_monitor.get_total_network_io()
                                except Exception:
                                    pass
                            
                            reason = analyze_temperature_reason(
                                cpu_percent=cpu_percent,
                                cpu_freq=cpu_freq_info,
                                cpu_per_core=cpu_per_core,
                                top_processes=top_cpu_procs,
                                load_avg=load_avg,
                                fan_speeds=fans,
                                disk_io=temp_disk_io,
                                network_io=temp_network_io,
                                gpu_temp=gpu_temp
                            )
                            
                            logger.warning(f"[ALERT] Temperature: {cpu_temp:.1f}°C (threshold: {temp_threshold:.1f}°C) for {temp_high_duration:.0f}s")
                            logger.info(f"    Likely cause: {reason}")
                            
                            success = webhook.send_temperature_alert(
                                temperature=cpu_temp,
                                threshold=temp_threshold,
                                duration=temp_high_duration,
                                reason=reason,
                                cpu_percent=cpu_percent,
                                cpu_freq=cpu_freq_info,
                                top_processes=top_cpu_procs,
                                fan_speeds=fans
                            )
                            if success:
                                logger.info("    Discord alert sent")
                            else:
                                logger.warning("    Discord alert failed")
                        else:
                            logger.warning(f"Temperature: {cpu_temp:.1f}°C (threshold: {temp_threshold:.1f}°C) for {temp_high_duration:.0f}s")
                        
                        last_temp_alert_time = current_time
                        temp_alert_sent = True
                    else:
                        remaining_cooldown = alert_cooldown - time_since_last_alert
                        logger.debug(f"Temperature alert cooldown: {remaining_cooldown:.0f}s")
                else:
                    remaining_duration = temp_alert_duration - temp_high_duration
                    logger.debug(f"Temperature high ({cpu_temp:.1f}°C) but needs to persist for {remaining_duration:.0f}s more before alert")
            else:
                if temp_high_start_time is not None:
                    temp_high_start_time = None
                if temp_alert_sent:
                    temp_alert_sent = False
            
            if network_bandwidth is not None and network_bandwidth >= network_bandwidth_threshold:
                time_since_last_alert = current_time - last_network_alert_time
                
                if not network_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook:
                        logger.warning(f"[ALERT] Network: {network_bandwidth:.1f} MB/s (threshold: {network_bandwidth_threshold:.1f} MB/s)")
                        bandwidth_data = network_monitor.get_bandwidth_usage(interval=1.0)
                        fields = [
                            {'name': 'Total Bandwidth', 'value': f'{network_bandwidth:.1f} MB/s', 'inline': True},
                            {'name': 'Threshold', 'value': f'{network_bandwidth_threshold:.1f} MB/s', 'inline': True},
                        ]
                        if bandwidth_data:
                            fields.append({
                                'name': 'Upload Speed',
                                'value': format_bytes_per_second(bandwidth_data.get('bytes_sent_per_sec', 0)),
                                'inline': True
                            })
                            fields.append({
                                'name': 'Download Speed',
                                'value': format_bytes_per_second(bandwidth_data.get('bytes_recv_per_sec', 0)),
                                'inline': True
                            })
                        success = webhook.send_embed(
                            title='Network Bandwidth Alert',
                            description=f'Network bandwidth has exceeded the threshold of {network_bandwidth_threshold:.1f} MB/s',
                            color=0xdc3545,
                            fields=fields,
                            footer='Server Monitor',
                            timestamp=True
                        )
                        if success:
                            logger.info("    Discord alert sent")
                        else:
                            logger.warning("    Discord alert failed")
                    else:
                        logger.warning(f"Network: {network_bandwidth:.1f} MB/s (threshold: {network_bandwidth_threshold:.1f} MB/s)")
                    
                    last_network_alert_time = current_time
                    network_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"Network alert cooldown: {remaining_cooldown:.0f}s")
            else:
                if network_alert_sent:
                    network_alert_sent = False
            
            process_check_counter += 1
            
            if process_check_counter >= PROCESS_CHECK_INTERVAL:
                process_check_counter = 0
                
                try:
                    top_cpu_processes = process_monitor.get_top_processes(limit=5, sort_by='cpu')
                    top_memory_processes = process_monitor.get_top_processes(limit=5, sort_by='memory')
                    
                    for proc in top_cpu_processes:
                        cpu_usage = proc.get('cpu_percent', 0)
                        if cpu_usage >= process_cpu_threshold:
                            pid = proc.get('pid')
                            name = proc.get('name', 'Unknown')
                            proc_key = f"cpu_{pid}"
                            time_since_last_alert = current_time - last_process_alert_time.get(proc_key, 0)
                            
                            if time_since_last_alert >= alert_cooldown:
                                if webhook:
                                    logger.warning(f"[ALERT] Process {name} (PID: {pid}): CPU {cpu_usage:.1f}% (threshold: {process_cpu_threshold:.1f}%)")
                                    success = webhook.send_process_alert(
                                        process_info=proc,
                                        resource_type='cpu',
                                        threshold=process_cpu_threshold
                                    )
                                    if success:
                                        logger.info("    Discord alert sent")
                                    else:
                                        logger.warning("    Discord alert failed")
                                else:
                                    logger.warning(f"Process {name} (PID: {pid}): CPU {cpu_usage:.1f}% (threshold: {process_cpu_threshold:.1f}%)")
                                
                                last_process_alert_time[proc_key] = current_time
                    
                    for proc in top_memory_processes:
                        memory_usage = proc.get('memory_percent', 0)
                        if memory_usage >= process_memory_threshold:
                            pid = proc.get('pid')
                            name = proc.get('name', 'Unknown')
                            proc_key = f"memory_{pid}"
                            time_since_last_alert = current_time - last_process_alert_time.get(proc_key, 0)
                            
                            if time_since_last_alert >= alert_cooldown:
                                if webhook:
                                    logger.warning(f"[ALERT] Process {name} (PID: {pid}): Memory {memory_usage:.1f}% (threshold: {process_memory_threshold:.1f}%)")
                                    success = webhook.send_process_alert(
                                        process_info=proc,
                                        resource_type='memory',
                                        threshold=process_memory_threshold
                                    )
                                    if success:
                                        logger.info("    Discord alert sent")
                                    else:
                                        logger.warning("    Discord alert failed")
                                else:
                                    logger.warning(f"Process {name} (PID: {pid}): Memory {memory_usage:.1f}% (threshold: {process_memory_threshold:.1f}%)")
                                
                                last_process_alert_time[proc_key] = current_time
                except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess):
                    pass
                except Exception as e:
                    pass
            
            freeze_check_counter += 1
            if freeze_check_counter >= FREEZE_CHECK_INTERVAL:
                freeze_check_counter = 0
                try:
                    freeze_status = freeze_monitor.check_freeze_status()
                    if freeze_status['is_frozen']:
                        time_since_last_alert = current_time - last_freeze_alert_time
                        
                        if not freeze_alert_sent or time_since_last_alert >= alert_cooldown:
                            if webhook:
                                logger.warning(f"[ALERT] System Freeze Detected: {', '.join(freeze_status['reasons'])}")
                                try:
                                    current_system_info = {
                                        'uptime': system_monitor.get_uptime(),
                                        'cpu': {'cpu_percent': cpu_percent},
                                        'memory': {'memory_percent': memory_percent}
                                    }
                                except Exception:
                                    current_system_info = None
                                
                                success = webhook.send_freeze_alert(
                                    freeze_status=freeze_status,
                                    system_info=current_system_info
                                )
                                if success:
                                    logger.info("    Discord freeze alert sent")
                                else:
                                    logger.warning("    Discord freeze alert failed")
                            else:
                                logger.warning(f"System Freeze Detected: {', '.join(freeze_status['reasons'])}")
                            
                            last_freeze_alert_time = current_time
                            freeze_alert_sent = True
                        else:
                            remaining_cooldown = alert_cooldown - time_since_last_alert
                            logger.debug(f"Freeze alert cooldown: {remaining_cooldown:.0f}s")
                    else:
                        if freeze_alert_sent:
                            freeze_alert_sent = False
                except Exception as e:
                    logger.debug(f"Error checking freeze status: {e}")
            
            media_check_counter += 1
            if media_check_counter >= media_check_interval and os.path.exists(media_directory):
                media_check_counter = 0
                try:
                    media_size_info = disk_monitor.get_directory_size(media_directory)
                    if media_size_info and media_size_info['size'] >= media_directory_threshold_bytes:
                        time_since_last_alert = current_time - last_media_alert_time
                        
                        if not media_alert_sent or time_since_last_alert >= alert_cooldown:
                            subdirs = disk_monitor.get_subdirectories_sizes(media_directory)
                            subdirs_human = []
                            for subdir in subdirs:
                                subdir_human = disk_monitor.get_directory_size_human(subdir['path'])
                                if subdir_human:
                                    subdir_human['size_bytes'] = subdir['size']
                                    subdirs_human.append(subdir_human)
                            
                            if webhook:
                                from util.util import format_bytes
                                size_formatted = format_bytes(media_size_info['size'])
                                logger.warning(f"[ALERT] Directory {media_directory}: {size_formatted} (threshold: {format_bytes(media_directory_threshold_bytes)})")
                                success = webhook.send_directory_size_alert(
                                    directory_path=media_directory,
                                    size_bytes=media_size_info['size'],
                                    threshold_bytes=media_directory_threshold_bytes,
                                    subdirectories=subdirs_human
                                )
                                if success:
                                    logger.info("    Discord directory alert sent")
                                else:
                                    logger.warning("    Discord directory alert failed")
                            else:
                                from util.util import format_bytes
                                size_formatted = format_bytes(media_size_info['size'])
                                logger.warning(f"Directory {media_directory}: {size_formatted} (threshold: {format_bytes(media_directory_threshold_bytes)})")
                            
                            last_media_alert_time = current_time
                            media_alert_sent = True
                        else:
                            remaining_cooldown = alert_cooldown - time_since_last_alert
                            logger.debug(f"Directory alert cooldown: {remaining_cooldown:.0f}s")
                    else:
                        if media_alert_sent:
                            media_alert_sent = False
                except Exception as e:
                    logger.debug(f"Error checking directory size: {e}")
            
            qbittorrent_check_counter += 1
            if qbittorrent_check_counter >= QBITTORRENT_CHECK_INTERVAL:
                qbittorrent_check_counter = 0
                if qbittorrent_monitor.is_available():
                    try:
                        all_torrents = qbittorrent_monitor.get_all_torrents()
                        current_torrents = {torrent['hash']: torrent for torrent in all_torrents}
                        
                        for torrent_hash, torrent in current_torrents.items():
                            state = torrent.get('state', '').lower()
                            progress = torrent.get('progress', 0.0)
                            
                            is_seeding = state in ['uploading', 'stalledup', 'queuedup']
                            was_downloading = state in ['downloading', 'stalleddl', 'queueddl', 'checkingdl', 'pauseddl']
                            
                            if torrent_hash not in tracked_torrents:
                                tracked_torrents[torrent_hash] = {
                                    'state': state,
                                    'progress': progress,
                                    'name': torrent.get('name', 'Unknown'),
                                    'notified_download': False,
                                    'notified_seeding': False
                                }
                                continue
                            
                            prev_state = tracked_torrents[torrent_hash]['state']
                            prev_progress = tracked_torrents[torrent_hash]['progress']
                            prev_was_seeding = prev_state in ['uploading', 'stalledup', 'queuedup']
                            prev_was_downloading = prev_state in ['downloading', 'stalleddl', 'queueddl', 'checkingdl', 'pauseddl']
                            
                            if is_seeding and progress >= 1.0:
                                if not tracked_torrents[torrent_hash]['notified_download'] and prev_was_downloading:
                                    if webhook:
                                        logger.info(f"[qBittorrent] Download complete: {torrent.get('name', 'Unknown')}")
                                        success = webhook.send_download_complete_alert(torrent)
                                        if success:
                                            logger.info("    Discord download complete notification sent")
                                        else:
                                            logger.warning("    Discord download complete notification failed")
                                    tracked_torrents[torrent_hash]['notified_download'] = True
                            
                            if is_seeding and not prev_was_seeding:
                                if not tracked_torrents[torrent_hash]['notified_seeding']:
                                    if webhook:
                                        logger.info(f"[qBittorrent] Torrent seeding: {torrent.get('name', 'Unknown')}")
                                        success = webhook.send_seeding_notification(torrent)
                                        if success:
                                            logger.info("    Discord seeding notification sent")
                                        else:
                                            logger.warning("    Discord seeding notification failed")
                                    tracked_torrents[torrent_hash]['notified_seeding'] = True
                            
                            tracked_torrents[torrent_hash]['state'] = state
                            tracked_torrents[torrent_hash]['progress'] = progress
                        
                        for torrent_hash in list(tracked_torrents.keys()):
                            if torrent_hash not in current_torrents:
                                del tracked_torrents[torrent_hash]
                    except Exception as e:
                        logger.debug(f"Error checking qBittorrent: {e}")
            
            time.sleep(check_interval)
            
    except KeyboardInterrupt:
        shutdown_start = time.time()
        logger.info("Monitoring stopped by user")
        if webhook:
            try:
                logger.info("Sending shutdown notification...")
                webhook.send_message("Server monitoring stopped by user")
                logger.info("Shutdown notification sent")
            except (KeyboardInterrupt, Exception):
                logger.warning("Shutdown notification skipped")
        logger.info("Shutting down...")
        try:
            freeze_monitor.stop_watchdog()
        except Exception as e:
            logger.debug(f"Error stopping freeze monitor: {e}")
        try:
            stop_server()
        except Exception as e:
            logger.warning(f"Error stopping web server: {e}")
        shutdown_time = time.time() - shutdown_start
        logger.info(f"Shutdown complete ({shutdown_time:.2f}s)")
        
        clear_cache_on_exit(verbose=True)
        
        logger.info("Server Monitor stopped")


if __name__ == "__main__":
    main()

