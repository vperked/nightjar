import os
import requests
from typing import Dict, Optional, List
from datetime import datetime, timezone
from dotenv import load_dotenv
from util.util import format_bytes
from logger import get_logger

load_dotenv()

logger = get_logger('DiscordWebhook')


class DiscordWebhook:
    """Discord webhook client for sending messages."""
    
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
        elif isinstance(error, requests.exceptions.RequestException):
            logger.warning(f"Discord webhook error - {error}")
        else:
            logger.warning(f"Discord webhook error - {error}")
        return False
    
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
        
        try:
            response = requests.post(self.webhook_url, json=payload, timeout=10)
            response.raise_for_status()
            return True
        except KeyboardInterrupt:
            raise
        except Exception as e:
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
        embed = {
            'title': title[:256],
            'description': description[:4096] if description else "",
        }
        
        if color is not None:
            embed['color'] = color
        
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
        
        payload = {
            'embeds': [embed]
        }
        
        try:
            response = requests.post(self.webhook_url, json=payload, timeout=10)
            response.raise_for_status()
            return True
        except KeyboardInterrupt:
            raise
        except Exception as e:
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
            color=color,
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
            color=0xdc3545,
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
            color=color,
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
            color=0xdc3545,
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
            color=0xdc3545,
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
            color=color,
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
            color=0xdc3545,
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

