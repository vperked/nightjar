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
from monitor.log import LogMonitor
from monitor.kernel import KernelMonitor
from monitor.watchdog import WatchdogMonitor
from discord.webhook import DiscordWebhook
from util.util import format_bytes, format_bytes_per_second, BYTES_PER_KB, BYTES_PER_MB, BYTES_PER_GB
from util.systemd import install_systemd_service, is_systemd_available
from cache.cache import clear_cache_on_startup, clear_cache_on_exit
from logger import get_logger
from web.server import set_monitors, start_server, stop_server, set_crash_webhook
from typing import Optional, Tuple, Dict, List, Any
from datetime import datetime
from pathlib import Path
import os
import sys
import time
import platform
import subprocess
import psutil
import hashlib
import json
import traceback
import threading
import socket
import signal
import atexit
from concurrent.futures import ThreadPoolExecutor, TimeoutError as FutureTimeoutError
from dotenv import load_dotenv

load_dotenv()

logger = get_logger()


def analyze_system_usage_patterns() -> Dict[str, Any]:
    """
    Analyze current system usage patterns to determine optimal thresholds.

    Samples CPU, memory, disk, and load averages to understand typical workload.

    Returns:
        Dictionary with usage analysis results
    """
    analysis = {
        'cpu_samples': [],
        'memory_samples': [],
        'disk_samples': [],
        'load_averages': [],
        'process_count': 0,
        'typical_cpu': 0.0,
        'typical_memory': 0.0,
        'typical_disk': 0.0,
        'peak_cpu': 0.0,
        'peak_memory': 0.0,
        'system_type': 'unknown'
    }

    try:
        import time

        sample_count = 5
        sample_interval = 0.5

        for i in range(sample_count):
            try:

                cpu_percent = psutil.cpu_percent(interval=0.1)
                analysis['cpu_samples'].append(cpu_percent)


                mem = psutil.virtual_memory()
                analysis['memory_samples'].append(mem.percent)


                if platform.system() == 'Windows':
                    root_path = os.path.splitdrive(sys.executable)[0] + '\\'
                else:
                    root_path = '/'
                disk = psutil.disk_usage(root_path)
                analysis['disk_samples'].append(disk.percent)

                try:
                    load_avg = psutil.getloadavg()
                    analysis['load_averages'].append(load_avg)
                except (AttributeError, OSError):
                    pass

                if i < sample_count - 1:
                    time.sleep(sample_interval)
            except Exception as e:
                logger.debug(f"Error sampling system metrics: {e}")
                break

        if analysis['cpu_samples']:
            analysis['typical_cpu'] = sum(analysis['cpu_samples']) / len(analysis['cpu_samples'])
            analysis['peak_cpu'] = max(analysis['cpu_samples'])

        if analysis['memory_samples']:
            analysis['typical_memory'] = sum(analysis['memory_samples']) / len(analysis['memory_samples'])
            analysis['peak_memory'] = max(analysis['memory_samples'])

        if analysis['disk_samples']:
            analysis['typical_disk'] = sum(analysis['disk_samples']) / len(analysis['disk_samples'])

        try:
            analysis['process_count'] = len(psutil.pids())
        except Exception:
            pass

        # Determine system type based on usage patterns
        if analysis['typical_cpu'] > 0:
            if analysis['typical_cpu'] > 30 or analysis['process_count'] > 100:
                analysis['system_type'] = 'server'
            elif analysis['typical_cpu'] < 10 and analysis['process_count'] < 50:
                analysis['system_type'] = 'desktop'
            else:
                analysis['system_type'] = 'workstation'

    except Exception as e:
        logger.debug(f"Could not analyze system usage patterns: {e}")

    return analysis


def detect_system_config(skip_usage_analysis: bool = False) -> Dict[str, Any]:
    """
    Auto-detect system capabilities and adapt configuration.

    Detects:
    - GPU availability (NVIDIA)
    - qBittorrent availability
    - CPU and memory specs to adapt thresholds
    - Disk space to adapt thresholds
    - Platform-specific features
    - System usage patterns for optimal thresholds (if skip_usage_analysis=False)

    Args:
        skip_usage_analysis: If True, skip expensive usage pattern analysis (default: False)

    Returns:
        Dictionary with detected configuration values
    """
    detected = {
        'gpu_available': False,
        'qbittorrent_available': False,
        'cpu_cores': 0,
        'total_memory_gb': 0.0,
        'total_disk_gb': 0.0,
        'platform': platform.system(),
        'usage_analysis': {},
        'adaptations': []
    }

    try:
        detected['cpu_cores'] = psutil.cpu_count(logical=True)
        detected['adaptations'].append(f"Detected {detected['cpu_cores']} CPU cores")
    except Exception as e:
        logger.debug(f"Could not detect CPU cores: {e}")

    try:
        # Detect total memory
        mem = psutil.virtual_memory()
        detected['total_memory_gb'] = mem.total / BYTES_PER_GB
        detected['adaptations'].append(f"Detected {detected['total_memory_gb']:.1f} GB total memory")
    except Exception as e:
        logger.debug(f"Could not detect memory: {e}")

    try:
        try:
            result = subprocess.run(
                ['nvidia-smi', '--version'],
                capture_output=True,
                text=True,
                timeout=3
            )
            if result.returncode == 0:
                detected['gpu_available'] = True
                detected['adaptations'].append("NVIDIA GPU detected - GPU monitoring will be enabled")
        except (FileNotFoundError, subprocess.TimeoutExpired):
            pass
    except Exception as e:
        logger.debug(f"Could not detect GPU: {e}")

    try:
        # Detect qBittorrent availability
        qb_host = os.getenv('QBITTORRENT_HOST', 'localhost')
        qb_port = int(os.getenv('QBITTORRENT_PORT', '8080'))

        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(2)
                result = sock.connect_ex((qb_host, qb_port))
        except Exception as e:
            logger.debug(f"Socket error checking qBittorrent: {e}")
            result = 1  # Connection failed

        if result == 0:
            # Port is open, try to authenticate
            try:
                import qbittorrentapi
                qb_client = qbittorrentapi.Client(
                    host=qb_host,
                    port=qb_port,
                    username=os.getenv('QBITTORRENT_USERNAME', 'admin'),
                    password=os.getenv('QBITTORRENT_PASSWORD', '')
                )
                qb_client.auth_log_in()
                detected['qbittorrent_available'] = True
                detected['adaptations'].append("qBittorrent detected - qBittorrent monitoring can be enabled")
                qb_client.auth_log_out()
            except Exception:
                pass
    except Exception as e:
        logger.debug(f"Could not detect qBittorrent: {e}")

    try:
        if platform.system() == 'Windows':
            # On Windows, use the drive where the script is running
            root_path = os.path.splitdrive(sys.executable)[0] + '\\'
        else:
            root_path = '/'

        disk = psutil.disk_usage(root_path)
        detected['total_disk_gb'] = disk.total / BYTES_PER_GB
        detected['adaptations'].append(f"Detected {detected['total_disk_gb']:.1f} GB total disk space")
    except Exception as e:
        logger.debug(f"Could not detect disk space: {e}")

    if not skip_usage_analysis:
        logger.info("Analyzing system usage patterns to determine optimal thresholds...")
        detected['usage_analysis'] = analyze_system_usage_patterns()
        if detected['usage_analysis'].get('system_type') != 'unknown':
            detected['adaptations'].append(f"System type: {detected['usage_analysis']['system_type']}")
    else:
        logger.info("Skipping usage pattern analysis (thresholds already configured)")
        detected['usage_analysis'] = {'system_type': 'unknown'}

    return detected


def apply_system_adaptations(detected: Dict[str, Any]) -> Dict[str, str]:
    """
    Apply auto-detected system adaptations to environment variables if not already set.

    Args:
        detected: Dictionary from detect_system_config()

    Returns:
        Dictionary of auto-detected values that were set (key -> value)
    """
    adaptations_applied = []
    auto_detected_values = {}

    # Adapt thresholds based on system specs AND usage patterns
    usage = detected.get('usage_analysis', {})

    if not os.getenv('CPU_THRESHOLD'):
        threshold = None
        reasoning = []

        if detected['cpu_cores'] > 0:
            if detected['cpu_cores'] <= 2:
                base_threshold = 70.0
                reasoning.append(f"{detected['cpu_cores']} cores (conservative)")
            elif detected['cpu_cores'] <= 4:
                base_threshold = 75.0
                reasoning.append(f"{detected['cpu_cores']} cores")
            else:
                base_threshold = 80.0
                reasoning.append(f"{detected['cpu_cores']} cores")
        else:
            base_threshold = DEFAULT_CPU_THRESHOLD

        # Adjust based on typical usage patterns
        typical_cpu = usage.get('typical_cpu', 0)
        peak_cpu = usage.get('peak_cpu', 0)
        system_type = usage.get('system_type', 'unknown')

        if typical_cpu > 0:
            if typical_cpu > 50:
                threshold = min(95.0, base_threshold + 10.0)
                reasoning.append(f"high typical usage ({typical_cpu:.1f}%)")
            elif typical_cpu > 30:
                threshold = min(90.0, base_threshold + 5.0)
                reasoning.append(f"moderate typical usage ({typical_cpu:.1f}%)")
            elif typical_cpu < 10:
                # Low usage system - more conservative threshold
                threshold = max(70.0, base_threshold - 5.0)
                reasoning.append(f"low typical usage ({typical_cpu:.1f}%)")
            else:
                threshold = base_threshold
                reasoning.append(f"typical usage ({typical_cpu:.1f}%)")

            if system_type == 'server':
                # Servers can handle higher sustained loads
                threshold = min(90.0, threshold + 5.0)
                reasoning.append("server workload")
            elif system_type == 'desktop':
                threshold = max(70.0, threshold - 5.0)
                reasoning.append("desktop workload")
        else:
            threshold = base_threshold

        # Ensure threshold is within reasonable bounds
        threshold = max(65.0, min(95.0, threshold))

        os.environ['CPU_THRESHOLD'] = str(threshold)
        auto_detected_values['CPU_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"CPU_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    if not os.getenv('MEMORY_THRESHOLD'):
        threshold = None
        reasoning = []

        # Base threshold on total memory
        if detected['total_memory_gb'] > 0:
            if detected['total_memory_gb'] < 4:
                base_threshold = 70.0
                reasoning.append(f"{detected['total_memory_gb']:.1f} GB (low memory)")
            elif detected['total_memory_gb'] < 8:
                base_threshold = 75.0
                reasoning.append(f"{detected['total_memory_gb']:.1f} GB")
            elif detected['total_memory_gb'] < 16:
                base_threshold = 80.0
                reasoning.append(f"{detected['total_memory_gb']:.1f} GB")
            else:
                base_threshold = 82.0  # More headroom for high-memory systems
                reasoning.append(f"{detected['total_memory_gb']:.1f} GB (high memory)")
        else:
            base_threshold = DEFAULT_MEMORY_THRESHOLD

        # Adjust based on typical memory usage
        typical_memory = usage.get('typical_memory', 0)
        peak_memory = usage.get('peak_memory', 0)

        if typical_memory > 0:
            # If system typically uses a lot of memory, set threshold higher
            if typical_memory > 60:
                threshold = min(90.0, base_threshold + 8.0)
                reasoning.append(f"high typical usage ({typical_memory:.1f}%)")
            elif typical_memory > 40:
                threshold = min(87.0, base_threshold + 5.0)
                reasoning.append(f"moderate typical usage ({typical_memory:.1f}%)")
            elif typical_memory < 20:
                # Low usage - more conservative
                threshold = max(70.0, base_threshold - 5.0)
                reasoning.append(f"low typical usage ({typical_memory:.1f}%)")
            else:
                threshold = base_threshold
                reasoning.append(f"typical usage ({typical_memory:.1f}%)")
        else:
            threshold = base_threshold

        # Ensure threshold is within reasonable bounds
        threshold = max(65.0, min(92.0, threshold))

        os.environ['MEMORY_THRESHOLD'] = str(threshold)
        auto_detected_values['MEMORY_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"MEMORY_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    if not os.getenv('DISK_THRESHOLD'):
        threshold = None
        reasoning = []

        # Base threshold on total disk space
        if detected['total_disk_gb'] > 0:
            if detected['total_disk_gb'] < 100:
                base_threshold = 85.0
                reasoning.append(f"{detected['total_disk_gb']:.1f} GB (small disk)")
            elif detected['total_disk_gb'] < 500:
                base_threshold = 87.0
                reasoning.append(f"{detected['total_disk_gb']:.1f} GB")
            elif detected['total_disk_gb'] < 1000:
                base_threshold = 85.0
                reasoning.append(f"{detected['total_disk_gb']:.1f} GB")
            else:
                base_threshold = 88.0  # Large disks can go higher
                reasoning.append(f"{detected['total_disk_gb']:.1f} GB (large disk)")
        else:
            base_threshold = DEFAULT_DISK_THRESHOLD

        # Adjust based on current disk usage
        typical_disk = usage.get('typical_disk', 0)

        if typical_disk > 0:
            # If disk is already quite full, be more conservative
            if typical_disk > 70:
                threshold = max(85.0, base_threshold - 3.0)
                reasoning.append(f"current usage {typical_disk:.1f}% (already high)")
            elif typical_disk < 30:
                # Lots of free space - can allow higher threshold
                threshold = min(90.0, base_threshold + 3.0)
                reasoning.append(f"current usage {typical_disk:.1f}% (plenty of space)")
            else:
                threshold = base_threshold
                reasoning.append(f"current usage {typical_disk:.1f}%")
        else:
            threshold = base_threshold

        # Ensure threshold is within reasonable bounds
        threshold = max(80.0, min(92.0, threshold))

        os.environ['DISK_THRESHOLD'] = str(threshold)
        auto_detected_values['DISK_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"DISK_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    # Auto-enable GPU monitoring if GPU is available and not explicitly disabled
    if detected['gpu_available'] and not os.getenv('GPU_THRESHOLD'):
        os.environ['GPU_THRESHOLD'] = str(DEFAULT_GPU_THRESHOLD)
        auto_detected_values['GPU_THRESHOLD'] = str(DEFAULT_GPU_THRESHOLD)
        adaptations_applied.append("GPU monitoring enabled (NVIDIA GPU detected)")

    # Suggest qBittorrent monitoring if available
    if detected['qbittorrent_available'] and not os.getenv('QBITTORRENT_MONITOR'):
        # Don't auto-enable, but log suggestion
        logger.info("qBittorrent is available. Set QBITTORRENT_MONITOR=true to enable monitoring")


    if detected['platform'] == 'Windows':

        if not os.getenv('FREEZE_MONITOR_ENABLED'):

            pass
    elif detected['platform'] == 'Linux':

        if not os.getenv('MONITOR_INDIVIDUAL_PARTITIONS'):
            os.environ['MONITOR_INDIVIDUAL_PARTITIONS'] = 'true'
            auto_detected_values['MONITOR_INDIVIDUAL_PARTITIONS'] = 'true'
            adaptations_applied.append("MONITOR_INDIVIDUAL_PARTITIONS enabled (Linux detected)")

    if adaptations_applied:
        logger.info("System auto-detection applied the following adaptations:")
        for adaptation in adaptations_applied:
            logger.info(f"  - {adaptation}")
    else:
        logger.info("System auto-detection completed (no adaptations needed - using defaults or existing config)")

    return auto_detected_values


def prompt_save_config(auto_detected_values: Dict[str, str]) -> bool:
    """
    Prompt user interactively if they want to save auto-detected configuration.

    Args:
        auto_detected_values: Dictionary of auto-detected values (key -> value)

    Returns:
        True if user wants to save, False otherwise
    """
    if not auto_detected_values:
        return False

    if not sys.stdin.isatty():
        logger.info("Non-interactive mode detected - skipping save prompt")
        return False

    try:
        print("The following configuration values were auto-detected:")
        print()
        for key, value in auto_detected_values.items():
            print(f"  {key}={value}")
        print()

        while True:
            response = input("Save these settings to .env file? (y/n): ").strip().lower()
            if response in ('y', 'yes'):
                return True
            elif response in ('n', 'no'):
                return False
            else:
                print("Please enter 'y' for yes or 'n' for no.")
    except (EOFError, KeyboardInterrupt):

        logger.info("Save prompt cancelled by user")
        return False
    except Exception as e:
        logger.warning(f"Error prompting for save: {e}")
        return False


def save_auto_detected_config(auto_detected_values: Dict[str, str], project_root: Optional[Path] = None) -> bool:
    """
    Save auto-detected configuration values to .env file.

    Preserves existing .env values and only adds auto-detected values that aren't already set.
    Adds comments to indicate auto-generated values.

    Args:
        auto_detected_values: Dictionary of auto-detected values (key -> value)
        project_root: Optional project root path (defaults to detecting from script location)

    Returns:
        True if .env file was updated, False otherwise
    """
    if not auto_detected_values:
        return False

    if project_root is None:
        script_path = Path(__file__).resolve()
        project_root = script_path.parent

    env_file = project_root / '.env'


    existing_lines = []
    existing_keys = set()
    has_auto_detected_section = False

    if env_file.exists():
        try:
            with open(env_file, 'r', encoding='utf-8') as f:
                existing_lines = f.readlines()

                for line in existing_lines:
                    line_stripped = line.strip()

                    if 'Auto-detected configuration' in line_stripped:
                        has_auto_detected_section = True
                    if line_stripped and not line_stripped.startswith('#') and '=' in line_stripped:
                        key = line_stripped.split('=')[0].strip()
                        existing_keys.add(key)
        except Exception as e:
            logger.warning(f"Could not read existing .env file: {e}")
            existing_lines = []




    new_entries = []
    for key, value in auto_detected_values.items():
        if key not in existing_keys:

            escaped_value = value.replace('\\', '\\\\').replace('$', '\\$')
            new_entries.append(f"{key}={escaped_value}")

    if not new_entries:
        return False  # Nothing new to add

    # Write to .env file
    try:
        with open(env_file, 'a', encoding='utf-8') as f:
            # Add newline if file exists and doesn't end with newline
            if existing_lines and not existing_lines[-1].endswith('\n'):
                f.write('\n')


            for entry in new_entries:
                f.write(f"{entry}\n")

        logger.info(f"Saved {len(new_entries)} auto-detected configuration value(s) to {env_file}")
        logger.info(f"  File location: {env_file.absolute()}")
        saved_keys = [key for key, value in auto_detected_values.items()
                     if key not in existing_keys]
        logger.info("  Values saved: " + ", ".join(saved_keys))
        return True
    except Exception as e:
        logger.warning(f"Could not save auto-detected configuration to .env file: {e}")
        return False


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

    boolean_vars = ['QBITTORRENT_MONITOR', 'MEDIA_MONITOR_ENABLED', 'INSTALL_SYSTEMD']
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

EMBED_SEND_DELAY = 0.2
MAX_TRACKED_TORRENTS = 1000

DEFAULT_CPU_THRESHOLD = 80.0
DEFAULT_MEMORY_THRESHOLD = 80.0
DEFAULT_GPU_THRESHOLD = 80.0
DEFAULT_DISK_THRESHOLD = 80.0
DEFAULT_DISK_EARLY_WARNING_THRESHOLD = 75.0
DEFAULT_DISK_CLEANUP_THRESHOLD = 40.0
DEFAULT_DISK_EMERGENCY_THRESHOLD = 95.0
DEFAULT_TEMP_THRESHOLD = 80.0
DEFAULT_TEMP_ALERT_DURATION = 30.0
DEFAULT_NETWORK_BANDWIDTH_THRESHOLD = 1000.0
DEFAULT_PROCESS_CPU_THRESHOLD = 50.0
DEFAULT_PROCESS_MEMORY_THRESHOLD = 10.0
DEFAULT_CHECK_INTERVAL = 5.0
DEFAULT_ALERT_COOLDOWN = 300.0
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


MAX_PARTITIONS_DISPLAY = 6
MAX_PROCESSES_DISPLAY = 5
MAX_REASONS_DISPLAY = 3
MAX_INDICATORS_DISPLAY = 8
MAX_RECOMMENDATIONS_DISPLAY = 5
MAX_CRASH_TYPES_DISPLAY = 5
MAX_HARDWARE_ERRORS_DISPLAY = 5
MAX_WARNINGS_DISPLAY = 5
MAX_DIRECTORIES_DISPLAY = 5

LOG_CLEANUP_INTERVAL_SECONDS = 1800
DISK_CLEANUP_COOLDOWN_HIGH = 300
DISK_CLEANUP_COOLDOWN_MEDIUM = 900
DISK_CLEANUP_COOLDOWN_LOW = 1800
DISK_CLEANUP_COOLDOWN_NORMAL = 3600
DISK_EMERGENCY_COOLDOWN = 300

_env_cache = {}


def get_cached_env(key: str, default: str = None) -> Optional[str]:
    """
    Get environment variable with caching to avoid repeated lookups.

    Environment variables are cached after first access to improve performance.
    Values are validated for security before caching.

    Args:
        key: Environment variable name
        default: Default value if variable is not set (default: None)

    Returns:
        Environment variable value or default if not set
    """
    if key not in _env_cache:
        value = os.getenv(key, default)
        if value:
            from util.security import validate_environment_variable
            is_valid, error_msg = validate_environment_variable(key, value, max_length=10000)
            if not is_valid:
                logger.warning(f"Environment variable {key} validation failed: {error_msg}")
        _env_cache[key] = value
    return _env_cache[key]


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


def get_env_float(key: str, default: float, min_val: Optional[float] = None, max_val: Optional[float] = None) -> float:
    """
    Safely get float value from environment variable with optional range validation.

    Handles empty strings, None values, and invalid conversions gracefully.
    Returns default value on any error. Validates range if min_val/max_val provided.

    Args:
        key (str): Environment variable name
        default (float): Default value to return if variable is not set or invalid
        min_val (Optional[float]): Optional minimum allowed value
        max_val (Optional[float]): Optional maximum allowed value

    Returns:
        float: Float value from environment variable or default
    """
    value = get_cached_env(key, str(default))
    if not value or value.strip() == '':
        return default
    try:
        float_value = float(value)

        if min_val is not None and float_value < min_val:
            logger.warning(f"{key}={float_value} is below minimum {min_val}, using default {default}")
            return default
        if max_val is not None and float_value > max_val:
            logger.warning(f"{key}={float_value} is above maximum {max_val}, using default {default}")
            return default

        return float_value
    except (ValueError, TypeError):
        return default


def get_env_int(key: str, default: int, min_val: Optional[int] = None, max_val: Optional[int] = None) -> int:
    """
    Safely get integer value from environment variable with optional range validation.

    Handles empty strings, None values, and invalid conversions gracefully.
    Returns default value on any error. Validates range if min_val/max_val provided.

    Args:
        key (str): Environment variable name
        default (int): Default value to return if variable is not set or invalid
        min_val (Optional[int]): Optional minimum allowed value
        max_val (Optional[int]): Optional maximum allowed value

    Returns:
        int: Integer value from environment variable or default
    """
    value = get_cached_env(key, str(default))
    if not value or value.strip() == '':
        return default
    try:
        int_value = int(value)

        if min_val is not None and int_value < min_val:
            logger.warning(f"{key}={int_value} is below minimum {min_val}, using default {default}")
            return default
        if max_val is not None and int_value > max_val:
            logger.warning(f"{key}={int_value} is above maximum {max_val}, using default {default}")
            return default

        return int_value
    except (ValueError, TypeError):
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
    value = get_cached_env(key, str(default).lower())
    if not value or value.strip() == '':
        return default
    value_lower = value.strip().lower()
    if value_lower in ('true', '1', 'yes', 'on'):
        return True
    elif value_lower in ('false', '0', 'no', 'off', ''):
        return False
    else:
        logger.warning(f"Invalid boolean value '{value}' for {key}, defaulting to {default}")
        return default


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
        log_monitor = LogMonitor()
        log_sizes = log_monitor.get_all_log_sizes()
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
        kernel_status = kernel_monitor.get_kernel_status()
        if platform.system() == 'Linux':
            stability = kernel_monitor.check_system_stability()
            add_result("Kernel monitor", True, f"risk level: {stability.get('risk_level', 'unknown')}")
        else:
            add_result("Kernel monitor", True, "skipped (not Linux)")
    except Exception as e:
        add_result("Kernel monitor", False, str(e))

    try:
        disk_io = disk_monitor.get_disk_io_counters(perdisk=False)
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
        system_info = system_monitor.get_system_info()
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


def run_with_timeout(func, timeout: float = 5.0, default_value=None, operation_name: str = "operation"):
    """
    Run a function with a timeout using ThreadPoolExecutor.

    Args:
        func: Function to execute (callable that takes no arguments)
        timeout: Maximum time to wait in seconds (default: 5.0)
        default_value: Value to return if timeout occurs (default: None)
        operation_name: Name of the operation for logging (default: "operation")

    Returns:
        Function result or default_value if timeout
    """
    try:
        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(func)
            return future.result(timeout=timeout)
    except FutureTimeoutError:
        logger.warning(f"{operation_name} timed out after {timeout}s, using default value")
        return default_value
    except Exception as e:
        logger.error(f"Error in {operation_name}: {e}")
        raise


def main(
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
    kernel_monitor: Optional[KernelMonitor] = None
):
    """
    Main function for comprehensive server monitoring with Discord alerts.

    Args:
        cpu_monitor: Optional CPU monitor instance (for dependency injection/testing)
        memory_monitor: Optional memory monitor instance
        system_monitor: Optional system monitor instance
        gpu_monitor: Optional GPU monitor instance
        disk_monitor: Optional disk monitor instance
        network_monitor: Optional network monitor instance
        sensors_monitor: Optional sensors monitor instance
        process_monitor: Optional process monitor instance
        qbittorrent_monitor: Optional qBittorrent monitor instance
        log_monitor: Optional log monitor instance
    """
    if '--test' in sys.argv:
        ok = run_self_test()
        sys.exit(0 if ok else 1)

    install_systemd = get_env_bool('INSTALL_SYSTEMD', False)
    if '--install-systemd' in sys.argv or install_systemd:
        logger.info("Systemd installation requested...")
        if is_systemd_available():
            service_name = get_cached_env('SYSTEMD_SERVICE_NAME', 'server-monitor')
            force = '--force' in sys.argv or get_env_bool('SYSTEMD_FORCE_INSTALL', False)
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


    thresholds_already_set = (
        os.getenv('CPU_THRESHOLD') is not None or
        os.getenv('MEMORY_THRESHOLD') is not None or
        os.getenv('DISK_THRESHOLD') is not None
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
            logger.debug("qBittorrent webhook not configured (set QBITTORRENT_WEBHOOK to enable)")

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
        logger.debug("Disk webhook not configured (set DISK_WEBHOOK to enable)")

    crash_webhook = None
    crash_webhook_url = get_cached_env('CRASH_WEBHOOK')
    if crash_webhook_url:
        try:
            crash_webhook = DiscordWebhook(webhook_url=crash_webhook_url)
            logger.info("Crash Discord webhook connected")
            set_crash_webhook(crash_webhook)
        except ValueError:
            logger.warning("Crash webhook URL invalid")
        except Exception as e:
            logger.warning(f"Failed to connect to crash webhook: {e}")
    else:
        logger.debug("Crash webhook not configured (set CRASH_WEBHOOK to enable)")

    cpu_threshold = get_env_float('CPU_THRESHOLD', DEFAULT_CPU_THRESHOLD)
    memory_threshold = get_env_float('MEMORY_THRESHOLD', DEFAULT_MEMORY_THRESHOLD)
    gpu_threshold = get_env_float('GPU_THRESHOLD', DEFAULT_GPU_THRESHOLD)
    disk_threshold = get_env_float('DISK_THRESHOLD', DEFAULT_DISK_THRESHOLD)
    disk_early_warning_threshold = get_env_float('DISK_EARLY_WARNING_THRESHOLD', DEFAULT_DISK_EARLY_WARNING_THRESHOLD)
    disk_cleanup_threshold = get_env_float('DISK_CLEANUP_THRESHOLD', DEFAULT_DISK_CLEANUP_THRESHOLD)
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
    
    watchdog_monitor = WatchdogMonitor(
        timeout=monitoring_watchdog_timeout,
        auto_restart=watchdog_auto_restart,
        restart_cooldown=watchdog_restart_cooldown,
        service_name=systemd_service_name,
        alert_cooldown=alert_cooldown,
        webhook=webhook,
        crash_webhook=crash_webhook
    )
    watchdog_monitor.start()
    
    freeze_threshold = get_env_float('FREEZE_THRESHOLD', DEFAULT_FREEZE_THRESHOLD)
    freeze_heartbeat_timeout = get_env_float('FREEZE_HEARTBEAT_TIMEOUT', DEFAULT_FREEZE_HEARTBEAT_TIMEOUT)
    freeze_monitor_enabled = get_env_bool('FREEZE_MONITOR_ENABLED', False)

    media_directory = get_cached_env('MEDIA_DIRECTORY', '/home/media')
    if media_directory and ('..' in media_directory or media_directory.startswith('/proc') or media_directory.startswith('/sys')):
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
    disk_emergency_threshold = clamp_threshold(disk_emergency_threshold, 90.0, 99.0)
    if disk_early_warning_threshold >= disk_threshold:
        disk_early_warning_threshold = max(0.0, disk_threshold - 5.0)
        logger.warning(f"Early warning threshold adjusted to {disk_early_warning_threshold:.1f}% (must be less than main threshold)")
    if disk_cleanup_threshold >= disk_threshold:
        disk_cleanup_threshold = max(0.0, disk_threshold - 10.0)
        logger.warning(f"Cleanup threshold adjusted to {disk_cleanup_threshold:.1f}% (must be less than main threshold)")
    if disk_emergency_threshold <= disk_threshold:
        disk_emergency_threshold = min(99.0, disk_threshold + 10.0)
        logger.warning(f"Emergency threshold adjusted to {disk_emergency_threshold:.1f}% (must be greater than main threshold)")
    temp_threshold = max(0.0, temp_threshold)
    temp_alert_duration = max(0.0, temp_alert_duration)
    process_cpu_threshold = clamp_threshold(process_cpu_threshold, 0.0, 100.0)
    process_memory_threshold = clamp_threshold(process_memory_threshold, 0.0, 100.0)
    check_interval = max(0.1, check_interval)
    alert_cooldown = max(0.0, alert_cooldown)
    freeze_threshold = max(10.0, freeze_threshold)
    freeze_heartbeat_timeout = max(20.0, freeze_heartbeat_timeout)

    freeze_monitor = None
    if freeze_monitor_enabled:
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
    logger.info(f"Disk: {total_disk['percent']:.1f}% used | Threshold: {disk_threshold:.1f}% | Early Warning: {disk_early_warning_threshold:.1f}% | Cleanup: {disk_cleanup_threshold:.1f}% | Emergency: {disk_emergency_threshold:.1f}%")
    if monitor_individual_partitions:
        logger.info(f"Individual partition monitoring: Enabled")

    if gpu_monitor.nvidia_smi_available:
        gpu_count = gpu_monitor.get_gpu_count()
        gpu_name = ""
        if gpu_count > 0:
            gpu_info = gpu_monitor.get_gpu_info(0)
            gpu_name = f" | {gpu_info.get('name', 'N/A')}"
        logger.info(f"GPU: {gpu_count} detected | Threshold: {gpu_threshold:.1f}%{gpu_name}")

    if sensors_monitor.is_available():
        cpu_temp = sensors_monitor.get_cpu_temperature()
        logger.info(f"Sensors: {sensors_monitor.format_temperature(cpu_temp)} | Threshold: {temp_threshold:.1f}C | Alert Duration: {temp_alert_duration:.1f}s")

    logger.info(f"Processes: CPU threshold {process_cpu_threshold:.1f}% | Memory threshold {process_memory_threshold:.1f}%")
    logger.info(f"Network: Bandwidth threshold {network_bandwidth_threshold:.1f} MB/s")
    if freeze_monitor_enabled:
        logger.info(f"Freeze Detection: Threshold {freeze_threshold:.1f}s | Heartbeat timeout {freeze_heartbeat_timeout:.1f}s")
    else:
        logger.info("Freeze Detection: disabled")
    if media_monitor_enabled:
        if Path(media_directory).exists():
            logger.info(f"Directory Monitoring: {media_directory} | Threshold: {media_directory_threshold_gb:.1f} GB")
        else:
            logger.warning(f"Directory Monitoring enabled but path not found: {media_directory}. Skipping directory monitoring.")
    else:
        logger.info("Directory Monitoring disabled (set MEDIA_MONITOR_ENABLED=true to enable)")

    log_threshold_mb = get_env_float('LOG_THRESHOLD_MB', 100.0)
    log_check_interval = get_env_int('LOG_CHECK_INTERVAL', 20)
    log_monitor.set_threshold(log_threshold_mb)
    logger.info(f"Log Monitoring: Threshold: {log_threshold_mb:.1f} MB | Check interval: {log_check_interval} cycles | Auto-clean: Enabled")

    kernel_check_interval = get_env_int('KERNEL_CHECK_INTERVAL', DEFAULT_KERNEL_CHECK_INTERVAL)
    kernel_stability_check_interval = get_env_int('KERNEL_STABILITY_CHECK_INTERVAL', DEFAULT_KERNEL_STABILITY_CHECK_INTERVAL)
    kernel_check_since_minutes = get_env_int('KERNEL_CHECK_SINCE_MINUTES', DEFAULT_KERNEL_CHECK_SINCE_MINUTES)
    kernel_monitor_enabled = get_env_bool('KERNEL_MONITOR_ENABLED', True)
    if kernel_monitor_enabled:
        logger.info(f"Kernel Monitoring: Crash check interval: {kernel_check_interval} cycles | Stability check: {kernel_stability_check_interval} cycles | Lookback: {kernel_check_since_minutes} minutes")
    else:
        logger.info("Kernel Monitoring: Disabled (set KERNEL_MONITOR_ENABLED=true to enable)")

    qbittorrent_connected = False
    if qbittorrent_monitor:
        if qbittorrent_monitor.password:
            logger.debug("Checking qBittorrent connection...")
            if qbittorrent_monitor.is_available():
                try:
                    summary = qbittorrent_monitor.get_torrent_summary()
                    logger.info(f"qBittorrent: Connected | {summary['total']} torrents ({summary['downloading']} downloading, {summary['seeding']} seeding)")
                    qbittorrent_connected = True
                except Exception as e:
                    logger.warning(f"qBittorrent: Connected but error getting summary: {e}")
            else:
                error_msg = getattr(qbittorrent_monitor, '_last_error', None) or "Unknown error"
                logger.warning(f"qBittorrent: Not available - {error_msg}")
        else:
            logger.info("qBittorrent: Not configured (set QBITTORRENT_PASSWORD to enable)")
    else:
        logger.info("qBittorrent: Monitoring disabled")

    logger.info(f"Settings: Check every {check_interval:.1f}s | Cooldown {alert_cooldown:.1f}s")

    web_port = get_env_int('WEB_PORT', 8080)
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

        if qbittorrent_connected and qbittorrent_webhook:
            try:
                downloading = qbittorrent_monitor.get_downloading_torrents()
                seeding = qbittorrent_monitor.get_seeding_torrents()

                if downloading or seeding:
                    logger.info(f"Sending qBittorrent status to Discord ({len(downloading)} downloading, {len(seeding)} seeding)...")
                    success = qbittorrent_webhook.send_torrent_list(downloading, seeding)
                    if success:
                        logger.info("qBittorrent status sent to Discord")
                    else:
                        logger.warning("Failed to send qBittorrent status to Discord")
                else:
                    logger.debug("No active torrents to report")
            except Exception as e:
                logger.warning(f"Error sending qBittorrent status to Discord: {e}")

    logger.info("Starting continuous monitoring (Press Ctrl+C to stop)")


    ERROR_LOG_FILE = os.getenv('ERROR_LOG_FILE', 'server_monitor_errors.log')
    error_log_lock = threading.Lock()
    last_error_webhook_time = 0
    ERROR_WEBHOOK_COOLDOWN = 300.0

    def log_error_to_file_and_webhook(error: Exception, context: str = "", include_traceback: bool = True):
        """
        Log error to dedicated error log file and send webhook notification.

        Args:
            error: The exception that occurred
            context: Additional context about where the error occurred
            include_traceback: Whether to include full traceback in log
        """
        nonlocal last_error_webhook_time

        timestamp = datetime.now().strftime('%Y-%m-%d %H:%M:%S')
        error_message = str(error)
        error_type = type(error).__name__


        log_entry_parts = [f"[{timestamp}] {error_type}: {error_message}"]
        if context:
            log_entry_parts.append(f"Context: {context}")
        if include_traceback:
            log_entry_parts.append(f"Traceback:\n{traceback.format_exc()}")
        log_entry = "\n".join(log_entry_parts) + "\n" + ("=" * 80) + "\n"


        try:
            with error_log_lock:
                with open(ERROR_LOG_FILE, 'a', encoding='utf-8') as f:
                    f.write(log_entry)
        except (OSError, PermissionError) as e:
            logger.warning(f"Could not write to error log file {ERROR_LOG_FILE}: {e}")


        current_time = time.time()
        time_since_last_webhook = current_time - last_error_webhook_time

        target_webhook = crash_webhook if crash_webhook else webhook

        if target_webhook and time_since_last_webhook >= ERROR_WEBHOOK_COOLDOWN:
            try:

                traceback_text = traceback.format_exc() if include_traceback else ""
                if len(traceback_text) > 1000:
                    traceback_text = traceback_text[:1000] + "... (truncated)"

                fields = [
                    {'name': 'Error Type', 'value': error_type, 'inline': True},
                    {'name': 'Error Message', 'value': error_message[:500], 'inline': False},
                ]

                if context:
                    fields.append({'name': 'Context', 'value': context[:500], 'inline': False})

                if traceback_text:
                    fields.append({'name': 'Traceback', 'value': f"```\n{traceback_text}\n```", 'inline': False})

                success = target_webhook.send_embed(
                    title='Monitoring Error Detected',
                    description=f'An error occurred in the monitoring system',
                    fields=fields,
                    color=0xFF6B6B,
                    footer='Server Monitor - Error Alert',
                    timestamp=True
                )

                if success:
                    last_error_webhook_time = current_time
                    webhook_name = "Crash webhook" if crash_webhook else "Discord"
                    logger.info(f"    Error notification sent to {webhook_name}")
            except Exception as e:
                logger.warning(f"Failed to send error notification to webhook: {e}")

    monitoring_start_time = time.time()

    last_cpu_alert_time = 0
    last_memory_alert_time = 0
    last_gpu_alert_time = 0
    last_disk_alert_time = 0
    last_disk_cleanup_time = 0
    last_partition_cleanup_times = {}
    last_partition_alert_times = {}
    partition_alert_sent = {}
    disk_cleanup_cooldown = DISK_CLEANUP_COOLDOWN_NORMAL
    disk_emergency_cooldown = DISK_EMERGENCY_COOLDOWN
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
    log_check_counter = 0
    last_log_alert_time = {}
    cleaned_logs = set()
    kernel_check_counter = 0
    kernel_stability_check_counter = 0
    last_kernel_alert_time = 0
    last_kernel_stability_alert_time = 0
    kernel_alert_sent = False
    kernel_stability_alert_sent = False
    last_kernel_stability_alert_hash = None
    qbittorrent_check_counter = 0
    NETWORK_CHECK_INTERVAL = 2
    PROCESS_CHECK_INTERVAL = 3
    FREEZE_CHECK_INTERVAL = 1
    QBITTORRENT_CHECK_INTERVAL = 2
    tracked_torrents = {}


    monitoring_watchdog_timeout = get_env_float('MONITORING_WATCHDOG_TIMEOUT', DEFAULT_MONITORING_WATCHDOG_TIMEOUT)
    watchdog_auto_restart = get_env_bool('WATCHDOG_AUTO_RESTART', DEFAULT_WATCHDOG_AUTO_RESTART)
    watchdog_restart_cooldown = get_env_float('WATCHDOG_RESTART_COOLDOWN', DEFAULT_WATCHDOG_RESTART_COOLDOWN)
    systemd_service_name = get_cached_env('SYSTEMD_SERVICE_NAME', 'server-monitor')

    shutdown_state = {
        'graceful_shutdown': False,
        'shutdown_reason': 'unknown',
        'shutdown_time': None,
        'lock': threading.Lock()
    }

    def signal_handler(signum, frame):
        """Handle shutdown signals gracefully."""
        signal_name = signal.Signals(signum).name if hasattr(signal, 'Signals') else str(signum)
        with shutdown_state['lock']:
            shutdown_state['graceful_shutdown'] = True
            shutdown_state['shutdown_reason'] = f'signal_{signal_name}'
            shutdown_state['shutdown_time'] = time.time()
        logger.info(f"Received {signal_name} signal - initiating graceful shutdown...")
        raise KeyboardInterrupt

    def atexit_handler():
        """Handler called on normal program exit."""
        with shutdown_state['lock']:
            if not shutdown_state['graceful_shutdown']:


                if shutdown_state['shutdown_reason'] == 'unknown':

                    shutdown_state['shutdown_reason'] = 'atexit_unknown_crash'
                    logger.warning("Program exited via atexit without graceful shutdown flag - possible crash")
                elif shutdown_state['shutdown_reason'] == 'crash_exception':

                    pass
                else:
                    shutdown_state['graceful_shutdown'] = True

    if platform.system() != 'Windows':
        signal.signal(signal.SIGTERM, signal_handler)
        signal.signal(signal.SIGINT, signal_handler)
        try:
            signal.signal(signal.SIGQUIT, signal_handler)
        except (AttributeError, ValueError):
            pass
    else:
        try:
            signal.signal(signal.SIGTERM, signal_handler)
            signal.signal(signal.SIGINT, signal_handler)
        except (AttributeError, ValueError):
            pass

    atexit.register(atexit_handler)


    try:
        while True:
            try:

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()



                if watchdog_monitor:
                    watchdog_monitor.reset_alert_state()

                current_time = time.time()
                top_cpu_processes = None
                top_memory_processes = None


                try:
                    if freeze_monitor_enabled and freeze_monitor:
                        freeze_monitor.update_heartbeat()
                except Exception as e:
                    logger.warning(f"Error updating freeze monitor heartbeat: {e}")

                try:
                    cpu_percent = run_with_timeout(
                        lambda: cpu_monitor.get_cpu_percent(interval=1.0),
                        timeout=3.0,
                        default_value=0.0,
                        operation_name="CPU percent collection"
                    )
                    cpu_per_core = run_with_timeout(
                        lambda: cpu_monitor.get_cpu_percent_per_core(interval=0.1),
                        timeout=2.0,
                        default_value=[],
                        operation_name="CPU per-core collection"
                    )
                except Exception as e:
                    logger.error(f"Error getting CPU metrics: {e}")
                    log_error_to_file_and_webhook(e, context="CPU metrics collection", include_traceback=True)
                    cpu_percent = 0.0
                    cpu_per_core = []

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()

                try:
                    memory_percent = memory_monitor.get_memory_percent()
                    memory_info = memory_monitor.get_memory_info()
                    swap_info = memory_monitor.get_swap_info()
                except Exception as e:
                    logger.error(f"Error getting memory metrics: {e}")
                    log_error_to_file_and_webhook(e, context="Memory metrics collection", include_traceback=True)
                    memory_percent = 0.0
                    memory_info = {}
                    swap_info = {}

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()

                gpu_utilization = None
                gpu_data = None
                try:
                    if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
                        gpu_utilization = run_with_timeout(
                            lambda: gpu_monitor.get_gpu_utilization(0),
                            timeout=15.0,
                            default_value={'gpu': 0.0, 'memory': 0.0},
                            operation_name="GPU utilization collection"
                        )
                        gpu_data = run_with_timeout(
                            lambda: gpu_monitor.get_gpu_comprehensive(0),
                            timeout=20.0,
                            default_value={},
                            operation_name="GPU comprehensive data collection"
                        )
                except Exception as e:
                    logger.debug(f"Error getting GPU metrics: {e}")

                try:

                    all_partitions = disk_monitor.get_all_partitions()

                    total_disk = disk_monitor.get_total_disk_usage()
                    disk_percent = total_disk.get('percent', 0.0) if total_disk else 0.0
                except Exception as e:
                    logger.error(f"Error getting disk usage: {e}")
                    log_error_to_file_and_webhook(e, context="Disk usage collection", include_traceback=True)
                    all_partitions = []
                    total_disk = {}
                    disk_percent = 0.0

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()

                if disk_percent >= 50.0:
                    time_since_last_cleanup = current_time - last_disk_cleanup_time

                    if time_since_last_cleanup >= LOG_CLEANUP_INTERVAL_SECONDS:
                        logger.warning(f"[CLEANUP] Disk at {disk_percent:.1f}% - starting log cleanup...")
                        try:
                            cleanup_stats = run_with_timeout(
                                lambda: disk_monitor.cleanup_logs_and_restart_services(log_monitor=log_monitor),
                                timeout=300.0,
                                default_value={'logs_cleaned': 0, 'total_size_cleared': 0},
                                operation_name="Log cleanup operation"
                            )

                            if watchdog_monitor:
                                watchdog_monitor.update_heartbeat()

                            if cleanup_stats.get('logs_cleaned', 0) > 0:
                                logger.warning(f"[CLEANUP] Cleared {cleanup_stats['logs_cleaned']} log file(s), "
                                             f"freed {format_bytes(cleanup_stats['total_size_cleared'])}")

                                final_disk = disk_monitor.get_disk_usage('/')
                                if final_disk:
                                    logger.info(f"[CLEANUP] Disk usage after log cleanup: {final_disk['percent']:.1f}%")

                            last_disk_cleanup_time = current_time
                        except Exception as e:
                            logger.error(f"[CLEANUP] Error during log cleanup: {e}")
                            log_error_to_file_and_webhook(e, context="Log cleanup operation", include_traceback=True)
                            last_disk_cleanup_time = current_time

                if disk_percent >= disk_emergency_threshold:
                    time_since_last_cleanup = current_time - last_disk_cleanup_time

                    if time_since_last_cleanup >= disk_emergency_cooldown:
                        logger.critical(f"[EMERGENCY] Disk usage at {disk_percent:.1f}% (emergency threshold: {disk_emergency_threshold:.1f}%) - CRITICAL! Starting emergency cleanup...")

                        try:
                            cleanup_stats = run_with_timeout(
                                lambda: disk_monitor.emergency_cleanup_disk_space(mountpoint='/', log_monitor=log_monitor),
                                timeout=600.0,
                                default_value={'total_size_cleared': 0, 'files_removed': 0, 'dirs_removed': 0, 'logs_cleaned': 0, 'errors': []},
                                operation_name="Emergency disk cleanup operation"
                            )

                            if watchdog_monitor:
                                watchdog_monitor.update_heartbeat()

                            if cleanup_stats['total_size_cleared'] > 0:
                                logger.critical(f"[EMERGENCY] Emergency cleanup cleared {format_bytes(cleanup_stats['total_size_cleared'])} "
                                              f"({cleanup_stats['files_removed']} files, {cleanup_stats['dirs_removed']} dirs, {cleanup_stats.get('logs_cleaned', 0)} logs)")

                                if cleanup_stats.get('final_usage'):
                                    logger.critical(f"[EMERGENCY] Disk usage after emergency cleanup: {cleanup_stats['final_usage']:.1f}%")

                                    if cleanup_stats['final_usage'] >= disk_emergency_threshold:
                                        logger.critical(f"[EMERGENCY] Disk still critically full after cleanup! Manual intervention required!")

                                if disk_webhook:
                                    fields = [
                                        {'name': 'EMERGENCY CLEANUP', 'value': 'Disk critically full - aggressive cleanup performed', 'inline': False},
                                        {'name': 'Usage Before', 'value': f"{cleanup_stats.get('initial_usage', disk_percent):.1f}%", 'inline': True},
                                        {'name': 'Usage After', 'value': f"{cleanup_stats.get('final_usage', disk_percent):.1f}%", 'inline': True},
                                        {'name': 'Space Cleared', 'value': format_bytes(cleanup_stats['total_size_cleared']), 'inline': True},
                                        {'name': 'Files Removed', 'value': str(cleanup_stats['files_removed']), 'inline': True},
                                        {'name': 'Logs Cleaned', 'value': str(cleanup_stats.get('logs_cleaned', 0)), 'inline': True},
                                    ]

                                    if cleanup_stats.get('final_usage', disk_percent) >= disk_emergency_threshold:
                                        fields.append({
                                            'name': 'CRITICAL',
                                            'value': 'Disk still critically full after cleanup! Manual intervention required!',
                                            'inline': False
                                        })

                                    if cleanup_stats['errors']:
                                        error_count = len(cleanup_stats['errors'])
                                        fields.append({
                                            'name': 'Errors',
                                            'value': f"{error_count} error(s) occurred during emergency cleanup",
                                            'inline': False
                                        })

                                    success = disk_webhook.send_embed(
                                        title='EMERGENCY: Disk Critically Full',
                                        description=f'Emergency cleanup performed to prevent disk from reaching 100%',
                                        fields=fields,
                                        footer='Server Monitor - Emergency',
                                        timestamp=True
                                    )
                                    if success:
                                        logger.info("    Discord emergency notification sent")
                            else:
                                logger.critical(f"[EMERGENCY] No space could be freed! Disk may crash if it reaches 100%!")
                                if disk_webhook:
                                    disk_webhook.send_embed(
                                        title='CRITICAL: Disk Cleanup Failed',
                                        description=f'Disk at {disk_percent:.1f}% and emergency cleanup could not free space! Manual intervention required immediately!',
                                        fields=[
                                            {'name': 'Current Usage', 'value': f"{disk_percent:.1f}%", 'inline': False},
                                            {'name': 'Emergency Threshold', 'value': f"{disk_emergency_threshold:.1f}%", 'inline': True},
                                            {'name': 'Status', 'value': 'CRITICAL - Manual intervention required', 'inline': False},
                                        ],
                                        footer='Server Monitor - Critical Alert',
                                        timestamp=True
                                    )

                            last_disk_cleanup_time = current_time
                        except Exception as e:
                            logger.critical(f"[EMERGENCY] Error during emergency disk cleanup: {e}")
                            log_error_to_file_and_webhook(e, context="Emergency disk cleanup operation", include_traceback=True)
                            last_disk_cleanup_time = current_time

                elif disk_percent >= disk_cleanup_threshold:
                    time_since_last_cleanup = current_time - last_disk_cleanup_time

                    effective_cooldown = disk_cleanup_cooldown
                    if disk_percent >= 80.0:
                        effective_cooldown = DISK_CLEANUP_COOLDOWN_HIGH
                    elif disk_percent >= 70.0:
                        effective_cooldown = DISK_CLEANUP_COOLDOWN_MEDIUM
                    elif disk_percent >= 60.0:
                        effective_cooldown = DISK_CLEANUP_COOLDOWN_LOW

                    if time_since_last_cleanup >= effective_cooldown:
                        logger.warning(f"[CLEANUP] Disk usage at {disk_percent:.1f}% (cleanup threshold: {disk_cleanup_threshold:.1f}%), starting automatic cleanup...")

                        try:
                            target_free_percent = 20.0
                            cleanup_stats = run_with_timeout(
                                lambda: disk_monitor.cleanup_disk_space(mountpoint='/', target_free_percent=target_free_percent, log_monitor=log_monitor),
                                timeout=300.0,
                                default_value={'total_size_cleared': 0, 'files_removed': 0, 'dirs_removed': 0, 'logs_cleaned': 0},
                                operation_name="Disk cleanup operation"
                            )

                            if cleanup_stats['total_size_cleared'] > 0:
                                logger.info(f"[CLEANUP] Cleaned {format_bytes(cleanup_stats['total_size_cleared'])} "
                                          f"({cleanup_stats['files_removed']} files, {cleanup_stats['dirs_removed']} dirs, {cleanup_stats.get('logs_cleaned', 0)} logs)")

                                if cleanup_stats.get('final_usage'):
                                    logger.info(f"[CLEANUP] Disk usage after cleanup: {cleanup_stats['final_usage']:.1f}%")

                                if disk_webhook:
                                    fields = [
                                        {'name': 'Usage Before', 'value': f"{cleanup_stats.get('initial_usage', disk_percent):.1f}%", 'inline': True},
                                        {'name': 'Usage After', 'value': f"{cleanup_stats.get('final_usage', disk_percent):.1f}%", 'inline': True},
                                        {'name': 'Space Cleared', 'value': format_bytes(cleanup_stats['total_size_cleared']), 'inline': True},
                                        {'name': 'Files Removed', 'value': str(cleanup_stats['files_removed']), 'inline': True},
                                        {'name': 'Directories Removed', 'value': str(cleanup_stats['dirs_removed']), 'inline': True},
                                    ]

                                    if cleanup_stats['errors']:
                                        error_count = len(cleanup_stats['errors'])
                                        fields.append({
                                            'name': 'Errors',
                                            'value': f"{error_count} error(s) occurred during cleanup",
                                            'inline': False
                                        })

                                    success = disk_webhook.send_embed(
                                        title='Disk Cleanup Performed',
                                        description=f'Automatic disk cleanup completed to prevent disk from filling up',
                                        fields=fields,
                                        footer='Server Monitor',
                                        timestamp=True
                                    )
                                    if success:
                                        logger.info("    Discord cleanup notification sent")
                            else:
                                logger.info("[CLEANUP] No files were cleaned (disk may need manual intervention)")

                            last_disk_cleanup_time = current_time
                        except Exception as e:
                            logger.error(f"[CLEANUP] Error during automatic disk cleanup: {e}")
                            log_error_to_file_and_webhook(e, context="Automatic disk cleanup operation", include_traceback=True)
                            last_disk_cleanup_time = current_time
                    else:
                        remaining_cooldown = disk_cleanup_cooldown - time_since_last_cleanup
                        logger.debug(f"Disk cleanup cooldown: {remaining_cooldown:.0f}s remaining")

                cpu_temp = None
                try:
                    if sensors_monitor.is_available():
                        cpu_temp = sensors_monitor.get_cpu_temperature()
                        if cpu_temp is None:
                            cpu_temp = 0.0
                except Exception as e:
                    logger.debug(f"Error getting temperature: {e}")
                    cpu_temp = None

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
                        if watchdog_monitor:
                            watchdog_monitor.update_heartbeat()
                    except (ValueError, ZeroDivisionError) as e:
                        logger.debug(f"Error calculating network bandwidth: {e}")
                    except Exception as e:
                        logger.debug(f"Unexpected error in network bandwidth calculation: {e}")

                try:
                    disk_io_data = system_monitor.get_disk_io()
                except (OSError, psutil.Error) as e:
                    logger.debug(f"Error getting disk I/O: {e}")

                if freeze_monitor_enabled and freeze_monitor:
                    freeze_monitor.update_metrics(
                        cpu_percent=cpu_percent,
                        memory_percent=memory_percent,
                        disk_io=disk_io_data,
                        network_io=network_io_data
                    )

                timestamp = time.strftime("%Y-%m-%d %H:%M:%S")

                disk_status_parts = []
                try:
                    if all_partitions:
                        for partition in all_partitions:
                            mountpoint = partition.get('mountpoint', partition.get('device', 'unknown'))
                            percent = partition.get('percent', 0.0)
                            if platform.system() == 'Windows':
                                display_name = mountpoint.rstrip('\\').split('\\')[-1] or mountpoint[:2]
                            else:
                                display_name = mountpoint.split('/')[-1] if mountpoint != '/' else 'root'
                                if not display_name:
                                    display_name = mountpoint
                            disk_status_parts.append(f"{display_name}: {percent:.1f}%")
                    else:
                        disk_status_parts.append(f"{disk_percent:.1f}%")
                except Exception as e:
                    logger.debug(f"Error formatting disk status: {e}")
                    disk_status_parts.append(f"{disk_percent:.1f}%")

                disk_status = " | ".join(disk_status_parts) if disk_status_parts else f"{disk_percent:.1f}%"

                metrics_parts = [
                    f"[{timestamp}] CPU: {cpu_percent:.1f}%",
                    f"Memory: {memory_percent:.1f}%",
                    f"Disk: {disk_status}"
                ]
                if gpu_utilization is not None:
                    metrics_parts.append(f"GPU: {gpu_utilization['gpu']:.1f}%")
                if cpu_temp is not None:
                    metrics_parts.append(f"Temp: {cpu_temp:.1f}C")
                if network_bandwidth is not None:
                    metrics_parts.append(f"Network: {network_bandwidth:.1f} MB/s")

                metrics_str = " | ".join(metrics_parts)
                logger.info(metrics_str)

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()

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

                try:
                    if all_partitions:
                        for partition in all_partitions:
                            partition_percent = partition.get('percent', 0.0)
                            if partition_percent >= disk_threshold:
                                mountpoint = partition.get('mountpoint', partition.get('device', 'unknown'))
                                partition_key = mountpoint

                                last_alert_time = last_partition_alert_times.get(partition_key, 0)
                                alert_sent = partition_alert_sent.get(partition_key, False)
                                time_since_last_alert = current_time - last_alert_time

                                if not alert_sent or time_since_last_alert >= alert_cooldown:
                                    if disk_webhook:
                                        logger.warning(f"[ALERT] Disk {partition_key}: {partition_percent:.1f}% (threshold: {disk_threshold:.1f}%)")

                                        partition_total = partition.get('total', 0)
                                        partition_used = partition.get('used', 0)
                                        partition_free = partition.get('free', 0)

                                        fields = [
                                            {'name': 'Mountpoint', 'value': partition_key, 'inline': True},
                                            {'name': 'Usage', 'value': f'{partition_percent:.1f}%', 'inline': True},
                                            {'name': 'Threshold', 'value': f'{disk_threshold:.1f}%', 'inline': True},
                                            {'name': 'Total Capacity', 'value': format_bytes(partition_total), 'inline': False},
                                            {'name': 'Used', 'value': format_bytes(partition_used), 'inline': True},
                                            {'name': 'Free', 'value': format_bytes(partition_free), 'inline': True},
                                        ]

                                        # Get largest directories for this partition
                                        largest_dirs = []
                                        try:
                                            if partition.get('percent', 0) >= disk_threshold:
                                                largest_dirs = disk_monitor.get_largest_directories_human(mountpoint, limit=MAX_DIRECTORIES_DISPLAY)
                                        except Exception as e:
                                            logger.debug(f"Could not get largest directories for {mountpoint}: {e}")

                                        if largest_dirs:
                                            dir_list = []
                                            for i, dir_info in enumerate(largest_dirs, 1):
                                                dir_name = Path(dir_info['path']).name
                                                dir_size = dir_info['size']
                                                dir_list.append(f"{i}. `{dir_name}`: {dir_size}")

                                            fields.append({
                                                'name': f'Largest Directories ({mountpoint})',
                                                'value': '\n'.join(dir_list) if dir_list else 'Unable to determine',
                                                'inline': False
                                            })

                                        success = disk_webhook.send_embed(
                                            title=f'Disk Usage Alert: {partition_key}',
                                            description=f'Disk usage on {partition_key} has exceeded the threshold of {disk_threshold:.1f}%',
                                            fields=fields,
                                            footer='Server Monitor',
                                            timestamp=True
                                        )
                                        if success:
                                            logger.info("    Discord alert sent")
                                        else:
                                            logger.warning("    Discord alert failed")
                                    else:
                                        logger.warning(f"Disk {partition_key}: {partition_percent:.1f}% (threshold: {disk_threshold:.1f}%)")

                                    last_partition_alert_times[partition_key] = current_time
                                    partition_alert_sent[partition_key] = True
                                else:
                                    remaining_cooldown = alert_cooldown - time_since_last_alert
                                    logger.debug(f"Disk {partition_key} alert cooldown: {remaining_cooldown:.0f}s")
                            else:
                                # Reset alert flag if below threshold
                                mountpoint = partition.get('mountpoint', partition.get('device', 'unknown'))
                                partition_key = mountpoint
                                if partition_alert_sent.get(partition_key, False):
                                    partition_alert_sent[partition_key] = False
                except Exception as e:
                    logger.debug(f"Error checking individual disks: {e}")

                # Also check aggregated total for backward compatibility (only if no individual partitions detected)
                if not all_partitions and disk_percent >= disk_threshold:
                    time_since_last_alert = current_time - last_disk_alert_time

                    if not disk_alert_sent or time_since_last_alert >= alert_cooldown:
                        if disk_webhook:
                            logger.warning(f"[ALERT] Disk: {disk_percent:.1f}% (threshold: {disk_threshold:.1f}%)")

                            fields = [
                                {'name': 'Overall Usage', 'value': f'{disk_percent:.1f}%', 'inline': True},
                                {'name': 'Threshold', 'value': f'{disk_threshold:.1f}%', 'inline': True},
                                {'name': 'Total Capacity', 'value': format_bytes(total_disk.get('total', 0)), 'inline': False},
                                {'name': 'Used', 'value': format_bytes(total_disk.get('used', 0)), 'inline': True},
                                {'name': 'Free', 'value': format_bytes(total_disk.get('free', 0)), 'inline': True},
                            ]

                            largest_dirs = []
                            try:
                                root_partition = disk_monitor.get_disk_usage('/')
                                if root_partition and root_partition['percent'] >= disk_threshold:
                                    largest_dirs = disk_monitor.get_largest_directories_human('/', limit=MAX_DIRECTORIES_DISPLAY)
                            except Exception as e:
                                logger.debug(f"Could not get largest directories: {e}")

                            if largest_dirs:
                                dir_list = []
                                for i, dir_info in enumerate(largest_dirs, 1):
                                    dir_name = Path(dir_info['path']).name
                                    dir_size = dir_info['size']
                                    dir_list.append(f"{i}. `{dir_name}`: {dir_size}")

                                fields.append({
                                    'name': 'Largest Directories (root)',
                                    'value': '\n'.join(dir_list) if dir_list else 'Unable to determine',
                                    'inline': False
                                })

                            success = disk_webhook.send_embed(
                                title='Disk Usage Alert',
                                description=f'Disk usage has exceeded the threshold of {disk_threshold:.1f}%',
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
                elif not all_partitions:
                    # Reset alert flag if below threshold and no partitions detected
                    if disk_alert_sent:
                        disk_alert_sent = False

                if monitor_individual_partitions:
                    try:
                        # Use cached all_partitions from earlier in loop (already fetched at line 1947)
                        # all_partitions already available from start of loop iteration
                        nvme_partitions = disk_monitor.get_nvme_partitions()

                        for partition in all_partitions:
                            partition_key = partition.get('mountpoint', partition.get('device', 'unknown'))
                            percent = partition['percent']
                            mountpoint = partition.get('mountpoint', '/')

                            if percent >= disk_emergency_threshold:
                                last_cleanup_time = last_partition_cleanup_times.get(partition_key, 0)
                                time_since_last_cleanup = current_time - last_cleanup_time

                                if time_since_last_cleanup >= disk_emergency_cooldown:
                                    logger.critical(f"[EMERGENCY] Partition {partition_key}: {percent:.1f}% (emergency threshold: {disk_emergency_threshold:.1f}%) - CRITICAL! Starting emergency cleanup...")

                                    try:
                                        cleanup_stats = disk_monitor.emergency_cleanup_disk_space(mountpoint=mountpoint, log_monitor=log_monitor)

                                        if cleanup_stats['total_size_cleared'] > 0:
                                            logger.critical(f"[EMERGENCY] Partition {partition_key}: Emergency cleanup cleared {format_bytes(cleanup_stats['total_size_cleared'])} "
                                                          f"({cleanup_stats['files_removed']} files, {cleanup_stats['dirs_removed']} dirs, {cleanup_stats.get('logs_cleaned', 0)} logs)")

                                            if cleanup_stats.get('final_usage'):
                                                logger.critical(f"[EMERGENCY] Partition {partition_key} usage after emergency cleanup: {cleanup_stats['final_usage']:.1f}%")

                                            if disk_webhook:
                                                fields = [
                                                    {'name': 'EMERGENCY CLEANUP', 'value': f'Partition {partition_key} critically full', 'inline': False},
                                                    {'name': 'Usage Before', 'value': f"{cleanup_stats.get('initial_usage', percent):.1f}%", 'inline': True},
                                                    {'name': 'Usage After', 'value': f"{cleanup_stats.get('final_usage', percent):.1f}%", 'inline': True},
                                                    {'name': 'Space Cleared', 'value': format_bytes(cleanup_stats['total_size_cleared']), 'inline': True},
                                                    {'name': 'Files Removed', 'value': str(cleanup_stats['files_removed']), 'inline': True},
                                                    {'name': 'Logs Cleaned', 'value': str(cleanup_stats.get('logs_cleaned', 0)), 'inline': True},
                                                ]

                                                if cleanup_stats.get('final_usage', percent) >= disk_emergency_threshold:
                                                    fields.append({
                                                        'name': 'CRITICAL',
                                                        'value': 'Partition still critically full after cleanup! Manual intervention required!',
                                                        'inline': False
                                                    })

                                                success = disk_webhook.send_embed(
                                                    title='EMERGENCY: Partition Critically Full',
                                                    description=f'Emergency cleanup performed for partition {partition_key}',
                                                    fields=fields,
                                                    footer='Server Monitor - Emergency',
                                                    timestamp=True
                                                )
                                                if success:
                                                    logger.info(f"    Discord emergency notification sent for {partition_key}")
                                        else:
                                            logger.critical(f"[EMERGENCY] Partition {partition_key}: No space could be freed!")

                                        last_partition_cleanup_times[partition_key] = current_time
                                    except Exception as e:
                                        logger.critical(f"[EMERGENCY] Error during emergency cleanup for partition {partition_key}: {e}")
                                        last_partition_cleanup_times[partition_key] = current_time

                            elif percent >= disk_cleanup_threshold:
                                last_cleanup_time = last_partition_cleanup_times.get(partition_key, 0)
                                time_since_last_cleanup = current_time - last_cleanup_time

                                if time_since_last_cleanup >= disk_cleanup_cooldown:
                                    logger.warning(f"[CLEANUP] Partition {partition_key}: {percent:.1f}% (cleanup threshold: {disk_cleanup_threshold:.1f}%), starting automatic cleanup...")

                                    try:
                                        target_free_percent = 20.0
                                        cleanup_stats = disk_monitor.cleanup_disk_space(mountpoint=mountpoint, target_free_percent=target_free_percent, log_monitor=log_monitor)

                                        if cleanup_stats['total_size_cleared'] > 0:
                                            logger.info(f"[CLEANUP] Partition {partition_key}: Cleaned {format_bytes(cleanup_stats['total_size_cleared'])} "
                                                      f"({cleanup_stats['files_removed']} files, {cleanup_stats['dirs_removed']} dirs, {cleanup_stats.get('logs_cleaned', 0)} logs)")

                                            if cleanup_stats.get('final_usage'):
                                                logger.info(f"[CLEANUP] Partition {partition_key} usage after cleanup: {cleanup_stats['final_usage']:.1f}%")

                                            if disk_webhook:
                                                fields = [
                                                    {'name': 'Partition', 'value': partition_key, 'inline': False},
                                                    {'name': 'Usage Before', 'value': f"{cleanup_stats.get('initial_usage', percent):.1f}%", 'inline': True},
                                                    {'name': 'Usage After', 'value': f"{cleanup_stats.get('final_usage', percent):.1f}%", 'inline': True},
                                                    {'name': 'Space Cleared', 'value': format_bytes(cleanup_stats['total_size_cleared']), 'inline': True},
                                                    {'name': 'Files Removed', 'value': str(cleanup_stats['files_removed']), 'inline': True},
                                                    {'name': 'Directories Removed', 'value': str(cleanup_stats['dirs_removed']), 'inline': True},
                                                ]

                                                if cleanup_stats['errors']:
                                                    error_count = len(cleanup_stats['errors'])
                                                    fields.append({
                                                        'name': 'Errors',
                                                        'value': f"{error_count} error(s) occurred during cleanup",
                                                        'inline': False
                                                    })

                                                success = disk_webhook.send_embed(
                                                    title='Partition Cleanup Performed',
                                                    description=f'Automatic cleanup completed for partition {partition_key}',
                                                    fields=fields,
                                                    footer='Server Monitor',
                                                    timestamp=True
                                                )
                                                if success:
                                                    logger.info(f"    Discord partition cleanup notification sent for {partition_key}")
                                        else:
                                            logger.info(f"[CLEANUP] Partition {partition_key}: No files were cleaned")

                                        last_partition_cleanup_times[partition_key] = current_time
                                    except Exception as e:
                                        logger.error(f"[CLEANUP] Error during automatic cleanup for partition {partition_key}: {e}")
                                        last_partition_cleanup_times[partition_key] = current_time

                            last_alert_time = last_partition_alert_times.get(partition_key, 0)
                            time_since_last_alert = current_time - last_alert_time

                            if percent >= disk_threshold:
                                if not partition_alert_sent.get(partition_key, False) or time_since_last_alert >= alert_cooldown:
                                    if disk_webhook:
                                        logger.warning(f"[ALERT] Partition {partition_key}: {percent:.1f}% (threshold: {disk_threshold:.1f}%)")
                                        success = disk_webhook.send_partition_alert(
                                            partition=partition,
                                            threshold=disk_threshold,
                                            is_early_warning=False
                                        )
                                        if success:
                                            logger.info(f"    Discord partition alert sent for {partition_key}")
                                        else:
                                            logger.warning(f"    Discord partition alert failed for {partition_key}")
                                    else:
                                        logger.warning(f"Partition {partition_key}: {percent:.1f}% (threshold: {disk_threshold:.1f}%)")

                                    last_partition_alert_times[partition_key] = current_time
                                    partition_alert_sent[partition_key] = True
                                else:
                                    remaining_cooldown = alert_cooldown - time_since_last_alert
                                    logger.debug(f"Partition {partition_key} alert cooldown: {remaining_cooldown:.0f}s")
                            elif percent >= disk_early_warning_threshold:
                                if not partition_alert_sent.get(partition_key, False) or time_since_last_alert >= alert_cooldown:
                                    if disk_webhook:
                                        logger.warning(f"[WARNING] Partition {partition_key}: {percent:.1f}% (early warning: {disk_early_warning_threshold:.1f}%)")
                                        success = disk_webhook.send_partition_alert(
                                            partition=partition,
                                            threshold=disk_early_warning_threshold,
                                            is_early_warning=True
                                        )
                                        if success:
                                            logger.info(f"    Discord partition early warning sent for {partition_key}")
                                        else:
                                            logger.warning(f"    Discord partition early warning failed for {partition_key}")

                                    last_partition_alert_times[partition_key] = current_time
                                    partition_alert_sent[partition_key] = True
                            else:
                                if partition_alert_sent.get(partition_key, False):
                                    partition_alert_sent[partition_key] = False

                        if nvme_partitions:
                            for nvme_partition in nvme_partitions:
                                nvme_key = nvme_partition.get('mountpoint', nvme_partition.get('device', 'unknown'))
                                nvme_percent = nvme_partition['percent']

                                if nvme_percent >= disk_early_warning_threshold:
                                    last_alert_time = last_partition_alert_times.get(nvme_key, 0)
                                    time_since_last_alert = current_time - last_alert_time

                                    if not partition_alert_sent.get(nvme_key, False) or time_since_last_alert >= alert_cooldown:
                                        if disk_webhook:
                                            logger.warning(f"[WARNING] NVMe {nvme_key}: {nvme_percent:.1f}% (early warning: {disk_early_warning_threshold:.1f}%)")
                                            success = disk_webhook.send_partition_alert(
                                                partition=nvme_partition,
                                                threshold=disk_early_warning_threshold,
                                                is_early_warning=True
                                            )
                                            if success:
                                                logger.info(f"    Discord NVMe early warning sent for {nvme_key}")
                                            else:
                                                logger.warning(f"    Discord NVMe early warning failed for {nvme_key}")

                                        last_partition_alert_times[nvme_key] = current_time
                                        partition_alert_sent[nvme_key] = True
                    except Exception as e:
                        logger.debug(f"Error checking individual partitions: {e}")

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
                                if top_cpu_processes is None:
                                    top_cpu_processes = process_monitor.get_top_processes(limit=5, sort_by='cpu')
                                top_cpu_procs = top_cpu_processes
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

                                logger.warning(f"[ALERT] Temperature: {cpu_temp:.1f}C (threshold: {temp_threshold:.1f}C) for {temp_high_duration:.0f}s")
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
                                logger.warning(f"Temperature: {cpu_temp:.1f}C (threshold: {temp_threshold:.1f}C) for {temp_high_duration:.0f}s")

                            last_temp_alert_time = current_time
                            temp_alert_sent = True
                        else:
                            remaining_cooldown = alert_cooldown - time_since_last_alert
                            logger.debug(f"Temperature alert cooldown: {remaining_cooldown:.0f}s")
                    else:
                        remaining_duration = temp_alert_duration - temp_high_duration
                        logger.debug(f"Temperature high ({cpu_temp:.1f}C) but needs to persist for {remaining_duration:.0f}s more before alert")
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
                        top_cpu_processes = process_monitor.get_top_processes(limit=MAX_PROCESSES_DISPLAY, sort_by='cpu')
                        top_memory_processes = process_monitor.get_top_processes(limit=MAX_PROCESSES_DISPLAY, sort_by='memory')

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
                    except (psutil.NoSuchProcess, psutil.AccessDenied, psutil.ZombieProcess, PermissionError, OSError) as e:
                        logger.debug(f"Error accessing process: {e}")
                    except Exception as e:
                        logger.debug(f"Unexpected error checking processes: {e}")

                if freeze_monitor_enabled and freeze_monitor:
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
                        except (ValueError, OSError) as e:
                            logger.debug(f"Error checking freeze status: {e}")
                        except Exception as e:
                            logger.warning(f"Unexpected error checking freeze status: {e}")

                media_check_counter += 1
                if media_monitor_enabled and media_check_counter >= media_check_interval and Path(media_directory).exists():
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

                                if disk_webhook:
                                    size_formatted = format_bytes(media_size_info['size'])
                                    logger.warning(f"[ALERT] Directory {media_directory}: {size_formatted} (threshold: {format_bytes(media_directory_threshold_bytes)})")
                                    success = disk_webhook.send_directory_size_alert(
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

                if qbittorrent_monitor:
                    qbittorrent_check_counter += 1
                    if qbittorrent_check_counter >= QBITTORRENT_CHECK_INTERVAL:
                        qbittorrent_check_counter = 0
                        if qbittorrent_monitor and qbittorrent_monitor.is_available():
                            try:
                                all_torrents = qbittorrent_monitor.get_all_torrents()
                                current_torrents = {torrent['hash']: torrent for torrent in all_torrents}

                                for torrent_hash, torrent in current_torrents.items():
                                    state = torrent.get('state', '').lower()
                                    progress = torrent.get('progress', 0.0)

                                    is_seeding = state in ['uploading', 'stalledup', 'queuedup']

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
                                            if qbittorrent_webhook:
                                                logger.info(f"[qBittorrent] Download complete: {torrent.get('name', 'Unknown')}")
                                                success = qbittorrent_webhook.send_download_complete_alert(torrent)
                                                if success:
                                                    logger.info("    Discord download complete notification sent")
                                                else:
                                                    logger.warning("    Discord download complete notification failed")
                                            tracked_torrents[torrent_hash]['notified_download'] = True

                                    if is_seeding and not prev_was_seeding:
                                        if not tracked_torrents[torrent_hash]['notified_seeding']:
                                            if qbittorrent_webhook:
                                                logger.info(f"[qBittorrent] Torrent seeding: {torrent.get('name', 'Unknown')}")
                                                success = qbittorrent_webhook.send_seeding_notification(torrent)
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

                                if len(tracked_torrents) > MAX_TRACKED_TORRENTS:
                                    logger.warning(f"Tracked torrents dict too large ({len(tracked_torrents)}), clearing old entries")
                                    oldest_hashes = sorted(tracked_torrents.keys())[:len(tracked_torrents) - MAX_TRACKED_TORRENTS]
                                    for old_hash in oldest_hashes:
                                        del tracked_torrents[old_hash]
                            except (ConnectionError, TimeoutError, ValueError) as e:
                                logger.debug(f"Error checking qBittorrent: {e}")
                            except Exception as e:
                                logger.warning(f"Unexpected error checking qBittorrent: {e}")
                log_check_counter += 1
                root_disk_usage = disk_monitor.get_disk_usage('/')
                disk_percent = root_disk_usage.get('percent', 0) if root_disk_usage else 0

                effective_log_interval = log_check_interval if disk_percent < 40.0 else 5

                if log_check_counter >= effective_log_interval:
                    log_check_counter = 0
                    try:
                        aggressive_log_cleanup = disk_percent >= 35.0

                        all_log_sizes = log_monitor.get_all_log_sizes()

                        if aggressive_log_cleanup:
                            all_log_files = log_monitor.get_all_log_files_in_directory('/var/log')
                            monitored_logs = []
                            if all_log_sizes.get('application_log'):
                                monitored_logs.append(all_log_sizes['application_log'])
                            monitored_logs.extend(all_log_sizes.get('system_logs', []))
                            if all_log_sizes.get('journal_log'):
                                monitored_logs.append(all_log_sizes['journal_log'])

                            all_logs_dict = {}
                            for log_info in all_log_files + monitored_logs:
                                log_path = log_info.get('path', '')
                                if log_path:
                                    if log_path not in all_logs_dict or log_info.get('size_bytes', 0) > all_logs_dict[log_path].get('size_bytes', 0):
                                        all_logs_dict[log_path] = log_info

                            # Convert to list for iteration (dict.values() is a view, list() allows multiple iterations)
                            logs_to_check = list(all_logs_dict.values())
                            logger.info(f"[LOG CHECK] Disk at {disk_percent:.1f}% - cleaning ALL logs proactively")
                        else:
                            logs_to_check = all_log_sizes.get('exceeded_logs', [])

                        cleaned_logs_batch = []
                        threshold_formatted = all_log_sizes.get('threshold_formatted', 'N/A')

                        for log_info in logs_to_check:
                            log_path = log_info.get('path', 'Unknown')

                            if log_path in cleaned_logs:
                                continue

                            current_log_info = log_monitor.get_log_size(log_path)
                            if not current_log_info:
                                continue

                            current_size_bytes = current_log_info.get('size_bytes', 0)
                            threshold_bytes = log_monitor.threshold_bytes

                            if not aggressive_log_cleanup and current_size_bytes <= threshold_bytes:
                                logger.debug(f"    Skipping {log_path}: {format_bytes(current_size_bytes)} <= {format_bytes(threshold_bytes)}")
                                continue

                            last_alert_time = last_log_alert_time.get(log_path, 0)
                            time_since_last_alert = current_time - last_alert_time

                            if time_since_last_alert >= alert_cooldown:
                                original_size = current_size_bytes
                                if aggressive_log_cleanup:
                                    logger.info(f"    Proactive cleanup (disk {disk_percent:.1f}%): {log_path} ({format_bytes(original_size)})")
                                else:
                                    logger.info(f"    Cleaning {log_path}: {format_bytes(original_size)} > {format_bytes(threshold_bytes)}")
                                cleanup_success = log_monitor.cleanup_log(current_log_info, force=aggressive_log_cleanup)

                                if cleanup_success:
                                    logger.info(f"    Log cleanup successful for {log_path} (was {format_bytes(original_size)})")
                                    cleaned_logs.add(log_path)

                                    size_after = log_monitor.get_log_size(log_path)
                                    if size_after:
                                        cleaned_log_info = size_after
                                        cleaned_log_info['original_size_bytes'] = original_size
                                        cleaned_log_info['original_size_formatted'] = format_bytes(original_size)
                                        logger.info(f"    Log size after cleanup: {cleaned_log_info.get('size_formatted', 'N/A')}")
                                        cleaned_logs_batch.append(cleaned_log_info)
                                    else:
                                        logger.warning(f"    Could not get log size after cleanup for {log_path}")
                                        current_log_info['original_size_bytes'] = original_size
                                        current_log_info['original_size_formatted'] = format_bytes(original_size)
                                        cleaned_logs_batch.append(current_log_info)
                                else:
                                    logger.error(f"    Log cleanup failed for {log_path} - check permissions (may need root/sudo)")

                                last_log_alert_time[log_path] = current_time

                        if cleaned_logs_batch and webhook:
                            logger.warning(f"[ALERT] {len(cleaned_logs_batch)} log file(s) exceeded threshold and were auto-cleaned")
                            success = webhook.send_batched_log_cleanup_alert(
                                cleaned_logs=cleaned_logs_batch,
                                threshold_formatted=threshold_formatted
                            )
                            if success:
                                logger.info(f"    Discord batched log cleanup alert sent for {len(cleaned_logs_batch)} log(s)")
                            else:
                                logger.warning(f"    Discord batched log cleanup alert failed")
                        elif cleaned_logs_batch:
                            logger.warning(f"{len(cleaned_logs_batch)} log file(s) exceeded threshold and were auto-cleaned (no webhook configured)")

                        current_exceeded_paths = {log.get('path') for log in exceeded_logs}
                        cleaned_logs = {path for path in cleaned_logs if path not in current_exceeded_paths}
                    except Exception as e:
                        logger.debug(f"Error checking log sizes: {e}")

                kernel_stability_check_counter += 1
                if kernel_monitor_enabled and kernel_stability_check_counter >= kernel_stability_check_interval:
                    kernel_stability_check_counter = 0
                    try:
                        stability = kernel_monitor.check_system_stability()
                        if stability.get('risk_level') in ['high', 'critical']:
                            time_since_last_alert = current_time - last_kernel_stability_alert_time

                            if not kernel_stability_alert_sent or time_since_last_alert >= alert_cooldown:
                                risk_level = stability.get('risk_level', 'unknown')
                                risk_score = stability.get('risk_score', 0)
                                indicators = stability.get('indicators', [])

                                logger.warning(f"[KERNEL] System stability risk: {risk_level.upper()} (score: {risk_score})")

                                if crash_webhook:
                                    fields = [
                                        {'name': 'Risk Level', 'value': risk_level.upper(), 'inline': True},
                                        {'name': 'Risk Score', 'value': str(risk_score), 'inline': True},
                                        {'name': 'Status', 'value': 'System showing signs of instability - crash risk elevated', 'inline': False},
                                    ]

                                    if indicators:
                                        fields.append({
                                            'name': 'Warning Indicators',
                                            'value': '\n'.join(indicators[:MAX_INDICATORS_DISPLAY]),
                                            'inline': False
                                        })

                                    recommendations = stability.get('recommendations', [])
                                    if recommendations:
                                        fields.append({
                                            'name': 'Recommended Actions',
                                            'value': '\n'.join(recommendations[:MAX_RECOMMENDATIONS_DISPLAY]),
                                            'inline': False
                                        })

                                    normalized_fields = sorted(
                                        [{'name': f.get('name', ''), 'value': f.get('value', ''), 'inline': f.get('inline', False)}
                                         for f in fields],
                                        key=lambda x: (x['name'], x['value'])
                                    )
                                    alert_content = {
                                        'title': f'Kernel Alert: System Stability Risk ({risk_level.upper()})',
                                        'description': 'System showing signs of instability that may lead to kernel crash',
                                        'fields': normalized_fields
                                    }
                                    alert_hash = hashlib.md5(json.dumps(alert_content, sort_keys=True).encode()).hexdigest()

                                    if alert_hash != last_kernel_stability_alert_hash:
                                        success = crash_webhook.send_embed(
                                            title=alert_content['title'],
                                            description=alert_content['description'],
                                            fields=fields,
                                            footer='Server Monitor - Kernel Stability',
                                            timestamp=True
                                        )
                                        if success:
                                            logger.info("    Discord kernel stability alert sent")
                                            last_kernel_stability_alert_hash = alert_hash
                                        else:
                                            logger.warning("    Discord kernel stability alert failed")
                                    else:
                                        logger.debug("    Skipping duplicate kernel stability alert (same content)")

                                last_kernel_stability_alert_time = current_time
                                kernel_stability_alert_sent = True
                        else:
                            if kernel_stability_alert_sent:
                                kernel_stability_alert_sent = False
                                last_kernel_stability_alert_hash = None
                    except Exception as e:
                        logger.debug(f"Error checking kernel stability: {e}")

                kernel_check_counter += 1
                if kernel_monitor_enabled and kernel_check_counter >= kernel_check_interval:
                    kernel_check_counter = 0
                    try:
                        crash_results = kernel_monitor.check_kernel_crashes(since_minutes=kernel_check_since_minutes)

                        if crash_results.get('unexpected_reboot'):
                            reboot_info = crash_results['unexpected_reboot']
                            time_since_last_alert = current_time - last_kernel_alert_time

                            if not kernel_alert_sent or time_since_last_alert >= alert_cooldown:
                                logger.critical(f"[KERNEL] Unexpected system reboot detected! Previous boot: {reboot_info.get('previous_boot_time')}, Current boot: {reboot_info.get('current_boot_time')}")

                                if crash_webhook:
                                    prev_boot = reboot_info.get('previous_boot_time')
                                    curr_boot = reboot_info.get('current_boot_time')
                                    fields = [
                                        {'name': 'Event Type', 'value': 'Unexpected System Reboot', 'inline': False},
                                        {'name': 'Previous Boot Time', 'value': datetime.fromtimestamp(prev_boot).strftime('%Y-%m-%d %H:%M:%S') if prev_boot else 'Unknown', 'inline': True},
                                        {'name': 'Current Boot Time', 'value': datetime.fromtimestamp(curr_boot).strftime('%Y-%m-%d %H:%M:%S') if curr_boot else 'Unknown', 'inline': True},
                                        {'name': 'Status', 'value': 'System may have crashed or was unexpectedly rebooted', 'inline': False},
                                    ]

                                    success = crash_webhook.send_embed(
                                        title='Kernel Alert: Unexpected System Reboot',
                                        description='System reboot detected - possible kernel crash or unexpected shutdown',
                                        fields=fields,
                                        footer='Server Monitor - Kernel',
                                        timestamp=True
                                    )
                                    if success:
                                        logger.info("    Discord kernel reboot alert sent")
                                    else:
                                        logger.warning("    Discord kernel reboot alert failed")

                                last_kernel_alert_time = current_time
                                kernel_alert_sent = True

                        early_warnings = crash_results.get('early_warnings', [])
                        hardware_errors = crash_results.get('hardware_errors', [])

                        critical_warnings = [w for w in early_warnings if w.get('severity') == 'critical']
                        if critical_warnings or hardware_errors:
                            time_since_last_alert = current_time - last_kernel_alert_time

                            if not kernel_alert_sent or time_since_last_alert >= alert_cooldown:
                                logger.warning(f"[KERNEL] Early warning signs detected: {len(critical_warnings)} critical warnings, {len(hardware_errors)} hardware errors")

                                if crash_webhook:
                                    fields = [
                                        {'name': 'Warning Type', 'value': 'Early Warning - System may be heading towards crash', 'inline': False},
                                        {'name': 'Critical Warnings', 'value': str(len(critical_warnings)), 'inline': True},
                                        {'name': 'Hardware Errors', 'value': str(len(hardware_errors)), 'inline': True},
                                    ]

                                    if hardware_errors:
                                        hw_details = []
                                        for hw_error in hardware_errors[:MAX_HARDWARE_ERRORS_DISPLAY]:
                                            if 'count_increase' in hw_error:
                                                source = hw_error.get('source', 'unknown')
                                                count_increase = hw_error['count_increase']
                                                total_count = hw_error.get('count', 0)
                                                error_type = hw_error.get('error_type', 'unknown')
                                                hw_details.append(f"{source}: {count_increase} new {error_type} errors (total: {total_count})")
                                            elif 'count' in hw_error:
                                                source = hw_error.get('source', 'unknown')
                                                error_type = hw_error.get('error_type', 'unknown')
                                                count = hw_error['count']
                                                hw_details.append(f"{source}: {count} {error_type} errors")
                                            elif 'message' in hw_error:
                                                source = hw_error.get('source', 'unknown')
                                                hw_details.append(f"{source}: {hw_error['message'][:100]}")

                                        if hw_details:
                                            fields.append({
                                                'name': 'Hardware Errors',
                                                'value': '\n'.join(hw_details),
                                                'inline': False
                                            })

                                    if critical_warnings:
                                        warning_details = []
                                        for warning in critical_warnings[:5]:
                                            keyword = warning.get('keyword', 'unknown')
                                            message = warning.get('message', '')[:150]
                                            warning_details.append(f"**{keyword}**: {message}")

                                        if warning_details:
                                            fields.append({
                                                'name': 'Critical Warnings',
                                                'value': '\n'.join(warning_details),
                                                'inline': False
                                            })

                                    fields.append({
                                        'name': 'Action Required',
                                        'value': 'System showing signs of instability. Monitor closely and investigate hardware/system issues.',
                                        'inline': False
                                    })

                                    success = crash_webhook.send_embed(
                                        title='Kernel Alert: Early Warning Signs Detected',
                                        description='System showing early warning signs that may indicate impending kernel crash',
                                        fields=fields,
                                        footer='Server Monitor - Kernel Warning',
                                        timestamp=True
                                    )
                                    if success:
                                        logger.info("    Discord kernel early warning alert sent")
                                    else:
                                        logger.warning("    Discord kernel early warning alert failed")

                                last_kernel_alert_time = current_time
                                kernel_alert_sent = True

                        crashes = crash_results.get('crashes_found', [])
                        if crashes:
                            time_since_last_alert = current_time - last_kernel_alert_time

                            if not kernel_alert_sent or time_since_last_alert >= alert_cooldown:
                                logger.critical(f"[KERNEL] {len(crashes)} kernel crash/panic detected!")

                                if crash_webhook:
                                    crash_summary = {}
                                    for crash in crashes:
                                        crash_type = crash.get('keyword', 'unknown')
                                        if crash_type not in crash_summary:
                                            crash_summary[crash_type] = []
                                        crash_summary[crash_type].append(crash)

                                    fields = [
                                        {'name': 'Total Crashes', 'value': str(len(crashes)), 'inline': True},
                                        {'name': 'Check Period', 'value': f'{kernel_check_since_minutes} minutes', 'inline': True},
                                    ]

                                    crash_details = []
                                    for i, (crash_type, crash_list) in enumerate(list(crash_summary.items())[:MAX_CRASH_TYPES_DISPLAY]):
                                        latest_crash = crash_list[0]
                                        message = latest_crash.get('message', '')[:200]
                                        source = latest_crash.get('source', 'unknown')
                                        crash_details.append(f"**{crash_type}** ({len(crash_list)} occurrence(s)) - Source: {source}\n`{message}`")

                                    if crash_details:
                                        fields.append({
                                            'name': 'Crash Details',
                                            'value': '\n\n'.join(crash_details),
                                            'inline': False
                                        })

                                    if len(crash_summary) > 5:
                                        fields.append({
                                            'name': 'Note',
                                            'value': f'Showing first 5 of {len(crash_summary)} crash types. Check logs for full details.',
                                            'inline': False
                                        })

                                    success = crash_webhook.send_embed(
                                        title='Kernel Alert: Crash/Panic Detected',
                                        description=f'Kernel crashes, panics, or oops detected in system logs',
                                        fields=fields,
                                        footer='Server Monitor - Kernel',
                                        timestamp=True
                                    )
                                    if success:
                                        logger.info("    Discord kernel crash alert sent")
                                    else:
                                        logger.warning("    Discord kernel crash alert failed")

                                last_kernel_alert_time = current_time
                                kernel_alert_sent = True
                        else:
                            if kernel_alert_sent:
                                kernel_alert_sent = False
                    except Exception as e:
                        logger.debug(f"Error checking kernel crashes: {e}")

            except Exception as e:


                logger.error(f"Unhandled error in monitoring loop iteration: {e}")
                logger.error(f"Traceback: {traceback.format_exc()}")
                log_error_to_file_and_webhook(e, context="Monitoring loop iteration", include_traceback=True)
                logger.warning("Continuing monitoring despite error...")

                if watchdog_monitor:
                    watchdog_monitor.update_heartbeat()

            time.sleep(check_interval)

    except KeyboardInterrupt:
        shutdown_start = time.time()

        # Check if this was a graceful shutdown (signal) or user interrupt
        with shutdown_state['lock']:
            is_graceful = shutdown_state['graceful_shutdown']
            shutdown_reason = shutdown_state.get('shutdown_reason', 'keyboard_interrupt')

        if is_graceful:
            logger.info(f"Monitoring stopped gracefully ({shutdown_reason})")
        else:
            logger.info("Monitoring stopped by user (Ctrl+C)")

        with shutdown_state['lock']:
            shutdown_state['shutdown_time'] = shutdown_start

        if watchdog_monitor:
            watchdog_monitor.stop()

        uptime_seconds = shutdown_start - monitoring_start_time

        logger.info("Shutting down...")

        try:
            if freeze_monitor_enabled and freeze_monitor:
                try:
                    freeze_monitor.stop_watchdog()
                except Exception as e:
                    logger.debug(f"Error stopping freeze monitor: {e}")
        except KeyboardInterrupt:
            logger.warning("Shutdown interrupted, exiting immediately")
            raise

        try:
            stop_server(timeout=3.0)
        except KeyboardInterrupt:
            logger.warning("Server shutdown interrupted, exiting immediately")
            raise
        except Exception as e:
            logger.warning(f"Error stopping web server: {e}")

        shutdown_seconds = time.time() - shutdown_start
        logger.info(f"Shutdown complete ({shutdown_seconds:.2f}s)")

        # Only send shutdown notification for graceful shutdowns (not crashes)
        if webhook and is_graceful:
            try:
                logger.info("Sending shutdown notification...")
                success = webhook.send_shutdown_notification(uptime_seconds=uptime_seconds, shutdown_seconds=shutdown_seconds)
                if success:
                    logger.info("Shutdown notification sent")
                else:
                    logger.warning("Shutdown notification failed")
            except (KeyboardInterrupt, Exception) as e:
                logger.warning(f"Shutdown notification skipped: {e}")
        elif not is_graceful:
            logger.debug("Skipping shutdown notification (not a graceful shutdown)")
    except Exception as e:
        # This is a crash - unhandled exception
        with shutdown_state['lock']:
            shutdown_state['graceful_shutdown'] = False
            shutdown_state['shutdown_reason'] = 'crash_exception'
            shutdown_state['shutdown_time'] = time.time()

        logger.error(f"Unexpected error in monitoring loop: {e}")
        logger.error(f"Traceback: {traceback.format_exc()}")
        logger.critical("CRITICAL: Monitoring loop crashed (unhandled exception).")

        # Send crash alert (not shutdown notification)
        crash_webhook_to_use = crash_webhook if crash_webhook else webhook
        if crash_webhook_to_use:
            try:
                traceback_text = traceback.format_exc()
                if len(traceback_text) > 1500:
                    traceback_text = traceback_text[:1500] + "... (truncated)"

                crash_webhook_to_use.send_embed(
                    title='CRITICAL: Server Monitor Crashed',
                    description='The monitoring loop crashed due to an unhandled exception',
                    fields=[
                        {'name': 'Error Type', 'value': type(e).__name__, 'inline': True},
                        {'name': 'Error Message', 'value': str(e)[:500], 'inline': False},
                        {'name': 'Uptime Before Crash', 'value': f"{(time.time() - monitoring_start_time):.1f} seconds", 'inline': True},
                        {'name': 'Traceback', 'value': f"```\n{traceback_text}\n```", 'inline': False},
                        {'name': 'Status', 'value': 'CRITICAL - Program crashed unexpectedly', 'inline': False},
                    ],
                    color=0xFF0000,  # Red for crashes
                    footer='Server Monitor - Crash Alert',
                    timestamp=True
                )
                logger.info("Crash alert sent to Discord")
            except Exception as webhook_error:
                logger.warning(f"Failed to send crash alert: {webhook_error}")

        log_error_to_file_and_webhook(e, context="CRITICAL: Monitoring loop crash", include_traceback=True)

        if watchdog_monitor:
            watchdog_monitor.stop()

        logger.info("Shutting down due to critical error...")
        if freeze_monitor_enabled and freeze_monitor:
            try:
                freeze_monitor.stop_watchdog()
            except Exception:
                pass
        try:
            stop_server()
        except Exception:
            pass
    finally:
        # Final check: if we reach finally without graceful shutdown flag, it might be a crash
        with shutdown_state['lock']:
            final_reason = shutdown_state.get('shutdown_reason', 'unknown')
            is_graceful_final = shutdown_state.get('graceful_shutdown', False)

        clear_cache_on_exit(verbose=True)

        if is_graceful_final:
            logger.info(f"Server Monitor stopped gracefully ({final_reason})")
        else:
            logger.warning(f"Server Monitor stopped unexpectedly ({final_reason}) - may have crashed")


if __name__ == "__main__":
    main()

