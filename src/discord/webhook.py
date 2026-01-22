import os
import requests
import time
from pathlib import Path
from typing import Dict, Optional, List, Any
from datetime import datetime, timezone
from collections import deque
from dotenv import load_dotenv
from util.util import format_bytes, format_bytes_per_second, format_duration
from util.circuit_breaker import CircuitBreaker, CircuitBreakerOpenError
from logger import get_logger

load_dotenv()

logger = get_logger('DiscordWebhook')


class RateLimiter:
    """Simple rate limiter for Discord webhook calls."""

    def __init__(self, max_calls: int = 30, period: float = 60.0):
        """
        Initialize rate limiter.

        Args:
            max_calls: Maximum number of calls allowed in the period
            period: Time period in seconds
        """
        self.max_calls = max_calls
        self.period = period
        self.calls = deque(maxlen=max_calls)

    def wait_if_needed(self):
        """Wait if rate limit would be exceeded."""
        now = time.time()
        while self.calls and self.calls[0] < now - self.period:
            self.calls.popleft()
        if len(self.calls) >= self.max_calls:
            sleep_time = self.period - (now - self.calls[0])
            if sleep_time > 0:
                time.sleep(sleep_time)
        self.calls.append(time.time())


class DiscordWebhook:
    """Discord webhook client for sending messages."""
    DEFAULT_EMBED_COLOR = 0xDC3545

    def __init__(self, webhook_url: Optional[str] = None):
        """
        Initialize Discord webhook client.

        Args:
            webhook_url: Discord webhook URL. If not provided, reads from WEBHOOK env variable.
        """
        self.webhook_url = webhook_url or os.getenv('WEBHOOK')

        if not self.webhook_url:
            raise ValueError(
                "Webhook URL not provided. Set WEBHOOK environment variable "
                "or pass webhook_url parameter."
            )
        self.rate_limiter = RateLimiter(max_calls=30, period=60.0)
        self.alert_log_path = os.getenv('ALERT_LOG_PATH', '/var/log/server-monitor-alerts.log')
        self.session = requests.Session()
        self.circuit_breaker = CircuitBreaker(failure_threshold=5, timeout=60.0, success_threshold=2)

    def _handle_webhook_error(self, error: Exception) -> bool:
        """
        Handle webhook errors consistently.

        Args:
            error: Exception that occurred

        Returns:
            False to indicate failure
        """
        if isinstance(error, requests.exceptions.Timeout):
            logger.warning("Discord webhook timeout")
        elif isinstance(error, requests.exceptions.ConnectionError):
            logger.warning("Discord connection failed")
        elif isinstance(error, requests.exceptions.HTTPError):
            logger.warning(f"Discord webhook HTTP error: {error}")
        elif isinstance(error, requests.exceptions.RequestException):
            logger.warning(f"Discord webhook request error: {error}")
        else:
            logger.error(f"Unexpected Discord webhook error: {error}")
        return False

    def _log_fallback(self, message: str):
        """
        Log alert to file as fallback when Discord webhook fails.

        Args:
            message: Message content to log
        """
        try:
            timestamp = datetime.now(timezone.utc).isoformat()
            log_entry = f"{timestamp}: {message}\n"
            with open(self.alert_log_path, 'a') as f:
                f.write(log_entry)
        except (OSError, PermissionError) as e:
            logger.debug(f"Could not write to alert log file {self.alert_log_path}: {e}")

    def build_embed_payload(self, title: str, description: str = "", color: Optional[int] = None,
                            fields: Optional[List[Dict[str, Any]]] = None,
                            footer: Optional[str] = None, timestamp: bool = True) -> Dict[str, Any]:
        resolved_color = self.DEFAULT_EMBED_COLOR if color is None else color
        embed = {
            'title': title[:256],
            'description': description[:4096] if description else "",
            'color': resolved_color
        }

        if fields:
            embed['fields'] = []
            for field in fields[:25]:
                embed['fields'].append({
                    'name': field.get('name', '')[:256],
                    'value': field.get('value', '')[:1024],
                    'inline': field.get('inline', False)
                })

        if footer:
            embed['footer'] = {
                'text': footer[:2048]
            }

        if timestamp:
            embed['timestamp'] = datetime.now(timezone.utc).isoformat()

        return {'embeds': [embed]}

    def send_shutdown_notification(self, uptime_seconds: float, shutdown_seconds: float) -> bool:
        """
        Send a shutdown notification embed with uptime and shutdown duration.

        Args:
            uptime_seconds: How long the server monitor ran in seconds
            shutdown_seconds: How long the shutdown process took in seconds

        Returns:
            True if message was sent successfully, False otherwise
        """
        uptime_formatted = format_duration(uptime_seconds)
        shutdown_formatted = format_duration(shutdown_seconds)

        color = 0x3498DB

        fields = [
            {
                'name': 'Monitoring Duration',
                'value': uptime_formatted,
                'inline': True
            },
            {
                'name': 'Shutdown Time',
                'value': shutdown_formatted,
                'inline': True
            }
        ]

        if shutdown_seconds < 1.0:
            status = "Fast shutdown"
        elif shutdown_seconds < 3.0:
            status = "Normal shutdown"
        else:
            status = "Slow shutdown"

        fields.append({
            'name': 'Status',
            'value': status,
            'inline': True
        })

        return self.send_embed(
            title='Server Monitor Stopped',
            description='Server monitoring has been stopped by user.',
            color=color,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_message(self, content: str, username: Optional[str] = None,
                    avatar_url: Optional[str] = None) -> bool:
        """
        Send a simple text message to Discord.

        Args:
            content: Message content (max 2000 characters)
            username: Override webhook username
            avatar_url: Override webhook avatar URL

        Returns:
            True if message was sent successfully, False otherwise
        """
        payload = {
            'content': content[:2000]
        }

        if username:
            payload['username'] = username
        if avatar_url:
            payload['avatar_url'] = avatar_url

        if self.circuit_breaker.is_open():
            logger.debug("Discord circuit breaker is OPEN, using fallback logging")
            self._log_fallback(str(payload)[:2000])
            return False

        def _send_post():
            self.rate_limiter.wait_if_needed()
            response = self.session.post(self.webhook_url, json=payload, timeout=10)
            response.raise_for_status()
            return True

        try:
            return self.circuit_breaker.call(_send_post)
        except KeyboardInterrupt:
            raise
        except CircuitBreakerOpenError:
            logger.debug("Discord circuit breaker is OPEN, using fallback logging")
            self._log_fallback(str(payload)[:2000])
            return False
        except (requests.exceptions.Timeout, requests.exceptions.ConnectionError,
                requests.exceptions.HTTPError, requests.exceptions.RequestException) as e:
            self._log_fallback(str(payload)[:2000])
            return self._handle_webhook_error(e)
        except Exception as e:
            self._log_fallback(str(payload)[:2000])
            return self._handle_webhook_error(e)

    def send_embed(self, title: str, description: str = "", color: Optional[int] = None,
                  fields: Optional[List[Dict[str, str]]] = None,
                  footer: Optional[str] = None, timestamp: bool = True) -> bool:
        """
        Send an embedded message to Discord.

        Args:
            title: Embed title
            description: Embed description
            color: Embed color as integer (0xRRGGBB format)
            fields: List of field dictionaries with 'name' and 'value' keys
            footer: Footer text
            timestamp: Whether to include timestamp

        Returns:
            True if message was sent successfully, False otherwise
        """
        payload = self.build_embed_payload(
            title=title,
            description=description,
            color=color,
            fields=fields,
            footer=footer,
            timestamp=timestamp
        )

        try:
            self.rate_limiter.wait_if_needed()
            response = self.session.post(self.webhook_url, json=payload, timeout=10)
            response.raise_for_status()
            return True
        except KeyboardInterrupt:
            raise
        except (requests.exceptions.Timeout, requests.exceptions.ConnectionError,
                requests.exceptions.HTTPError, requests.exceptions.RequestException) as e:
            self._log_fallback(f"{title}: {description[:100]}" if description else title)
            return self._handle_webhook_error(e)
        except Exception as e:
            self._log_fallback(f"{title}: {description[:100]}" if description else title)
            return self._handle_webhook_error(e)

    def send_cpu_alert(self, cpu_percent: float, threshold: float = 80.0,
                      per_core: Optional[List[float]] = None) -> bool:
        """
        Send a CPU usage alert to Discord.

        Args:
            cpu_percent: Current CPU usage percentage
            threshold: CPU threshold that triggered the alert
            per_core: Optional list of per-core CPU usage

        Returns:
            True if message was sent successfully, False otherwise
        """
        color = 0x000000 if cpu_percent >= threshold else 0xdc3545

        fields = [
            {
                'name': 'Overall CPU Usage',
                'value': f'{cpu_percent:.1f}%',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}%',
                'inline': True
            }
        ]

        if per_core:
            core_values = '\n'.join([f'Core {i}: {usage:.1f}%'
                                    for i, usage in enumerate(per_core)])
            fields.append({
                'name': 'Per-Core Usage',
                'value': core_values[:1024],
                'inline': False
            })

        return self.send_embed(
            title='CPU Usage Alert',
            description=f'CPU usage has exceeded the threshold of {threshold:.1f}%',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_system_info(self, cpu_info: Dict) -> bool:
        """
        Send system CPU information to Discord.

        Args:
            cpu_info: Dictionary containing CPU information from CpuMonitor.get_cpu_info()

        Returns:
            True if message was sent successfully, False otherwise
        """
        fields = [
            {
                'name': 'CPU Count',
                'value': f"Logical: {cpu_info.get('cpu_count_logical', 'N/A')}\n"
                        f"Physical: {cpu_info.get('cpu_count_physical', 'N/A')}",
                'inline': True
            },
            {
                'name': 'CPU Usage',
                'value': f"{cpu_info.get('cpu_percent', 0):.1f}%",
                'inline': True
            }
        ]

        freq_info = cpu_info.get('cpu_freq', {})
        if freq_info.get('current', 0) > 0:
            fields.append({
                'name': 'CPU Frequency',
                'value': f"{freq_info.get('current', 0):.0f} MHz",
                'inline': True
            })

        load_avg = cpu_info.get('load_average')
        if load_avg:
            fields.append({
                'name': 'Load Average',
                'value': f"1m: {load_avg[0]:.2f}\n5m: {load_avg[1]:.2f}\n15m: {load_avg[2]:.2f}",
                'inline': True
            })

        return self.send_embed(
            title='System CPU Information',
            description='Current CPU statistics and usage',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_memory_alert(self, memory_percent: float, threshold: float = 80.0,
                         memory_info: Optional[Dict] = None,
                         swap_info: Optional[Dict] = None) -> bool:
        """
        Send a memory usage alert to Discord.

        Args:
            memory_percent: Current memory usage percentage
            threshold: Memory threshold that triggered the alert
            memory_info: Optional dictionary containing memory information
            swap_info: Optional dictionary containing swap information

        Returns:
            True if message was sent successfully, False otherwise
        """
        color = 0x000000 if memory_percent >= threshold else 0xdc3545

        fields = [
            {
                'name': 'Memory Usage',
                'value': f'{memory_percent:.1f}%',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}%',
                'inline': True
            }
        ]

        if memory_info:
            mem_human = self._format_memory_info(memory_info)
            fields.append({
                'name': 'Memory Details',
                'value': f"Used: {mem_human.get('used', 'N/A')}\n"
                        f"Available: {mem_human.get('available', 'N/A')}\n"
                        f"Total: {mem_human.get('total', 'N/A')}",
                'inline': False
            })

        if swap_info and swap_info.get('total', 0) > 0:
            swap_human = self._format_memory_info(swap_info)
            fields.append({
                'name': 'Swap Memory',
                'value': f"Used: {swap_human.get('used', 'N/A')} ({swap_info.get('percent', 0):.1f}%)\n"
                        f"Total: {swap_human.get('total', 'N/A')}",
                'inline': True
            })

        return self.send_embed(
            title='Memory Usage Alert',
            description=f'Memory usage has exceeded the threshold of {threshold:.1f}%',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_memory_info(self, memory_stats: Dict) -> bool:
        """
        Send system memory information to Discord.

        Args:
            memory_stats: Dictionary containing memory information from MemoryMonitor.get_memory_stats()

        Returns:
            True if message was sent successfully, False otherwise
        """
        mem_info = memory_stats.get('virtual_memory_human', {})
        mem_percent = memory_stats.get('memory_percent', 0)

        fields = [
            {
                'name': 'Memory Usage',
                'value': f"{mem_percent:.1f}%",
                'inline': True
            },
            {
                'name': 'Total Memory',
                'value': mem_info.get('total', 'N/A'),
                'inline': True
            },
            {
                'name': 'Used Memory',
                'value': mem_info.get('used', 'N/A'),
                'inline': True
            },
            {
                'name': 'Available Memory',
                'value': mem_info.get('available', 'N/A'),
                'inline': True
            },
            {
                'name': 'Free Memory',
                'value': mem_info.get('free', 'N/A'),
                'inline': True
            }
        ]

        if mem_info.get('cached', '0 B') != '0.00 B':
            fields.append({
                'name': 'Cached',
                'value': mem_info.get('cached', 'N/A'),
                'inline': True
            })

        swap_info = memory_stats.get('swap_memory_human', {})
        swap_percent = memory_stats.get('swap_memory', {}).get('percent', 0)
        if swap_info.get('total', '0.00 B') != '0.00 B':
            fields.append({
                'name': 'Swap Memory',
                'value': f"{swap_info.get('used', 'N/A')} / {swap_info.get('total', 'N/A')}\n"
                        f"({swap_percent:.1f}% used)",
                'inline': False
            })

        return self.send_embed(
            title='System Memory Information',
            description='Current memory statistics and usage',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_system_info_comprehensive(self, system_info: Dict) -> bool:
        """
        Send comprehensive system information to Discord.

        Args:
            system_info: Dictionary containing system information from SystemMonitor.get_all_info()

        Returns:
            True if message was sent successfully, False otherwise
        """
        sys_info = system_info.get('system', {})
        uptime = system_info.get('uptime', {})
        cpu_info = system_info.get('cpu', {})
        memory_info = system_info.get('memory', {}).get('virtual_memory_human', {})
        disk_info = system_info.get('disk', {}).get('total', {})
        processes = system_info.get('processes', {})

        fields = [
            {
                'name': 'System',
                'value': f"{sys_info.get('platform', 'N/A')} {sys_info.get('platform_release', 'N/A')}\n"
                        f"Hostname: {sys_info.get('hostname', 'N/A')}",
                'inline': False
            },
            {
                'name': 'Uptime',
                'value': uptime.get('formatted', 'N/A'),
                'inline': True
            },
            {
                'name': 'CPU',
                'value': f"{cpu_info.get('cpu_percent', 0):.1f}%\n"
                        f"{cpu_info.get('cpu_count_logical', 'N/A')} cores",
                'inline': True
            },
            {
                'name': 'Memory',
                'value': f"{memory_info.get('percent', 'N/A')}\n"
                        f"{memory_info.get('used', 'N/A')} / {memory_info.get('total', 'N/A')}",
                'inline': True
            },
            {
                'name': 'Disk',
                'value': f"{disk_info.get('percent', 0):.1f}% used",
                'inline': True
            },
            {
                'name': 'Processes',
                'value': f"{processes.get('total', 0)} total",
                'inline': True
            }
        ]

        top_cpu = system_info.get('top_processes_cpu', [])
        if top_cpu:
            cpu_procs = '\n'.join([f"{p.get('name', 'N/A')}: {p.get('cpu_percent', 0):.1f}%"
                                  for p in top_cpu[:3]])
            fields.append({
                'name': 'Top CPU Processes',
                'value': cpu_procs[:1024],
                'inline': True
            })

        top_memory = system_info.get('top_processes_memory', [])
        if top_memory:
            mem_procs = '\n'.join([f"{p.get('name', 'N/A')}: {p.get('memory_percent', 0):.1f}%"
                                  for p in top_memory[:3]])
            fields.append({
                'name': 'Top Memory Processes',
                'value': mem_procs[:1024],
                'inline': True
            })

        return self.send_embed(
            title='System Information',
            description='Comprehensive system status and statistics',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_temperature_alert(self, temperature: float, threshold: float = 80.0,
                               duration: Optional[float] = None,
                               reason: Optional[str] = None,
                               cpu_percent: Optional[float] = None,
                               cpu_freq: Optional[Dict] = None,
                               top_processes: Optional[List[Dict]] = None,
                               fan_speeds: Optional[List[Dict]] = None) -> bool:
        """
        Send a CPU temperature alert to Discord.

        Args:
            temperature: Current CPU temperature in Celsius
            threshold: Temperature threshold that triggered the alert
            duration: How long the temperature has been above threshold (in seconds)
            reason: Human-readable reason for the temperature spike
            cpu_percent: Current CPU usage percentage
            cpu_freq: Optional dictionary containing CPU frequency info
            top_processes: Optional list of top CPU-consuming processes
            fan_speeds: Optional list of fan speed information

        Returns:
            True if message was sent successfully, False otherwise
        """
        color = 0xff6b00 if temperature >= threshold * 1.1 else 0xdc3545

        fields = [
            {
                'name': 'CPU Temperature',
                'value': f'{temperature:.1f}°C',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}°C',
                'inline': True
            }
        ]

        if duration is not None:
            duration_str = f"{duration:.0f}s" if duration < 60 else f"{duration/60:.1f}min"
            fields.append({
                'name': 'Duration Above Threshold',
                'value': duration_str,
                'inline': True
            })

        if reason:
            fields.append({
                'name': 'Likely Cause',
                'value': reason[:1024],
                'inline': False
            })

        if cpu_percent is not None:
            fields.append({
                'name': 'CPU Usage',
                'value': f'{cpu_percent:.1f}%',
                'inline': True
            })

        if cpu_freq and cpu_freq.get('current', 0) > 0:
            freq_current = cpu_freq.get('current', 0)
            freq_max = cpu_freq.get('max', 0)
            freq_percent = (freq_current / freq_max * 100) if freq_max > 0 else 0
            fields.append({
                'name': 'CPU Frequency',
                'value': f'{freq_current:.0f} MHz ({freq_percent:.0f}% of max)',
                'inline': True
            })

        if top_processes:
            process_list = []
            for proc in top_processes[:5]:
                name = proc.get('name', 'Unknown')
                cpu_usage = proc.get('cpu_percent', 0)
                if cpu_usage > 1.0:
                    process_list.append(f"{name}: {cpu_usage:.1f}%")
            if process_list:
                fields.append({
                    'name': 'Top CPU Processes',
                    'value': '\n'.join(process_list)[:1024],
                    'inline': False
                })

        if fan_speeds:
            fan_info = '\n'.join([f"{fan['label']}: {fan['speed_rpm']} RPM"
                                 for fan in fan_speeds[:3]])
            if fan_info:
                fields.append({
                    'name': 'Fan Speeds',
                    'value': fan_info[:1024],
                    'inline': False
                })

        description = f'CPU temperature has exceeded the threshold of {threshold:.1f}°C'
        if duration is not None and duration > 0:
            description += f' for {duration:.0f} seconds'

        return self.send_embed(
            title='Temperature Alert',
            description=description,
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_gpu_alert(self, gpu_utilization: float, threshold: float = 80.0,
                      gpu_info: Optional[Dict] = None,
                      memory_info: Optional[Dict] = None,
                      temperature: Optional[float] = None,
                      power: Optional[Dict] = None) -> bool:
        """
        Send a GPU usage alert to Discord.

        Args:
            gpu_utilization: Current GPU utilization percentage
            threshold: GPU threshold that triggered the alert
            gpu_info: Optional dictionary containing GPU information
            memory_info: Optional dictionary containing GPU memory information
            temperature: Optional GPU temperature in Celsius
            power: Optional dictionary containing power information

        Returns:
            True if message was sent successfully, False otherwise
        """
        color = 0x000000 if gpu_utilization >= threshold else 0xdc3545

        fields = [
            {
                'name': 'GPU Utilization',
                'value': f'{gpu_utilization:.1f}%',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}%',
                'inline': True
            }
        ]

        if gpu_info:
            fields.append({
                'name': 'GPU Info',
                'value': f"{gpu_info.get('name', 'N/A')}\n"
                        f"Driver: {gpu_info.get('driver_version', 'N/A')}",
                'inline': False
            })

        if memory_info:
            mem_percent = memory_info.get('percent', 0)
            mem_used = memory_info.get('used', 0)
            mem_total = memory_info.get('total', 0)
            from util.util import format_megabytes
            fields.append({
                'name': 'GPU Memory',
                'value': f"{mem_percent:.1f}% ({format_megabytes(mem_used)} / {format_megabytes(mem_total)})",
                'inline': True
            })

        if temperature is not None and temperature > 0:
            fields.append({
                'name': 'Temperature',
                'value': f'{temperature:.1f}°C',
                'inline': True
            })

        if power:
            power_draw = power.get('draw', 0)
            power_limit = power.get('limit', 0)
            if power_draw > 0:
                fields.append({
                    'name': 'Power',
                    'value': f'{power_draw:.1f}W / {power_limit:.1f}W',
                    'inline': True
                })

        return self.send_embed(
            title='GPU Usage Alert',
            description=f'GPU utilization has exceeded the threshold of {threshold:.1f}%',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_gpu_info(self, gpu_data: Dict) -> bool:
        """
        Send GPU information to Discord.

        Args:
            gpu_data: Dictionary containing GPU information from GpuMonitor.get_gpu_comprehensive()

        Returns:
            True if message was sent successfully, False otherwise
        """
        gpu_info = gpu_data.get('info', {})
        utilization = gpu_data.get('utilization', {})
        memory = gpu_data.get('memory', {})
        temperature = gpu_data.get('temperature', 0)
        power = gpu_data.get('power', {})
        clocks = gpu_data.get('clocks', {})

        fields = [
            {
                'name': 'GPU',
                'value': gpu_info.get('name', 'N/A'),
                'inline': False
            },
            {
                'name': 'GPU Utilization',
                'value': f"{utilization.get('gpu', 0):.1f}%",
                'inline': True
            },
            {
                'name': 'Memory Utilization',
                'value': f"{utilization.get('memory', 0):.1f}%",
                'inline': True
            },
            {
                'name': 'GPU Memory',
                'value': f"{memory.get('used', 0)} MB / {memory.get('total', 0)} MB\n"
                        f"({memory.get('percent', 0):.1f}% used)",
                'inline': True
            }
        ]

        if temperature > 0:
            fields.append({
                'name': 'Temperature',
                'value': f'{temperature:.1f}°C',
                'inline': True
            })

        if power.get('draw', 0) > 0:
            fields.append({
                'name': 'Power',
                'value': f"{power.get('draw', 0):.1f}W / {power.get('limit', 0):.1f}W",
                'inline': True
            })

        if clocks.get('graphics', 0) > 0:
            fields.append({
                'name': 'Clocks',
                'value': f"Graphics: {clocks.get('graphics', 0)} MHz\n"
                        f"Memory: {clocks.get('memory', 0)} MHz",
                'inline': True
            })

        return self.send_embed(
            title='GPU Information',
            description='Current GPU statistics and usage',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_process_alert(self, process_info: Dict, resource_type: str = 'cpu',
                          threshold: float = 80.0) -> bool:
        """
        Send a process resource usage alert to Discord.

        Args:
            process_info: Dictionary containing process information
            resource_type: 'cpu' or 'memory' to indicate what resource exceeded threshold
            threshold: Threshold that triggered the alert

        Returns:
            True if message was sent successfully, False otherwise
        """
        pid = process_info.get('pid', 'N/A')
        name = process_info.get('name', 'Unknown')
        cpu_percent = process_info.get('cpu_percent', 0)
        memory_percent = process_info.get('memory_percent', 0)
        memory_info = process_info.get('memory_info', {})

        if resource_type == 'cpu':
            resource_value = cpu_percent
            resource_name = 'CPU'
        else:
            resource_value = memory_percent
            resource_name = 'Memory'

        fields = [
            {
                'name': 'Process',
                'value': f"{name} (PID: {pid})",
                'inline': False
            },
            {
                'name': f'{resource_name} Usage',
                'value': f'{resource_value:.1f}%',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}%',
                'inline': True
            }
        ]

        fields.append({
            'name': 'Resource Usage',
            'value': f"CPU: {cpu_percent:.1f}%\nMemory: {memory_percent:.1f}%",
            'inline': True
        })

        if memory_info:
            rss = memory_info.get('rss', 0)
            vms = memory_info.get('vms', 0)
            fields.append({
                'name': 'Memory Details',
                'value': f"RSS: {format_bytes(rss)}\nVMS: {format_bytes(vms)}",
                'inline': True
            })

        username = process_info.get('username', 'N/A')
        if username != 'N/A':
            fields.append({
                'name': 'User',
                'value': username,
                'inline': True
            })

        return self.send_embed(
            title=f'Process {resource_name} Alert',
            description=f'Process "{name}" has exceeded the {resource_name.lower()} threshold of {threshold:.1f}%',
            color=0x000000,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_directory_size_alert(self, directory_path: str, size_bytes: int,
                                  threshold_bytes: int, subdirectories: Optional[List[Dict]] = None) -> bool:
        """
        Send a directory size alert to Discord.

        Args:
            directory_path: Path to the directory that exceeded threshold
            size_bytes: Current size of the directory in bytes
            threshold_bytes: Size threshold that triggered the alert in bytes
            subdirectories: Optional list of subdirectory size information

        Returns:
            True if message was sent successfully, False otherwise
        """
        from util.util import format_bytes

        size_formatted = format_bytes(size_bytes)
        threshold_formatted = format_bytes(threshold_bytes)

        color = 0xff6b00 if size_bytes >= threshold_bytes * 1.1 else 0xdc3545

        fields = [
            {
                'name': 'Directory Path',
                'value': directory_path,
                'inline': False
            },
            {
                'name': 'Current Size',
                'value': size_formatted,
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': threshold_formatted,
                'inline': True
            },
            {
                'name': 'Over Threshold By',
                'value': format_bytes(size_bytes - threshold_bytes),
                'inline': True
            }
        ]

        if subdirectories:
            top_subdirs = subdirectories[:5]
            subdir_list = []
            for subdir in top_subdirs:
                subdir_name = Path(subdir.get('path', 'Unknown')).name
                subdir_size = subdir.get('size_bytes', subdir.get('size', 0))
                if isinstance(subdir_size, str):
                    subdir_size = subdir.get('size_bytes', 0)
                subdir_list.append(f"{subdir_name}: {format_bytes(subdir_size)}")

            if subdir_list:
                fields.append({
                    'name': 'Largest Subdirectories',
                    'value': '\n'.join(subdir_list)[:1024],
                    'inline': False
                })

        description = f'Directory "{Path(directory_path).name}" has exceeded the size threshold of {threshold_formatted}'

        return self.send_embed(
            title='Directory Size Alert',
            description=description,
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor - Directory Monitoring',
            timestamp=True
        )

    def send_partition_alert(self, partition: Dict, threshold: float, is_early_warning: bool = False) -> bool:
        """
        Send an alert when a partition/drive exceeds usage threshold.

        Args:
            partition: Dictionary containing partition information
            threshold: Threshold percentage that was exceeded
            is_early_warning: If True, this is an early warning (not critical yet)

        Returns:
            True if message was sent successfully, False otherwise
        """
        device = partition.get('device', 'Unknown')
        mountpoint = partition.get('mountpoint', 'Unknown')
        percent = partition.get('percent', 0.0)
        total = partition.get('total', 0)
        used = partition.get('used', 0)
        free = partition.get('free', 0)
        is_nvme = partition.get('is_nvme', False)
        fstype = partition.get('fstype', 'Unknown')

        title = 'Drive Early Warning' if is_early_warning else 'Drive Usage Alert'
        if is_nvme:
            title = f'NVMe {title}'

        description = f'{"Drive" if not is_nvme else "NVMe drive"} usage is {"approaching" if is_early_warning else "exceeding"} threshold'

        fields = [
            {
                'name': 'Device',
                'value': device[:256],
                'inline': True
            },
            {
                'name': 'Mount Point',
                'value': mountpoint[:256],
                'inline': True
            },
            {
                'name': 'Usage',
                'value': f'{percent:.1f}%',
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': f'{threshold:.1f}%',
                'inline': True
            },
            {
                'name': 'Total Space',
                'value': format_bytes(total),
                'inline': True
            },
            {
                'name': 'Used Space',
                'value': format_bytes(used),
                'inline': True
            },
            {
                'name': 'Free Space',
                'value': format_bytes(free),
                'inline': True
            }
        ]

        if is_nvme:
            fields.append({
                'name': 'Drive Type',
                'value': 'NVMe SSD',
                'inline': True
            })

        if fstype and fstype != 'Unknown':
            fields.append({
                'name': 'File System',
                'value': fstype,
                'inline': True
            })

        return self.send_embed(
            title=title,
            description=description,
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor - Storage Monitoring',
            timestamp=True
        )

    def send_log_size_alert(self, log_info: Dict, threshold_bytes: int, threshold_formatted: str, cleanup_performed: bool = False) -> bool:
        """
        Send an alert when a log file exceeds the size threshold.

        Args:
            log_info: Dictionary containing log file information
            threshold_bytes: Threshold in bytes
            threshold_formatted: Formatted threshold string
            cleanup_performed: Whether automatic cleanup was performed

        Returns:
            True if message was sent successfully, False otherwise
        """
        log_path = log_info.get('path', 'Unknown')
        size_bytes = log_info.get('size_bytes', 0)
        size_formatted = log_info.get('size_formatted', 'N/A')
        file_count = log_info.get('file_count')

        color = 0xdc3545

        if cleanup_performed:
            description = f'Log file exceeded threshold and was auto-cleaned'
        else:
            description = f'Log file has exceeded the size threshold of {threshold_formatted}'

        fields = [
            {
                'name': 'Log Path',
                'value': log_path[:256],
                'inline': False
            },
            {
                'name': 'Current Size',
                'value': size_formatted,
                'inline': True
            },
            {
                'name': 'Threshold',
                'value': threshold_formatted,
                'inline': True
            }
        ]

        if file_count is not None:
            fields.append({
                'name': 'File Count',
                'value': str(file_count),
                'inline': True
            })

        excess_bytes = size_bytes - threshold_bytes
        if excess_bytes > 0 and not cleanup_performed:
            excess_formatted = format_bytes(excess_bytes)
            fields.append({
                'name': 'Excess Size',
                'value': excess_formatted,
                'inline': True
            })

        if cleanup_performed:
            fields.append({
                'name': 'Status',
                'value': 'Auto-cleaned',
                'inline': True
            })

        return self.send_embed(
            title='Log Size Alert',
            description=description,
            color=color,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_batched_log_cleanup_alert(self, cleaned_logs: List[Dict], threshold_formatted: str) -> bool:
        """
        Send a batched alert for multiple log files that were cleaned.

        Args:
            cleaned_logs: List of dictionaries containing cleaned log file information
            threshold_formatted: Formatted threshold string

        Returns:
            True if message was sent successfully, False otherwise
        """
        if not cleaned_logs:
            return False

        color = 0xdc3545
        log_count = len(cleaned_logs)

        if log_count == 1:
            description = f'Log file exceeded threshold and was auto-cleaned'
        else:
            description = f'{log_count} log files exceeded threshold and were auto-cleaned'

        fields = []

        fields.append({
            'name': 'Logs Cleaned',
            'value': str(log_count),
            'inline': True
        })

        fields.append({
            'name': 'Threshold',
            'value': threshold_formatted,
            'inline': True
        })

        fields.append({
            'name': 'Status',
            'value': 'Auto-cleaned',
            'inline': True
        })

        log_list = []
        for log_info in cleaned_logs[:20]:
            log_path = log_info.get('path', 'Unknown')
            original_size = log_info.get('original_size_formatted')
            if original_size:
                size_formatted = log_info.get('size_formatted', 'N/A')
                log_list.append(f"• `{log_path}` - {original_size} -> {size_formatted}")
            else:
                size_formatted = log_info.get('size_formatted', 'N/A')
                log_list.append(f"• `{log_path}` - {size_formatted}")

        if log_count > 20:
            log_list.append(f"\n*... and {log_count - 20} more log file(s)*")

        fields.append({
            'name': 'Cleaned Log Files',
            'value': '\n'.join(log_list) if log_list else 'None',
            'inline': False
        })

        return self.send_embed(
            title='Log Size Alert - Batch Cleanup',
            description=description,
            color=color,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def send_freeze_alert(self, freeze_status: Dict, system_info: Optional[Dict] = None) -> bool:
        """
        Send a system freeze detection alert to Discord.

        Args:
            freeze_status: Dictionary containing freeze detection information
            system_info: Optional dictionary containing system information

        Returns:
            True if message was sent successfully, False otherwise
        """
        reasons = freeze_status.get('reasons', [])
        time_since_heartbeat = freeze_status.get('time_since_heartbeat', 0)
        time_since_metric_update = freeze_status.get('time_since_metric_update', 0)
        responsive = freeze_status.get('responsive', False)

        color = 0x000000

        primary_reason = reasons[0] if reasons else "System freeze detected"

        fields = []

        if reasons:
            reasons_text = '\n'.join([f"• {reason}" for reason in reasons[:3]])
            fields.append({
                'name': 'Detection Reason',
                'value': reasons_text[:1024],
                'inline': False
            })

        fields.append({
            'name': 'Time Since Last Heartbeat',
            'value': f'{time_since_heartbeat:.1f} seconds',
            'inline': True
        })

        fields.append({
            'name': 'System Responsive',
            'value': 'Yes' if responsive else 'No',
            'inline': True
        })

        if system_info:
            uptime = system_info.get('uptime', {})
            if uptime:
                fields.append({
                    'name': 'System Uptime',
                    'value': uptime.get('formatted', 'N/A'),
                    'inline': True
                })

            cpu_info = system_info.get('cpu', {})
            if cpu_info:
                fields.append({
                    'name': 'Last Known CPU Usage',
                    'value': f"{cpu_info.get('cpu_percent', 0):.1f}%",
                    'inline': True
                })

            memory_info = system_info.get('memory', {})
            if memory_info:
                mem_percent = memory_info.get('memory_percent', 0)
                fields.append({
                    'name': 'Last Known Memory Usage',
                    'value': f"{mem_percent:.1f}%",
                    'inline': True
                })

        description = f'System freeze has been detected: {primary_reason}'

        return self.send_embed(
            title='System Freeze Alert',
            description=description,
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor',
            timestamp=True
        )

    def _format_memory_info(self, mem_dict: Dict) -> Dict[str, str]:
        """
        Helper method to format memory information for display.

        Args:
            mem_dict: Dictionary with memory values in bytes

        Returns:
            Dictionary with formatted human-readable values
        """
        return {
            'total': format_bytes(mem_dict.get('total', 0)),
            'used': format_bytes(mem_dict.get('used', 0)),
            'free': format_bytes(mem_dict.get('free', 0)),
            'available': format_bytes(mem_dict.get('available', 0)),
        }

    def send_download_complete_alert(self, torrent_info: Dict) -> bool:
        """
        Send a notification when a torrent download completes.

        Args:
            torrent_info: Dictionary containing torrent information

        Returns:
            True if message was sent successfully, False otherwise
        """
        name = torrent_info.get('name', 'Unknown')
        size = torrent_info.get('size', 0)
        save_path = torrent_info.get('save_path', 'N/A')
        ratio = torrent_info.get('ratio', 0.0)
        uploaded = torrent_info.get('uploaded', 0)

        fields = [
            {
                'name': 'Torrent Name',
                'value': name[:256],
                'inline': False
            },
            {
                'name': 'Size',
                'value': format_bytes(size),
                'inline': True
            },
            {
                'name': 'Save Path',
                'value': save_path[:256] if save_path != 'N/A' else 'N/A',
                'inline': False
            }
        ]

        if ratio > 0:
            fields.append({
                'name': 'Ratio',
                'value': f'{ratio:.2f}',
                'inline': True
            })

        if uploaded > 0:
            fields.append({
                'name': 'Uploaded',
                'value': format_bytes(uploaded),
                'inline': True
            })

        return self.send_embed(
            title='Download Complete',
            description=f'Torrent download has completed: **{name[:100]}**',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor - qBittorrent',
            timestamp=True
        )

    def send_seeding_notification(self, torrent_info: Dict) -> bool:
        """
        Send a notification when a torrent starts seeding.

        Args:
            torrent_info: Dictionary containing torrent information

        Returns:
            True if message was sent successfully, False otherwise
        """
        name = torrent_info.get('name', 'Unknown')
        size = torrent_info.get('size', 0)
        upload_speed = torrent_info.get('upspeed', 0)
        ratio = torrent_info.get('ratio', 0.0)
        seeds = torrent_info.get('seeds', 0)
        peers = torrent_info.get('peers', 0)
        save_path = torrent_info.get('save_path', 'N/A')

        fields = [
            {
                'name': 'Torrent Name',
                'value': name[:256],
                'inline': False
            },
            {
                'name': 'Size',
                'value': format_bytes(size),
                'inline': True
            },
            {
                'name': 'Upload Speed',
                'value': format_bytes_per_second(upload_speed),
                'inline': True
            },
            {
                'name': 'Ratio',
                'value': f'{ratio:.2f}',
                'inline': True
            },
            {
                'name': 'Seeds / Peers',
                'value': f'{seeds} / {peers}',
                'inline': True
            },
            {
                'name': 'Save Path',
                'value': save_path[:256] if save_path != 'N/A' else 'N/A',
                'inline': False
            }
        ]

        return self.send_embed(
            title='Torrent Seeding',
            description=f'Torrent is now seeding: **{name[:100]}**',
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor - qBittorrent',
            timestamp=True
        )

    def send_torrent_list(self, downloading_torrents: List[Dict], seeding_torrents: List[Dict]) -> bool:
        """
        Send a list of currently downloading and seeding torrents to Discord.
        Only shows torrents with active speeds (> 0).

        Args:
            downloading_torrents: List of dictionaries containing downloading torrent information
            seeding_torrents: List of dictionaries containing seeding torrent information

        Returns:
            True if message was sent successfully, False otherwise
        """
        active_downloading = [t for t in downloading_torrents if t.get('dlspeed', 0) > 0]
        active_seeding = [t for t in seeding_torrents if t.get('upspeed', 0) > 0]

        if not active_downloading and not active_seeding:
            return True

        fields = []

        if active_downloading:
            download_list = []
            for torrent in active_downloading[:10]:
                name = torrent.get('name', 'Unknown')
                if len(name) > 60:
                    name = name[:57] + '...'
                progress = torrent.get('progress_percent', 0.0)
                speed = format_bytes_per_second(torrent.get('dlspeed', 0))
                download_list.append(f"`{name}`\n{progress:.1f}% • {speed}")

            if len(active_downloading) > 10:
                download_list.append(f"\n*+{len(active_downloading) - 10} more*")

            fields.append({
                'name': f'Downloading ({len(active_downloading)})',
                'value': '\n\n'.join(download_list)[:1024],
                'inline': False
            })

        if active_seeding:
            seed_list = []
            for torrent in active_seeding[:10]:
                name = torrent.get('name', 'Unknown')
                if len(name) > 60:
                    name = name[:57] + '...'
                ratio = torrent.get('ratio', 0.0)
                speed = format_bytes_per_second(torrent.get('upspeed', 0))
                seed_list.append(f"`{name}`\nRatio: {ratio:.2f} • {speed}")

            if len(active_seeding) > 10:
                seed_list.append(f"\n*+{len(active_seeding) - 10} more*")

            fields.append({
                'name': f'Seeding ({len(active_seeding)})',
                'value': '\n\n'.join(seed_list)[:1024],
                'inline': False
            })

        total_downloading = len(active_downloading)
        total_seeding = len(active_seeding)
        description = f"{total_downloading} downloading • {total_seeding} seeding"

        return self.send_embed(
            title='qBittorrent Status',
            description=description,
            color=self.DEFAULT_EMBED_COLOR,
            fields=fields,
            footer='Server Monitor - qBittorrent',
            timestamp=True
        )

