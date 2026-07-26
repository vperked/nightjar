"""
System detection and adaptation logic.

Detects system capabilities and adapts configuration automatically.
"""

import os
import sys
import platform
import subprocess
import socket
import psutil
from typing import Dict, Optional, Any
from pathlib import Path
from core.observability import Component, get_logger
from core.format import BYTES_PER_GB
from core.config import (
    DEFAULT_CPU_THRESHOLD, DEFAULT_MEMORY_THRESHOLD, DEFAULT_DISK_THRESHOLD,
    DEFAULT_GPU_THRESHOLD
)

logger = get_logger(Component.SYSTEM_INFO)


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
        qb_host = os.getenv('QBITTORRENT_HOST', 'localhost')
        qb_port = int(os.getenv('QBITTORRENT_PORT', '8080'))

        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
                sock.settimeout(2)
                result = sock.connect_ex((qb_host, qb_port))
        except Exception as e:
            logger.debug(f"Socket error checking qBittorrent: {e}")
            result = 1

        if result == 0:
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

        typical_cpu = usage.get('typical_cpu', 0)
        system_type = usage.get('system_type', 'unknown')

        if typical_cpu > 0:
            if typical_cpu > 50:
                threshold = min(95.0, base_threshold + 10.0)
                reasoning.append(f"high typical usage ({typical_cpu:.1f}%)")
            elif typical_cpu > 30:
                threshold = min(90.0, base_threshold + 5.0)
                reasoning.append(f"moderate typical usage ({typical_cpu:.1f}%)")
            elif typical_cpu < 10:
                threshold = max(70.0, base_threshold - 5.0)
                reasoning.append(f"low typical usage ({typical_cpu:.1f}%)")
            else:
                threshold = base_threshold
                reasoning.append(f"typical usage ({typical_cpu:.1f}%)")

            if system_type == 'server':
                threshold = min(90.0, threshold + 5.0)
                reasoning.append("server workload")
            elif system_type == 'desktop':
                threshold = max(70.0, threshold - 5.0)
                reasoning.append("desktop workload")
        else:
            threshold = base_threshold

        threshold = max(65.0, min(95.0, threshold))

        os.environ['CPU_THRESHOLD'] = str(threshold)
        auto_detected_values['CPU_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"CPU_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    if not os.getenv('MEMORY_THRESHOLD'):
        threshold = None
        reasoning = []

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
                base_threshold = 82.0
                reasoning.append(f"{detected['total_memory_gb']:.1f} GB (high memory)")
        else:
            base_threshold = DEFAULT_MEMORY_THRESHOLD

        typical_memory = usage.get('typical_memory', 0)

        if typical_memory > 0:
            if typical_memory > 60:
                threshold = min(90.0, base_threshold + 8.0)
                reasoning.append(f"high typical usage ({typical_memory:.1f}%)")
            elif typical_memory > 40:
                threshold = min(87.0, base_threshold + 5.0)
                reasoning.append(f"moderate typical usage ({typical_memory:.1f}%)")
            elif typical_memory < 20:
                threshold = max(70.0, base_threshold - 5.0)
                reasoning.append(f"low typical usage ({typical_memory:.1f}%)")
            else:
                threshold = base_threshold
                reasoning.append(f"typical usage ({typical_memory:.1f}%)")
        else:
            threshold = base_threshold

        threshold = max(65.0, min(92.0, threshold))

        os.environ['MEMORY_THRESHOLD'] = str(threshold)
        auto_detected_values['MEMORY_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"MEMORY_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    if not os.getenv('DISK_THRESHOLD'):
        threshold = None
        reasoning = []

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
                base_threshold = 88.0
                reasoning.append(f"{detected['total_disk_gb']:.1f} GB (large disk)")
        else:
            base_threshold = DEFAULT_DISK_THRESHOLD

        typical_disk = usage.get('typical_disk', 0)

        if typical_disk > 0:
            if typical_disk > 70:
                threshold = max(85.0, base_threshold - 3.0)
                reasoning.append(f"current usage {typical_disk:.1f}% (already high)")
            elif typical_disk < 30:
                threshold = min(90.0, base_threshold + 3.0)
                reasoning.append(f"current usage {typical_disk:.1f}% (plenty of space)")
            else:
                threshold = base_threshold
                reasoning.append(f"current usage {typical_disk:.1f}%")
        else:
            threshold = base_threshold

        threshold = max(80.0, min(92.0, threshold))

        os.environ['DISK_THRESHOLD'] = str(threshold)
        auto_detected_values['DISK_THRESHOLD'] = str(threshold)
        reasoning_str = ", ".join(reasoning)
        adaptations_applied.append(f"DISK_THRESHOLD set to {threshold:.1f}% (based on: {reasoning_str})")

    if detected['gpu_available'] and not os.getenv('GPU_THRESHOLD'):
        os.environ['GPU_THRESHOLD'] = str(DEFAULT_GPU_THRESHOLD)
        auto_detected_values['GPU_THRESHOLD'] = str(DEFAULT_GPU_THRESHOLD)
        adaptations_applied.append("GPU monitoring enabled (NVIDIA GPU detected)")

    if detected['qbittorrent_available'] and not os.getenv('QBITTORRENT_MONITOR'):
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
        project_root = script_path.parent.parent.parent

    env_file = project_root / '.env'

    existing_lines = []
    existing_keys = set()
    has_auto_detected_section = False

    if env_file.exists():
        try:
            with open(env_file, 'r', encoding='utf-8') as f:
                for line in f:
                    existing_lines.append(line)
                    stripped = line.strip()
                    if stripped and not stripped.startswith('#'):
                        if '=' in stripped:
                            key = stripped.split('=')[0].strip()
                            existing_keys.add(key)
                    if '# Auto-detected' in line or '# Auto-detected configuration' in line:
                        has_auto_detected_section = True
        except Exception as e:
            logger.warning(f"Error reading existing .env file: {e}")

    new_lines = []
    added_keys = set()

    for line in existing_lines:
        new_lines.append(line)

    if not has_auto_detected_section:
        new_lines.append('\n')
        new_lines.append('# Auto-detected configuration values\n')

    for key, value in auto_detected_values.items():
        if key not in existing_keys and key not in added_keys:
            new_lines.append(f"{key}={value}  # Auto-detected\n")
            added_keys.add(key)

    if added_keys:
        try:
            with open(env_file, 'w', encoding='utf-8') as f:
                f.writelines(new_lines)
            logger.info(f"Saved {len(added_keys)} auto-detected configuration value(s) to {env_file}")
            return True
        except Exception as e:
            logger.error(f"Error saving auto-detected configuration to .env file: {e}")
            return False

    return False
