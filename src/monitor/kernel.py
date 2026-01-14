"""
Kernel crash and panic monitoring.
Monitors kernel logs, dmesg, and systemd journal for kernel crashes, panics, and oops.
"""

import json
import os
import platform
import subprocess
import time
from datetime import datetime, timedelta
from typing import Dict, List, Optional, Tuple
from logger import get_logger
from util.security import safe_subprocess_path

logger = get_logger()

# Default path for persistence file
DEFAULT_PERSISTENCE_FILE = os.path.join(os.path.expanduser('~'), '.servermonitor_kernel_tracking.json')


class KernelMonitor:
    """Monitor kernel for crashes, panics, and oops."""
    
    def __init__(self, persistence_file: Optional[str] = None, ignored_devices: Optional[List[str]] = None):
        """
        Initialize the kernel monitor.
        
        Args:
            persistence_file: Path to file for persisting tracking data (default: ~/.servermonitor_kernel_tracking.json)
            ignored_devices: List of device names to ignore for I/O error alerts (default: from KERNEL_IGNORE_DEVICES env)
        """
        self.is_linux = platform.system() == 'Linux'
        self.last_boot_time = None
        self.last_check_time = time.time()
        self.detected_crashes = set()
        self.last_reported_errors = {}  # Track last reported error counts per device
        self.last_alert_time = {}  # Track last alert time per device (to prevent spam)
        self.persistence_file = persistence_file or DEFAULT_PERSISTENCE_FILE
        
        # Load ignored devices from environment or parameter
        if ignored_devices is None:
            ignore_env = os.getenv('KERNEL_IGNORE_DEVICES', '')
            if ignore_env:
                self.ignored_devices = [d.strip() for d in ignore_env.split(',') if d.strip()]
            else:
                self.ignored_devices = []
        else:
            self.ignored_devices = ignored_devices
        
        if self.ignored_devices:
            logger.info(f"Kernel monitor: Ignoring I/O errors on devices: {', '.join(self.ignored_devices)}")
        
        if self.is_linux:
            try:
                self.last_boot_time = self._get_boot_time()
                # Load persisted tracking data
                self._load_tracking_data()
            except Exception as e:
                logger.debug(f"Could not get initial boot time: {e}")
    
    def _get_boot_time(self) -> Optional[float]:
        """
        Get system boot time.
        
        Returns:
            Boot time as Unix timestamp, or None if unavailable
        """
        try:
            import psutil
            return psutil.boot_time()
        except Exception:
            try:
                with open('/proc/stat', 'r') as f:
                    for line in f:
                        if line.startswith('btime'):
                            parts = line.split()
                            if len(parts) >= 2:
                                return float(parts[1])
            except Exception:
                pass
            return None
    
    def _load_tracking_data(self):
        """Load persisted tracking data from file."""
        if not os.path.exists(self.persistence_file):
            logger.debug(f"Persistence file not found: {self.persistence_file}, starting fresh")
            return
        
        try:
            with open(self.persistence_file, 'r') as f:
                data = json.load(f)
                
            saved_boot_time = data.get('boot_time')
            if saved_boot_time and self.last_boot_time:
                if abs(saved_boot_time - self.last_boot_time) >= 10.0:
                    logger.debug("System rebooted since last run, resetting tracking data")
                    self.last_reported_errors = {}
                    self.last_alert_time = {}
                    self._save_tracking_data()
                    return
            
            self.last_reported_errors = data.get('last_reported_errors', {})
            self.last_alert_time = data.get('last_alert_time', {})
            
            self.last_alert_time = {k: float(v) for k, v in self.last_alert_time.items()}
            
            logger.debug(f"Loaded tracking data for {len(self.last_reported_errors)} devices")
        except (json.JSONDecodeError, IOError, ValueError) as e:
            logger.warning(f"Error loading tracking data from {self.persistence_file}: {e}, starting fresh")
            self.last_reported_errors = {}
            self.last_alert_time = {}
    
    def _save_tracking_data(self):
        """Save tracking data to file for persistence across restarts."""
        try:
            data = {
                'boot_time': self.last_boot_time,
                'last_reported_errors': self.last_reported_errors,
                'last_alert_time': self.last_alert_time,
                'saved_at': time.time()
            }
            
            # Create directory if it doesn't exist
            persistence_dir = os.path.dirname(self.persistence_file)
            if persistence_dir and not os.path.exists(persistence_dir):
                os.makedirs(persistence_dir, mode=0o700, exist_ok=True)
            
            # Write to temporary file first, then rename (atomic operation)
            temp_file = self.persistence_file + '.tmp'
            with open(temp_file, 'w') as f:
                json.dump(data, f, indent=2)
            
            # Atomic rename
            os.replace(temp_file, self.persistence_file)
            logger.debug(f"Saved tracking data to {self.persistence_file}")
        except (IOError, OSError) as e:
            logger.warning(f"Error saving tracking data to {self.persistence_file}: {e}")
    
    def _check_early_warning_signs(self, since_minutes: int = 60) -> List[Dict]:
        """
        Check for early warning signs that may indicate an impending kernel crash.
        
        Args:
            since_minutes: How many minutes back to check (default: 60)
            
        Returns:
            List of early warning indicators found
        """
        if not self.is_linux:
            return []
        
        warnings = []
        try:
            warning_keywords = [
                'mce:', 'machine check', 'edac error', 'corrected error', 'uncorrected error',
                'memory error', 'ecc error', 'hardware error', 'mcelog',
                'oom killer', 'out of memory', 'memory allocation failure', 'slab allocation',
                'page allocation failure', 'swap', 'memory pressure',
                'i/o error', 'disk error', 'sector', 'bad block', 'smart error',
                'filesystem error', 'ext4 error', 'xfs error', 'btrfs error',
                'driver error', 'device error', 'hardware failure', 'pci error',
                'usb error', 'sata error', 'nvme error', 'ahci error',
                'soft lockup', 'hard lockup', 'watchdog', 'hung task', 'stuck',
                'rcu stall', 'rcu self-detected stall', 'rcu detected stall',
                'thermal', 'overheating', 'temperature', 'power', 'voltage',
                'throttling', 'thermal shutdown',
                'filesystem corruption', 'superblock', 'journal', 'metadata',
                'network error', 'ethernet error', 'link down', 'phy error',
                'call trace', 'stack trace', 'backtrace', 'warn:', 'error:',
                'failed', 'failure', 'timeout', 'deadlock'
            ]
            
            since_time = datetime.now() - timedelta(minutes=since_minutes)
            since_str = since_time.strftime('%Y-%m-%d %H:%M:%S')
            
            for keyword in warning_keywords:
                try:
                    cmd = [
                        'journalctl',
                        '--since', since_str,
                        '--priority', '0..4',  # emerg, alert, crit, err, warning
                        '--grep', keyword,
                        '--no-pager',
                        '--format', 'json'
                    ]
                    
                    result = subprocess.run(
                        cmd,
                        capture_output=True,
                        text=True,
                        timeout=10,
                        check=False
                    )
                    
                    if result.returncode == 0 and result.stdout.strip():
                        lines = result.stdout.strip().split('\n')
                        for line in lines:
                            if line.strip():
                                try:
                                    entry = json.loads(line)
                                    warning_id = f"warning_{entry.get('__REALTIME_TIMESTAMP', '')}_{keyword}"
                                    if warning_id not in self.detected_crashes:
                                        priority = entry.get('PRIORITY', '6')
                                        severity = 'critical' if priority in ['0', '1', '2', '3'] else 'warning'
                                        
                                        warnings.append({
                                            'type': 'early_warning',
                                            'severity': severity,
                                            'keyword': keyword,
                                            'message': entry.get('MESSAGE', ''),
                                            'timestamp': entry.get('__REALTIME_TIMESTAMP', ''),
                                            'priority': priority,
                                            'source': 'journalctl'
                                        })
                                        self.detected_crashes.add(warning_id)
                                except (json.JSONDecodeError, KeyError):
                                    continue
                except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                    logger.debug(f"journalctl warning check failed for {keyword}: {e}")
                    continue
        except Exception as e:
            logger.debug(f"Error checking early warning signs: {e}")
        
        return warnings
    
    def _check_hardware_errors(self) -> List[Dict]:
        """
        Check for hardware errors that could lead to crashes.
        
        Returns:
            List of hardware errors found
        """
        if not self.is_linux:
            return []
        
        errors = []
        
        try:
            edac_paths = [
                '/sys/devices/system/edac/mc',
                '/sys/devices/system/edac'
            ]
            
            for edac_path in edac_paths:
                if os.path.exists(edac_path):
                    for root, dirs, files in os.walk(edac_path):
                        for file in files:
                            if 'ce_count' in file or 'ue_count' in file:
                                filepath = os.path.join(root, file)
                                try:
                                    with open(filepath, 'r') as f:
                                        count = int(f.read().strip())
                                        if count > 0:
                                            error_type = 'correctable' if 'ce_count' in file else 'uncorrectable'
                                            errors.append({
                                                'type': 'hardware_error',
                                                'error_type': error_type,
                                                'source': 'EDAC',
                                                'count': count,
                                                'location': filepath
                                            })
                                except (ValueError, OSError, PermissionError):
                                    continue
        except Exception as e:
            logger.debug(f"Error checking EDAC: {e}")
        
        try:
            with open('/proc/meminfo', 'r') as f:
                meminfo = f.read()
                if 'MemAvailable' in meminfo:
                    for line in meminfo.split('\n'):
                        if 'MemAvailable' in line:
                            try:
                                parts = line.split()
                                if len(parts) >= 2:
                                    available_kb = int(parts[1])
                                else:
                                    continue
                                if available_kb < 100 * 1024:
                                    errors.append({
                                        'type': 'memory_pressure',
                                        'severity': 'critical',
                                        'available_mb': available_kb / 1024,
                                        'message': 'Very low memory available - system may become unstable'
                                    })
                            except (ValueError, IndexError):
                                pass
        except Exception as e:
            logger.debug(f"Error checking /proc/meminfo: {e}")
        
        try:
            with open('/var/log/kern.log', 'r', encoding='utf-8', errors='ignore') as f:
                lines = f.readlines()
                for line in reversed(lines[-500:]):
                    if 'oom' in line.lower() and 'killer' in line.lower():
                        error_id = f"oom_{line}"
                        if error_id not in self.detected_crashes:
                            errors.append({
                                'type': 'oom_killer',
                                'severity': 'critical',
                                'message': line.strip(),
                                'source': '/var/log/kern.log'
                            })
                            self.detected_crashes.add(error_id)
                            break
        except (OSError, PermissionError):
            pass
        except Exception as e:
            logger.debug(f"Error checking for OOM killer: {e}")
        
        try:
            with open('/proc/interrupts', 'r') as f:
                interrupts = f.read()
                lines = interrupts.split('\n')
                if len(lines) > 10:
                    for line in lines[1:10]:
                        parts = line.split()
                        if len(parts) > 1:
                            try:
                                total = sum(int(x) for x in parts[1:] if x.isdigit())
                                if total > 1000000:
                                    device = parts[0] if parts else 'unknown'
                                    errors.append({
                                        'type': 'high_interrupts',
                                        'severity': 'warning',
                                        'device': device,
                                        'count': total,
                                        'message': f'High interrupt count on {device} - possible hardware issue'
                                    })
                                    break
                            except (ValueError, IndexError):
                                continue
        except (OSError, PermissionError):
            pass
        except Exception as e:
            logger.debug(f"Error checking /proc/interrupts: {e}")
        
        return errors
    
    def check_system_stability(self) -> Dict:
        """
        Check overall system stability indicators that may predict crashes.
        Combines multiple signals to assess crash risk.
        
        Returns:
            Dictionary containing stability assessment
        """
        stability = {
            'risk_level': 'low',
            'indicators': [],
            'recommendations': [],
            'timestamp': time.time()
        }
        
        if not self.is_linux:
            stability['error'] = 'Stability monitoring only available on Linux'
            return stability
        
        try:
            current_boot_time = self._get_boot_time()
            if current_boot_time and self.last_boot_time and abs(current_boot_time - self.last_boot_time) >= 10.0:
                self.last_reported_errors = {}
                self.last_alert_time = {}
                self.last_boot_time = current_boot_time
                self._save_tracking_data()
        except Exception:
            pass
        
        risk_score = 0
        indicators = []
        
        try:
            with open('/proc/meminfo', 'r') as f:
                meminfo = f.read()
                for line in meminfo.split('\n'):
                    if 'MemAvailable' in line:
                        try:
                            parts = line.split()
                            if len(parts) < 2:
                                continue
                            available_kb = int(parts[1])
                            available_mb = available_kb / 1024
                            
                            if available_mb < 50:
                                risk_score += 30
                                indicators.append('Critical: Less than 50MB memory available')
                                stability['recommendations'].append('Free memory immediately - system at risk of OOM kill')
                            elif available_mb < 200:
                                risk_score += 15
                                indicators.append('Warning: Less than 200MB memory available')
                        except (ValueError, IndexError):
                            pass
        except Exception:
            pass
        
        try:
            with open('/proc/vmstat', 'r') as f:
                vmstat = f.read()
                for line in vmstat.split('\n'):
                    if 'oom_kill' in line:
                        try:
                            parts = line.split()
                            if len(parts) < 2:
                                continue
                            oom_kills = int(parts[1])
                            if oom_kills > 0:
                                risk_score += 25
                                indicators.append(f'Critical: OOM killer has killed {oom_kills} process(es)')
                                stability['recommendations'].append('OOM killer active - system memory critically low')
                        except (ValueError, IndexError):
                            pass
        except Exception:
            pass
        
        try:
            with open('/proc/stat', 'r') as f:
                stat = f.read()
                if 'softlockup' in stat.lower() or 'hardlockup' in stat.lower():
                    risk_score += 20
                    indicators.append('Warning: Lockup detected in system stats')
        except Exception:
            pass
        
        try:
            with open('/proc/diskstats', 'r') as f:
                diskstats = f.read()
                for line in diskstats.split('\n'):
                    parts = line.split()
                    if len(parts) >= 14:
                        try:
                            read_errors = int(parts[12])
                            write_errors = int(parts[13])
                            
                            if read_errors > 0 or write_errors > 0:
                                device = parts[2]
                                
                                is_virtual = (device.startswith('loop') or 
                                             device.startswith('ram') or 
                                             device.startswith('zram') or
                                             device.startswith('dm-'))
                                is_partition = False
                                if 'p' in device:
                                    p_index = device.rfind('p')
                                    if p_index > 0 and p_index < len(device) - 1 and device[p_index+1:].isdigit():
                                        is_partition = True
                                elif len(device) >= 4:
                                    device_lower = device.lower()
                                    if (device_lower.startswith(('sd', 'hd', 'vd')) and 
                                        device[-1].isdigit() and 
                                        any(c.isalpha() for c in device[2:-1])):
                                        is_partition = True
                                
                                if is_virtual or is_partition:
                                    continue
                                
                                # Check if device is in ignore list
                                if device in self.ignored_devices:
                                    continue
                                
                                total_errors = read_errors + write_errors
                                should_alert = False
                                current_time = time.time()
                                device_cooldown = 24 * 60 * 60 
                                
                                if device not in self.last_reported_errors:
                                    should_alert = True
                                    self.last_reported_errors[device] = {'read': read_errors, 'write': write_errors, 'total': total_errors}
                                    self.last_alert_time[device] = current_time
                                else:
                                    last_errors = self.last_reported_errors[device]
                                    last_total = last_errors['total']
                                    total_increase = total_errors - last_total
                                    
                                    if total_increase < 0:
                                        should_alert = True
                                        self.last_reported_errors[device] = {'read': read_errors, 'write': write_errors, 'total': total_errors}
                                        self.last_alert_time[device] = current_time
                                    elif total_increase > 0:
                                        dramatic_increase_threshold = int(last_total * 0.50)
                                        if total_increase >= dramatic_increase_threshold:
                                            should_alert = True
                                            self.last_reported_errors[device] = {'read': read_errors, 'write': write_errors, 'total': total_errors}
                                            self.last_alert_time[device] = current_time
                                        else:
                                            absolute_threshold = 100
                                            relative_threshold = max(1, int(last_total * 0.10))
                                            error_threshold = max(absolute_threshold, relative_threshold)
                                            
                                            time_since_last_alert = current_time - self.last_alert_time.get(device, 0)
                                            
                                            if total_increase >= error_threshold and time_since_last_alert >= device_cooldown:
                                                should_alert = True
                                                self.last_reported_errors[device] = {'read': read_errors, 'write': write_errors, 'total': total_errors}
                                                self.last_alert_time[device] = current_time
                                            else:
                                                self.last_reported_errors[device] = {'read': read_errors, 'write': write_errors, 'total': total_errors}
                                
                                if should_alert:
                                    risk_score += 15
                                    indicators.append(f'Warning: I/O errors on {device} (R:{read_errors}, W:{write_errors})')
                                    stability['recommendations'].append(f'Check disk health on {device}')
                        except (ValueError, IndexError):
                            continue
        except Exception:
            pass

        try:
            self._save_tracking_data()
        except Exception as e:
            logger.debug(f"Error saving tracking data: {e}")
        
        if risk_score >= 50:
            stability['risk_level'] = 'critical'
        elif risk_score >= 30:
            stability['risk_level'] = 'high'
        elif risk_score >= 15:
            stability['risk_level'] = 'medium'
        else:
            stability['risk_level'] = 'low'
        
        stability['risk_score'] = risk_score
        stability['indicators'] = indicators
        
        return stability
    
    def _check_journalctl(self, since_minutes: int = 60) -> List[Dict]:
        """
        Check systemd journal for kernel crashes/panics.
        
        Args:
            since_minutes: How many minutes back to check (default: 60)
            
        Returns:
            List of crash/panic entries found
        """
        if not self.is_linux:
            return []
        
        crashes = []
        try:
            keywords = ['kernel panic', 'kernel oops', 'kernel crash', 'BUG:', 'WARNING:', 
                       'general protection fault', 'segmentation fault', 'double fault',
                       'unhandled page fault', 'kernel BUG', 'Unable to handle']
            
            since_time = datetime.now() - timedelta(minutes=since_minutes)
            since_str = since_time.strftime('%Y-%m-%d %H:%M:%S')
            
            for keyword in keywords:
                try:
                    safe_keyword = safe_subprocess_path(keyword)
                    if not safe_keyword:
                        continue
                    
                    cmd = [
                        'journalctl',
                        '--since', since_str,
                        '--priority', 'err',
                        '--grep', keyword,
                        '--no-pager',
                        '--format', 'json'
                    ]
                    
                    result = subprocess.run(
                        cmd,
                        capture_output=True,
                        text=True,
                        timeout=10,
                        check=False
                    )
                    
                    if result.returncode == 0 and result.stdout.strip():
                        lines = result.stdout.strip().split('\n')
                        for line in lines:
                            if line.strip():
                                try:
                                    entry = json.loads(line)
                                    crash_id = f"{entry.get('__REALTIME_TIMESTAMP', '')}_{keyword}"
                                    if crash_id not in self.detected_crashes:
                                        crashes.append({
                                            'type': 'kernel_error',
                                            'keyword': keyword,
                                            'message': entry.get('MESSAGE', ''),
                                            'timestamp': entry.get('__REALTIME_TIMESTAMP', ''),
                                            'source': 'journalctl'
                                        })
                                        self.detected_crashes.add(crash_id)
                                except (json.JSONDecodeError, KeyError):
                                    continue
                except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
                    logger.debug(f"journalctl check failed for {keyword}: {e}")
                    continue
        except Exception as e:
            logger.debug(f"Error checking journalctl: {e}")
        
        return crashes
    
    def _check_dmesg(self, since_minutes: int = 60) -> List[Dict]:
        """
        Check dmesg for kernel errors and warnings.
        
        Args:
            since_minutes: How many minutes back to check (default: 60)
            
        Returns:
            List of kernel errors found
        """
        if not self.is_linux:
            return []
        
        crashes = []
        try:
            cmd = ['dmesg', '-T', '--level=err,warn']
            
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=10,
                check=False
            )
            
            if result.returncode == 0 and result.stdout.strip():
                current_time = time.time()
                cutoff_time = current_time - (since_minutes * 60)
                
                lines = result.stdout.strip().split('\n')
                for line in lines:
                    if not line.strip():
                        continue
                    
                    try:
                        if ']' in line:
                            timestamp_str = line.split(']')[0].strip('[')
                            try:
                                dt = datetime.strptime(timestamp_str, '%a %b %d %H:%M:%S %Y')
                                line_time = dt.timestamp()
                                
                                if line_time < cutoff_time:
                                    continue
                            except ValueError:
                                pass
                        
                        line_lower = line.lower()
                        critical_keywords = ['panic', 'oops', 'bug:', 'crash', 'fault', 
                                           'segmentation', 'general protection', 'double fault']
                        
                        for keyword in critical_keywords:
                            if keyword in line_lower:
                                crash_id = f"dmesg_{line}"
                                if crash_id not in self.detected_crashes:
                                    crashes.append({
                                        'type': 'kernel_error',
                                        'keyword': keyword,
                                        'message': line,
                                        'timestamp': time.time(),
                                        'source': 'dmesg'
                                    })
                                    self.detected_crashes.add(crash_id)
                                    break
                    except Exception as e:
                        logger.debug(f"Error parsing dmesg line: {e}")
                        continue
        except (FileNotFoundError, subprocess.TimeoutExpired, subprocess.SubprocessError) as e:
            logger.debug(f"dmesg check failed: {e}")
        except Exception as e:
            logger.debug(f"Error checking dmesg: {e}")
        
        return crashes
    
    def _check_kernel_logs(self, since_minutes: int = 60) -> List[Dict]:
        """
        Check kernel log files directly.
        
        Args:
            since_minutes: How many minutes back to check (default: 60)
            
        Returns:
            List of kernel errors found
        """
        if not self.is_linux:
            return []
        
        crashes = []
        kernel_log_paths = [
            '/var/log/kern.log',
            '/var/log/messages',
            '/var/log/syslog'
        ]
        
        cutoff_time = time.time() - (since_minutes * 60)
        
        for log_path in kernel_log_paths:
            if not os.path.exists(log_path):
                continue
            
            try:
                with open(log_path, 'r', encoding='utf-8', errors='ignore') as f:
                    lines = f.readlines()
                    
                    for line in reversed(lines[-1000:]):
                        if not line.strip():
                            continue
                        
                        line_lower = line.lower()
                        critical_keywords = ['kernel panic', 'kernel oops', 'kernel crash', 
                                           'bug:', 'panic:', 'oops:', 'crash:',
                                           'general protection fault', 'segmentation fault']
                        
                        for keyword in critical_keywords:
                            if keyword in line_lower:
                                try:
                                    parts = line.split()
                                    if len(parts) >= 3:
                                        file_mtime = os.path.getmtime(log_path)
                                        if file_mtime >= cutoff_time:
                                            crash_id = f"{log_path}_{line}"
                                            if crash_id not in self.detected_crashes:
                                                crashes.append({
                                                    'type': 'kernel_error',
                                                    'keyword': keyword,
                                                    'message': line.strip(),
                                                    'timestamp': file_mtime,
                                                    'source': log_path
                                                })
                                                self.detected_crashes.add(crash_id)
                                                break
                                except Exception:
                                    continue
            except (OSError, PermissionError) as e:
                logger.debug(f"Could not read kernel log {log_path}: {e}")
                continue
            except Exception as e:
                logger.debug(f"Error checking kernel log {log_path}: {e}")
                continue
        
        return crashes
    
    def _check_unexpected_reboot(self) -> Optional[Dict]:
        """
        Check for unexpected reboots by monitoring boot time changes.
        Requires at least 10 seconds difference to avoid false positives from timing issues.
        
        Returns:
            Dictionary with reboot information if detected, None otherwise
        """
        if not self.is_linux:
            return None
        
        try:
            current_boot_time = self._get_boot_time()
            
            if current_boot_time is None:
                return None
            
            if self.last_boot_time is None:
                self.last_boot_time = current_boot_time
                return None
            
            boot_time_diff = abs(current_boot_time - self.last_boot_time)
            if boot_time_diff >= 10.0:
                reboot_info = {
                    'type': 'unexpected_reboot',
                    'previous_boot_time': self.last_boot_time,
                    'current_boot_time': current_boot_time,
                    'timestamp': time.time()
                }
                self.last_boot_time = current_boot_time
                return reboot_info
            
            return None
        except Exception as e:
            logger.debug(f"Error checking for unexpected reboot: {e}")
            return None
    
    def check_kernel_crashes(self, since_minutes: int = 60) -> Dict:
        """
        Check for kernel crashes, panics, and oops.
        Also checks for early warning signs that may indicate impending crashes.
        
        Args:
            since_minutes: How many minutes back to check (default: 60)
            
        Returns:
            Dictionary containing crash detection results and early warnings
        """
        result = {
            'crashes_found': [],
            'early_warnings': [],
            'hardware_errors': [],
            'unexpected_reboot': None,
            'total_crashes': 0,
            'total_warnings': 0,
            'check_time': time.time()
        }
        
        if not self.is_linux:
            result['error'] = 'Kernel monitoring only available on Linux'
            return result
        
        reboot_info = self._check_unexpected_reboot()
        if reboot_info:
            result['unexpected_reboot'] = reboot_info
        
        early_warnings = self._check_early_warning_signs(since_minutes=since_minutes)
        result['early_warnings'] = early_warnings
        result['total_warnings'] = len(early_warnings)
        
        hardware_errors = self._check_hardware_errors()
        result['hardware_errors'] = hardware_errors
        
        journal_crashes = self._check_journalctl(since_minutes=since_minutes)
        result['crashes_found'].extend(journal_crashes)
        
        dmesg_crashes = self._check_dmesg(since_minutes=since_minutes)
        result['crashes_found'].extend(dmesg_crashes)
        
        log_crashes = self._check_kernel_logs(since_minutes=since_minutes)
        result['crashes_found'].extend(log_crashes)
        
        result['total_crashes'] = len(result['crashes_found'])
        
        return result
    
    def get_kernel_status(self) -> Dict:
        """
        Get current kernel status summary.
        
        Returns:
            Dictionary containing kernel status information
        """
        status = {
            'monitoring_enabled': self.is_linux,
            'last_boot_time': self.last_boot_time,
            'detected_crashes_count': len(self.detected_crashes)
        }
        
        if self.is_linux:
            try:
                with open('/proc/version', 'r') as f:
                    status['kernel_version'] = f.read().strip()
            except Exception:
                status['kernel_version'] = 'Unknown'
            
            try:
                with open('/proc/uptime', 'r') as f:
                    parts = f.read().split()
                    if len(parts) >= 1:
                        uptime_seconds = float(parts[0])
                        status['uptime_seconds'] = uptime_seconds
                    else:
                        uptime_seconds = 0
                    status['uptime_formatted'] = self._format_uptime(uptime_seconds)
            except Exception:
                status['uptime_seconds'] = None
                status['uptime_formatted'] = 'Unknown'
        
        return status
    
    def _format_uptime(self, seconds: float) -> str:
        """
        Format uptime in human-readable format.
        
        Args:
            seconds: Uptime in seconds
            
        Returns:
            Formatted uptime string
        """
        days = int(seconds // 86400)
        hours = int((seconds % 86400) // 3600)
        minutes = int((seconds % 3600) // 60)
        
        if days > 0:
            return f"{days}d {hours}h {minutes}m"
        elif hours > 0:
            return f"{hours}h {minutes}m"
        else:
            return f"{minutes}m"
