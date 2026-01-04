from monitor.cpu import CpuMonitor
from monitor.memory import MemoryMonitor
from monitor.system import SystemMonitor
from monitor.gpu import GpuMonitor
from monitor.disk import DiskMonitor
from monitor.network import NetworkMonitor
from monitor.sensors import SensorsMonitor
from monitor.processes import ProcessMonitor
from discord.webhook import DiscordWebhook
from util.util import format_bytes, format_bytes_per_second
from cache.cache import clear_cache_on_startup, clear_cache_on_exit
from logger import get_logger
import os
import time
import psutil
from dotenv import load_dotenv

load_dotenv()

logger = get_logger()


def main():
    """Main function for comprehensive server monitoring with Discord alerts."""
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
    network_bandwidth_threshold = float(os.getenv('NETWORK_BANDWIDTH_THRESHOLD', '1000.0'))
    process_cpu_threshold = float(os.getenv('PROCESS_CPU_THRESHOLD', '50.0'))
    process_memory_threshold = float(os.getenv('PROCESS_MEMORY_THRESHOLD', '10.0'))
    check_interval = float(os.getenv('CHECK_INTERVAL', '5.0'))
    alert_cooldown = float(os.getenv('ALERT_COOLDOWN', '300.0'))
    
    cpu_threshold = max(0.0, min(100.0, cpu_threshold))
    memory_threshold = max(0.0, min(100.0, memory_threshold))
    gpu_threshold = max(0.0, min(100.0, gpu_threshold))
    disk_threshold = max(0.0, min(100.0, disk_threshold))
    temp_threshold = max(0.0, temp_threshold)
    process_cpu_threshold = max(0.0, min(100.0, process_cpu_threshold))
    process_memory_threshold = max(0.0, min(100.0, process_memory_threshold))
    check_interval = max(0.1, check_interval)
    alert_cooldown = max(0.0, alert_cooldown)
    
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
        logger.info(f"Sensors: {sensors_monitor.format_temperature(cpu_temp)} | Threshold: {temp_threshold:.1f}°C")
    
    logger.info(f"Processes: CPU threshold {process_cpu_threshold:.1f}% | Memory threshold {process_memory_threshold:.1f}%")
    logger.info(f"Network: Bandwidth threshold {network_bandwidth_threshold:.1f} MB/s")
    logger.info(f"Settings: Check every {check_interval:.1f}s | Cooldown {alert_cooldown:.1f}s")
    
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
    last_process_alert_time = {}
    cpu_alert_sent = False
    memory_alert_sent = False
    gpu_alert_sent = False
    disk_alert_sent = False
    temp_alert_sent = False
    network_alert_sent = False
    process_check_counter = 0
    network_check_counter = 0
    NETWORK_CHECK_INTERVAL = 2
    PROCESS_CHECK_INTERVAL = 3
    
    try:
        while True:
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
            if network_check_counter >= NETWORK_CHECK_INTERVAL:
                network_check_counter = 0
                try:
                    bandwidth = network_monitor.get_bandwidth_usage(interval=1.0)
                    if bandwidth:
                        total_bandwidth = bandwidth.get('bytes_sent_per_sec', 0) + bandwidth.get('bytes_recv_per_sec', 0)
                        mb_per_sec = 1024 * 1024
                        if mb_per_sec > 0:
                            network_bandwidth = total_bandwidth / mb_per_sec
                except (ValueError, ZeroDivisionError) as e:
                    pass
                except Exception as e:
                    pass
            
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
                        fields = [
                            {'name': 'Disk Usage', 'value': f'{disk_percent:.1f}%', 'inline': True},
                            {'name': 'Threshold', 'value': f'{disk_threshold:.1f}%', 'inline': True},
                            {'name': 'Total Disk', 'value': format_bytes(total_disk['total']), 'inline': False},
                            {'name': 'Used', 'value': format_bytes(total_disk['used']), 'inline': True},
                            {'name': 'Free', 'value': format_bytes(total_disk['free']), 'inline': True},
                        ]
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
                time_since_last_alert = current_time - last_temp_alert_time
                
                if not temp_alert_sent or time_since_last_alert >= alert_cooldown:
                    if webhook:
                        logger.warning(f"[ALERT] Temperature: {cpu_temp:.1f}°C (threshold: {temp_threshold:.1f}°C)")
                        all_temps = sensors_monitor.get_all_temperatures()
                        fans = sensors_monitor.get_fan_speeds()
                        fields = [
                            {'name': 'CPU Temperature', 'value': f'{cpu_temp:.1f}°C', 'inline': True},
                            {'name': 'Threshold', 'value': f'{temp_threshold:.1f}°C', 'inline': True},
                        ]
                        if fans:
                            fan_info = '\n'.join([f"{fan['label']}: {fan['speed_rpm']} RPM" for fan in fans[:3]])
                            fields.append({'name': 'Fan Speeds', 'value': fan_info[:1024], 'inline': False})
                        success = webhook.send_embed(
                            title='Temperature Alert',
                            description=f'CPU temperature has exceeded the threshold of {temp_threshold:.1f}°C',
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
                        logger.warning(f"Temperature: {cpu_temp:.1f}°C (threshold: {temp_threshold:.1f}°C)")
                    
                    last_temp_alert_time = current_time
                    temp_alert_sent = True
                else:
                    remaining_cooldown = alert_cooldown - time_since_last_alert
                    logger.debug(f"Temperature alert cooldown: {remaining_cooldown:.0f}s")
            else:
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
        shutdown_time = time.time() - shutdown_start
        logger.info(f"Shutdown complete ({shutdown_time:.2f}s)")
        
        clear_cache_on_exit(verbose=True)
        
        logger.info("Server Monitor stopped")


if __name__ == "__main__":
    main()

