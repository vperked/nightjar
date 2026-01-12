import psutil
import time
from typing import Dict, List, Optional, Callable
from datetime import datetime
from util.util import format_bytes, format_bytes_per_second
from logger import get_logger

logger = get_logger()


class NetworkMonitor:
    """Monitor network interfaces, bandwidth, and statistics."""
    
    def __init__(self):
        """Initialize the network monitor."""
        self._last_io_counters = None
        self._last_io_timestamp = None
        self._last_io_per_nic = None
    
    def get_network_interfaces(self) -> List[Dict]:
        """
        Get information about all network interfaces.
        
        Returns:
            List of dictionaries containing interface information
        """
        interfaces = psutil.net_if_addrs()
        stats = psutil.net_if_stats()
        io_counters = psutil.net_io_counters(pernic=True)
        
        interface_list = []
        
        for interface_name, addrs in interfaces.items():
            interface_info = {
                'name': interface_name,
                'addresses': [],
                'isup': stats[interface_name].isup if interface_name in stats else False,
                'speed': stats[interface_name].speed if interface_name in stats else 0,
                'mtu': stats[interface_name].mtu if interface_name in stats else 0,
                'duplex': getattr(stats[interface_name], 'duplex', None) if interface_name in stats else None,
            }
            
            for addr in addrs:
                interface_info['addresses'].append({
                    'family': str(addr.family),
                    'address': addr.address,
                    'netmask': addr.netmask if addr.netmask else None,
                    'broadcast': addr.broadcast if addr.broadcast else None,
                })
            
            if interface_name in io_counters:
                io = io_counters[interface_name]
                interface_info['io'] = {
                    'bytes_sent': io.bytes_sent,
                    'bytes_recv': io.bytes_recv,
                    'packets_sent': io.packets_sent,
                    'packets_recv': io.packets_recv,
                    'errin': io.errin,
                    'errout': io.errout,
                    'dropin': io.dropin,
                    'dropout': io.dropout,
                }
            else:
                interface_info['io'] = None
            
            interface_list.append(interface_info)
        
        return interface_list
    
    def get_interface_by_name(self, name: str) -> Optional[Dict]:
        """
        Get information about a specific network interface.
        
        Args:
            name: Interface name
            
        Returns:
            Interface dictionary if found, None otherwise
        """
        interfaces = self.get_network_interfaces()
        for interface in interfaces:
            if interface['name'] == name:
                return interface
        return None
    
    def get_active_interfaces(self) -> List[Dict]:
        """
        Get only active (up) network interfaces.
        
        Returns:
            List of active interface dictionaries
        """
        all_interfaces = self.get_network_interfaces()
        return [iface for iface in all_interfaces if iface['isup']]
    
    def get_total_network_io(self) -> Dict:
        """
        Get system-wide network I/O statistics.
        
        Returns:
            Dictionary containing total network I/O statistics
        """
        io = psutil.net_io_counters()
        if io is None:
            return {}
        
        return {
            'bytes_sent': io.bytes_sent,
            'bytes_recv': io.bytes_recv,
            'packets_sent': io.packets_sent,
            'packets_recv': io.packets_recv,
            'errin': io.errin,
            'errout': io.errout,
            'dropin': io.dropin,
            'dropout': io.dropout,
        }
    
    def get_network_io_human(self) -> Dict:
        """
        Get network I/O statistics in human-readable format.
        
        Returns:
            Dictionary with formatted network I/O values
        """
        io = self.get_total_network_io()
        if not io:
            return {}
        
        return {
            'bytes_sent': format_bytes(io['bytes_sent']),
            'bytes_recv': format_bytes(io['bytes_recv']),
            'packets_sent': f"{io['packets_sent']:,}",
            'packets_recv': f"{io['packets_recv']:,}",
            'errin': f"{io['errin']:,}",
            'errout': f"{io['errout']:,}",
            'dropin': f"{io['dropin']:,}",
            'dropout': f"{io['dropout']:,}",
        }
    
    def get_bandwidth_usage(self, interface: Optional[str] = None,
                           interval: float = 1.0, use_cache: bool = True) -> Dict:
        """
        Get current bandwidth usage (bytes per second).
        
        Args:
            interface: Interface name (None for system-wide)
            interval: Time interval to measure over in seconds (must be > 0)
            use_cache: If True, uses cached counters for non-blocking calculation (recommended)
            
        Returns:
            Dictionary containing bandwidth usage
        """
        if interval <= 0:
            logger.warning("Invalid interval for bandwidth calculation")
            return {}
        
        # Ensure interval is safe for division (prevent division by zero)
        safe_interval = max(interval, 0.001)
        
        if use_cache:
            # Non-blocking approach using cached values
            return self._get_bandwidth_from_cache(interface, safe_interval)
        else:
            # Blocking approach with time.sleep() - kept for compatibility
            return self._get_bandwidth_blocking(interface, safe_interval)
    
    def _get_bandwidth_from_cache(self, interface: Optional[str], interval: float) -> Dict:
        """
        Calculate bandwidth from cached counters (non-blocking).
        
        Args:
            interface: Interface name (None for system-wide)
            interval: Time interval for rate calculation
            
        Returns:
            Dictionary containing bandwidth usage
        """
        import time
        
        current_time = time.time()
        
        if interface:
            current_io = psutil.net_io_counters(pernic=True)
            if not current_io or interface not in current_io:
                return {}
            
            # Check if we have cached data for this interface
            if (self._last_io_per_nic and interface in self._last_io_per_nic 
                and self._last_io_timestamp):
                time_delta = current_time - self._last_io_timestamp
                if time_delta > 0:
                    before = self._last_io_per_nic[interface]
                    after = current_io[interface]
                    
                    result = {
                        'interface': interface,
                        'bytes_sent_per_sec': (after.bytes_sent - before.bytes_sent) / time_delta,
                        'bytes_recv_per_sec': (after.bytes_recv - before.bytes_recv) / time_delta,
                        'packets_sent_per_sec': (after.packets_sent - before.packets_sent) / time_delta,
                        'packets_recv_per_sec': (after.packets_recv - before.packets_recv) / time_delta,
                    }
                    
                    # Update cache
                    self._last_io_per_nic = current_io
                    self._last_io_timestamp = current_time
                    
                    return result
            
            # No cached data, initialize cache and return zeros
            self._last_io_per_nic = current_io
            self._last_io_timestamp = current_time
            return {
                'interface': interface,
                'bytes_sent_per_sec': 0.0,
                'bytes_recv_per_sec': 0.0,
                'packets_sent_per_sec': 0.0,
                'packets_recv_per_sec': 0.0,
            }
        else:
            # System-wide
            current_io = psutil.net_io_counters()
            if current_io is None:
                return {}
            
            # Check if we have cached data
            if self._last_io_counters and self._last_io_timestamp:
                time_delta = current_time - self._last_io_timestamp
                if time_delta > 0:
                    result = {
                        'interface': 'system-wide',
                        'bytes_sent_per_sec': (current_io.bytes_sent - self._last_io_counters.bytes_sent) / time_delta,
                        'bytes_recv_per_sec': (current_io.bytes_recv - self._last_io_counters.bytes_recv) / time_delta,
                        'packets_sent_per_sec': (current_io.packets_sent - self._last_io_counters.packets_sent) / time_delta,
                        'packets_recv_per_sec': (current_io.packets_recv - self._last_io_counters.packets_recv) / time_delta,
                    }
                    
                    # Update cache
                    self._last_io_counters = current_io
                    self._last_io_timestamp = current_time
                    
                    return result
            
            # No cached data, initialize cache and return zeros
            self._last_io_counters = current_io
            self._last_io_timestamp = current_time
            return {
                'interface': 'system-wide',
                'bytes_sent_per_sec': 0.0,
                'bytes_recv_per_sec': 0.0,
                'packets_sent_per_sec': 0.0,
                'packets_recv_per_sec': 0.0,
            }
    
    def _get_bandwidth_blocking(self, interface: Optional[str], interval: float) -> Dict:
        """
        Calculate bandwidth using blocking time.sleep() approach.
        
        Args:
            interface: Interface name (None for system-wide)
            interval: Time interval to sleep and measure over
            
        Returns:
            Dictionary containing bandwidth usage
        """
        if interface:
            io_before = psutil.net_io_counters(pernic=True)
            if not io_before or interface not in io_before:
                return {}
            
            time.sleep(interval)
            
            io_after = psutil.net_io_counters(pernic=True)
            if not io_after or interface not in io_after:
                return {}
            
            before = io_before[interface]
            after = io_after[interface]
            
            return {
                'interface': interface,
                'bytes_sent_per_sec': (after.bytes_sent - before.bytes_sent) / interval,
                'bytes_recv_per_sec': (after.bytes_recv - before.bytes_recv) / interval,
                'packets_sent_per_sec': (after.packets_sent - before.packets_sent) / interval,
                'packets_recv_per_sec': (after.packets_recv - before.packets_recv) / interval,
            }
        else:
            io_before = psutil.net_io_counters()
            if io_before is None:
                return {}
            
            time.sleep(interval)
            
            io_after = psutil.net_io_counters()
            if io_after is None:
                return {}
            
            return {
                'interface': 'system-wide',
                'bytes_sent_per_sec': (io_after.bytes_sent - io_before.bytes_sent) / interval,
                'bytes_recv_per_sec': (io_after.bytes_recv - io_before.bytes_recv) / interval,
                'packets_sent_per_sec': (io_after.packets_sent - io_before.packets_sent) / interval,
                'packets_recv_per_sec': (io_after.packets_recv - io_before.packets_recv) / interval,
            }
    
    def get_bandwidth_usage_human(self, interface: Optional[str] = None,
                                 interval: float = 1.0) -> Dict:
        """
        Get current bandwidth usage in human-readable format.
        
        Args:
            interface: Interface name (None for system-wide)
            interval: Time interval to measure over in seconds
            
        Returns:
            Dictionary with formatted bandwidth usage
        """
        bandwidth = self.get_bandwidth_usage(interface, interval)
        if not bandwidth:
            return {}
        
        return {
            'interface': bandwidth['interface'],
            'bytes_sent_per_sec': format_bytes_per_second(bandwidth['bytes_sent_per_sec']),
            'bytes_recv_per_sec': format_bytes_per_second(bandwidth['bytes_recv_per_sec']),
            'packets_sent_per_sec': f"{bandwidth['packets_sent_per_sec']:.2f}",
            'packets_recv_per_sec': f"{bandwidth['packets_recv_per_sec']:.2f}",
        }
    
    def monitor_bandwidth(self, interface: Optional[str] = None,
                         interval: float = 1.0, duration: Optional[float] = None,
                         callback: Optional[Callable[[Dict], None]] = None) -> List[Dict]:
        """
        Continuously monitor bandwidth usage.
        
        Args:
            interface: Interface name to monitor (None for system-wide)
            interval: Time interval between measurements in seconds
            duration: Total duration to monitor in seconds (None for infinite)
            callback: Optional callback function to call with each measurement
            
        Returns:
            List of bandwidth measurements
        """
        measurements = []
        start_time = time.time()
        
        while True:
            if duration and (time.time() - start_time) >= duration:
                break
            
            bandwidth = self.get_bandwidth_usage(interface, interval=interval)
            if bandwidth:
                measurement = {
                    'timestamp': time.time(),
                    'interface': bandwidth.get('interface', interface),
                    'bandwidth': bandwidth,
                }
                measurements.append(measurement)
                
                if callback:
                    callback(measurement)
            
            time.sleep(interval)
        
        return measurements
    
    def get_network_connections(self, kind: str = 'inet') -> List[Dict]:
        """
        Get network connections.
        
        Args:
            kind: Connection kind ('inet', 'inet4', 'inet6', 'tcp', 'tcp4', 'tcp6', 'udp', 'udp4', 'udp6')
            
        Returns:
            List of connection dictionaries
        """
        connections = psutil.net_connections(kind=kind)
        connection_list = []
        
        for conn in connections:
            try:
                conn_info = {
                    'fd': conn.fd,
                    'family': str(conn.family),
                    'type': str(conn.type),
                    'laddr': f"{conn.laddr.ip}:{conn.laddr.port}" if conn.laddr else None,
                    'raddr': f"{conn.raddr.ip}:{conn.raddr.port}" if conn.raddr else None,
                    'status': conn.status,
                    'pid': conn.pid,
                }
                connection_list.append(conn_info)
            except (psutil.AccessDenied, AttributeError):
                continue
        
        return connection_list
    
    def get_network_stats(self) -> Dict:
        """
        Get comprehensive network statistics.
        
        Returns:
            Dictionary containing all network information
        """
        return {
            'interfaces': self.get_network_interfaces(),
            'active_interfaces': self.get_active_interfaces(),
            'total_io': self.get_total_network_io(),
            'total_io_human': self.get_network_io_human(),
            'connections': {
                'tcp': self.get_network_connections(kind='tcp'),
                'udp': self.get_network_connections(kind='udp'),
            },
        }
    
    def find_interface_by_ip(self, ip_address: str) -> Optional[Dict]:
        """
        Find network interface by IP address.
        
        Args:
            ip_address: IP address to search for
            
        Returns:
            Interface dictionary if found, None otherwise
        """
        interfaces = self.get_network_interfaces()
        for interface in interfaces:
            for addr in interface['addresses']:
                if addr['address'] == ip_address:
                    return interface
        return None

