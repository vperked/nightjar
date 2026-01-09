import json
import threading
import socket
import signal
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from typing import Dict, Optional
from logger import get_logger

logger = get_logger('WebServer')

cpu_monitor = None
memory_monitor = None
system_monitor = None
gpu_monitor = None
disk_monitor = None
network_monitor = None
sensors_monitor = None
httpd = None
server_thread = None

_process_cache = {
    'top_cpu': [],
    'top_memory': [],
    'last_update': 0.0,
    'cache_interval': 5.0  # Increased from 2.0 to reduce load
}

_network_cache = {
    'data': {},
    'last_update': 0.0,
    'cache_interval': 1.0
}

_gpu_cache = {
    'data': {},
    'last_update': 0.0,
    'cache_interval': 3.0
}

_disk_cache = {
    'data': {},
    'last_update': 0.0,
    'cache_interval': 5.0
}


def set_monitors(cpu, memory, system, gpu, disk, network, sensors):
    """Set monitor instances to use for stats collection."""
    global cpu_monitor, memory_monitor, system_monitor, gpu_monitor
    global disk_monitor, network_monitor, sensors_monitor
    
    cpu_monitor = cpu
    memory_monitor = memory
    system_monitor = system
    gpu_monitor = gpu
    disk_monitor = disk
    network_monitor = network
    sensors_monitor = sensors


class WebRequestHandler(BaseHTTPRequestHandler):
    """HTTP request handler for serving the web UI and API."""
    
    def log_message(self, format_str, *args):
        """Override to use our logger instead of default logging."""
        logger.info(f"{self.address_string()} - {format_str % args}")
    
    def do_GET(self):
        """Handle GET requests."""
        try:
            if self.path == '/' or self.path == '/index.html':
                self.serve_html()
            elif self.path == '/api/stats':
                self.serve_stats()
            elif self.path == '/health':
                self.serve_health()
            elif self.path == '/favicon.ico':
                self.send_no_content()
            elif self.path.startswith('/js/'):
                self.serve_static_file(self.path)
            else:
                self.send_error_safe(404, "Not Found")
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error (client may have disconnected): {e}")
        except Exception as e:
            logger.error(f"Unexpected error handling request: {e}")
    
    def send_no_content(self):
        """Send a 204 No Content response for favicon requests."""
        try:
            self.send_response(204)
            self.end_headers()
        except (ConnectionAbortedError, BrokenPipeError, OSError):
            pass
    
    def send_error_safe(self, code, message=None):
        """Send an error response, handling connection errors gracefully."""
        try:
            self.send_error(code, message)
        except (ConnectionAbortedError, BrokenPipeError, OSError):
            logger.debug(f"Connection aborted while sending error {code}")
    
    def serve_html(self):
        """Serve the HTML file."""
        try:
            html_path = Path(__file__).parent / 'site' / 'index.html'
            if not html_path.exists():
                self.send_error_safe(404, "HTML file not found")
                return
            
            with open(html_path, 'rb') as f:
                content = f.read()
            
            self.send_response(200)
            self.send_header('Content-type', 'text/html')
            self.send_header('Content-length', str(len(content)))
            self.end_headers()
            self.wfile.write(content)
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error serving HTML: {e}")
        except Exception as e:
            logger.error(f"Error serving HTML: {e}")
            self.send_error_safe(500, f"Internal Server Error: {str(e)}")
    
    def serve_static_file(self, path):
        """Serve static files like JavaScript."""
        try:
            site_dir = Path(__file__).parent / 'site'
            file_path = site_dir / path.lstrip('/')
            
            try:
                file_path.resolve().relative_to(site_dir.resolve())
            except ValueError:
                self.send_error_safe(403, "Forbidden")
                return
            
            if not file_path.exists() or not file_path.is_file():
                self.send_error_safe(404, "File not found")
                return
            
            with open(file_path, 'rb') as f:
                content = f.read()
            
            content_type = 'application/javascript'
            if path.endswith('.css'):
                content_type = 'text/css'
            elif path.endswith('.png'):
                content_type = 'image/png'
            elif path.endswith('.jpg') or path.endswith('.jpeg'):
                content_type = 'image/jpeg'
            elif path.endswith('.svg'):
                content_type = 'image/svg+xml'
            
            self.send_response(200)
            self.send_header('Content-type', content_type)
            self.send_header('Content-length', str(len(content)))
            self.end_headers()
            self.wfile.write(content)
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error serving static file: {e}")
        except Exception as e:
            logger.error(f"Error serving static file: {e}")
            self.send_error_safe(500, f"Internal Server Error: {str(e)}")
    
    def serve_stats(self):
        """Serve system statistics as JSON."""
        try:
            start_time = time.time()
            stats = self.get_system_stats()
            elapsed = time.time() - start_time
            
            if elapsed > 1.0:
                logger.warning(f"Stats collection took {elapsed:.2f}s (slow)")
            
            response = json.dumps(stats, indent=2).encode('utf-8')
            
            self.send_response(200)
            self.send_header('Content-type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.send_header('Content-length', str(len(response)))
            self.end_headers()
            self.wfile.write(response)
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error serving stats: {e}")
        except Exception as e:
            logger.error(f"Error serving stats: {e}")
            try:
                error_response = json.dumps({'error': str(e)}).encode('utf-8')
                self.send_response(500)
                self.send_header('Content-type', 'application/json')
                self.send_header('Content-length', str(len(error_response)))
                self.end_headers()
                self.wfile.write(error_response)
            except (ConnectionAbortedError, BrokenPipeError, OSError):
                logger.debug("Connection aborted while sending error response")
    
    def serve_health(self):
        """Serve health check endpoint with comprehensive system checks."""
        try:
            import time
            import psutil
            from datetime import datetime
            
            health_status = 'ok'
            checks = {}
            issues = []
            
            if cpu_monitor:
                try:
                    cpu_percent = cpu_monitor.get_cpu_percent(interval=0.1)
                    checks['cpu'] = {
                        'status': 'ok',
                        'usage_percent': round(cpu_percent, 1)
                    }
                    if cpu_percent > 95:
                        issues.append('CPU usage very high')
                        health_status = 'degraded'
                except Exception as e:
                    checks['cpu'] = {'status': 'error', 'error': str(e)}
                    issues.append('CPU monitor failed')
                    health_status = 'error'
            
            if memory_monitor:
                try:
                    memory_percent = memory_monitor.get_memory_percent()
                    memory_info = memory_monitor.get_memory_info()
                    checks['memory'] = {
                        'status': 'ok',
                        'usage_percent': round(memory_percent, 1),
                        'available_gb': round(memory_info.get('available', 0) / (1024**3), 2)
                    }
                    if memory_percent > 95:
                        issues.append('Memory usage very high')
                        health_status = 'degraded'
                    if memory_info.get('available', 0) < 100 * 1024 * 1024:
                        issues.append('Low memory available (<100MB)')
                        health_status = 'degraded'
                except Exception as e:
                    checks['memory'] = {'status': 'error', 'error': str(e)}
                    issues.append('Memory monitor failed')
                    health_status = 'error'
            
            if disk_monitor:
                try:
                    disk_usage = disk_monitor.get_total_disk_usage()
                    disk_percent = disk_usage.get('percent', 0)
                    free_gb = disk_usage.get('free', 0) / (1024**3)
                    checks['disk'] = {
                        'status': 'ok',
                        'usage_percent': round(disk_percent, 1),
                        'free_gb': round(free_gb, 2)
                    }
                    if disk_percent > 95:
                        issues.append('Disk usage very high (>95%)')
                        health_status = 'degraded'
                    if free_gb < 1.0:
                        issues.append('Low disk space (<1GB free)')
                        health_status = 'degraded'
                except Exception as e:
                    checks['disk'] = {'status': 'error', 'error': str(e)}
                    issues.append('Disk monitor failed')
                    health_status = 'error'
            
            try:
                boot_time = psutil.boot_time()
                uptime_seconds = time.time() - boot_time
                uptime_hours = uptime_seconds / 3600
                checks['system'] = {
                    'status': 'ok',
                    'uptime_hours': round(uptime_hours, 1),
                    'boot_time': datetime.fromtimestamp(boot_time).isoformat()
                }
            except Exception as e:
                checks['system'] = {'status': 'error', 'error': str(e)}
                issues.append('System info check failed')
                if health_status == 'ok':
                    health_status = 'degraded'
            
            if sensors_monitor:
                try:
                    if sensors_monitor.is_available():
                        cpu_temp = sensors_monitor.get_cpu_temperature()
                        checks['temperature'] = {
                            'status': 'ok',
                            'cpu_temp_celsius': round(cpu_temp, 1) if cpu_temp else None
                        }
                        if cpu_temp and cpu_temp > 90:
                            issues.append('CPU temperature very high (>90°C)')
                            health_status = 'degraded'
                    else:
                        checks['temperature'] = {'status': 'unavailable'}
                except Exception as e:
                    checks['temperature'] = {'status': 'error', 'error': str(e)}
            
            health_data = {
                'status': health_status,
                'timestamp': time.time(),
                'timestamp_iso': datetime.now().isoformat(),
                'checks': checks
            }
            
            if issues:
                health_data['issues'] = issues
            
            response = json.dumps(health_data, indent=2).encode('utf-8')
            
            status_code = 200 if health_status == 'ok' else (503 if health_status == 'degraded' else 500)
            self.send_response(status_code)
            self.send_header('Content-type', 'application/json')
            self.send_header('Access-Control-Allow-Origin', '*')
            self.send_header('Content-length', str(len(response)))
            self.end_headers()
            self.wfile.write(response)
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error serving health: {e}")
        except Exception as e:
            logger.error(f"Error serving health: {e}")
            try:
                error_response = json.dumps({
                    'status': 'error',
                    'error': str(e),
                    'timestamp': time.time()
                }).encode('utf-8')
                self.send_response(500)
                self.send_header('Content-type', 'application/json')
                self.send_header('Content-length', str(len(error_response)))
                self.end_headers()
                self.wfile.write(error_response)
            except (ConnectionAbortedError, BrokenPipeError, OSError):
                logger.debug("Connection aborted while sending health error response")
    
    def get_system_stats(self) -> Dict:
        """Collect current system statistics with caching for performance."""
        global cpu_monitor, memory_monitor, system_monitor, gpu_monitor
        global disk_monitor, network_monitor, sensors_monitor
        global _process_cache, _network_cache, _gpu_cache, _disk_cache
        
        stats = {}
        current_time = time.time()
        
        if system_monitor:
            try:
                stats['system'] = system_monitor.get_system_info()
                stats['uptime'] = system_monitor.get_uptime()
            except Exception as e:
                logger.warning(f"Error getting system info: {e}")
        
        if cpu_monitor:
            try:
                stats['cpu'] = cpu_monitor.get_cpu_info()
                stats['cpu']['cpu_percent'] = cpu_monitor.get_cpu_percent(interval=0.01)
            except Exception as e:
                logger.warning(f"Error getting CPU info: {e}")
        
        if memory_monitor:
            try:
                stats['memory'] = memory_monitor.get_memory_stats()
            except Exception as e:
                logger.warning(f"Error getting memory info: {e}")
        
        if disk_monitor:
            try:
                time_since_disk_update = current_time - _disk_cache['last_update']
                if time_since_disk_update >= _disk_cache['cache_interval']:
                    disk_data = disk_monitor.get_total_disk_usage()
                    _disk_cache['data'] = {'total': disk_data}
                    _disk_cache['last_update'] = current_time
                    stats['disk'] = _disk_cache['data']
                else:
                    stats['disk'] = _disk_cache['data']
            except Exception as e:
                logger.warning(f"Error getting disk info: {e}")
                stats['disk'] = _disk_cache.get('data', {})
        
        if gpu_monitor:
            try:
                if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
                    time_since_gpu_update = current_time - _gpu_cache['last_update']
                    if time_since_gpu_update >= _gpu_cache['cache_interval']:
                        gpu_data = gpu_monitor.get_gpu_comprehensive(0)
                        _gpu_cache['data'] = gpu_data
                        _gpu_cache['last_update'] = current_time
                        stats['gpu'] = _gpu_cache['data']
                    else:
                        stats['gpu'] = _gpu_cache['data']
            except Exception as e:
                logger.warning(f"Error getting GPU info: {e}")
                stats['gpu'] = _gpu_cache.get('data', {})
        
        if sensors_monitor:
            try:
                if sensors_monitor.is_available():
                    temp = sensors_monitor.get_cpu_temperature()
                    if temp is not None:
                        stats['temperature'] = temp
            except Exception as e:
                logger.warning(f"Error getting temperature: {e}")
        
        if network_monitor:
            try:
                time_since_network_update = current_time - _network_cache['last_update']
                if time_since_network_update >= _network_cache['cache_interval']:
                    bandwidth = network_monitor.get_bandwidth_usage(interval=0.01)
                    if bandwidth:
                        _network_cache['data'] = bandwidth
                        _network_cache['last_update'] = current_time
                        stats['network'] = bandwidth
                    else:
                        stats['network'] = _network_cache['data']
                else:
                    stats['network'] = _network_cache['data']
            except Exception as e:
                logger.warning(f"Error getting network info: {e}")
                stats['network'] = _network_cache.get('data', {})
        
        if system_monitor:
            try:
                stats['processes'] = system_monitor.get_process_count()
                
                time_since_process_update = current_time - _process_cache['last_update']
                if time_since_process_update >= _process_cache['cache_interval']:
                    try:
                        _process_cache['top_cpu'] = system_monitor.get_top_processes(limit=5, sort_by='cpu')
                        _process_cache['top_memory'] = system_monitor.get_top_processes(limit=5, sort_by='memory')
                        _process_cache['last_update'] = current_time
                    except Exception as e:
                        logger.debug(f"Error updating process cache: {e}")
                
                stats['top_processes_cpu'] = _process_cache['top_cpu']
                stats['top_processes_memory'] = _process_cache['top_memory']
            except Exception as e:
                logger.warning(f"Error getting process info: {e}")
                stats['processes'] = {'total': 0}
                stats['top_processes_cpu'] = _process_cache.get('top_cpu', [])
                stats['top_processes_memory'] = _process_cache.get('top_memory', [])
        else:
            stats['processes'] = {'total': 0}
            stats['top_processes_cpu'] = []
            stats['top_processes_memory'] = []
        
        return stats


def get_local_ip_addresses():
    """Get local network IP addresses."""
    addresses = []
    try:
        hostname = socket.gethostname()
        
        addrs = socket.getaddrinfo(hostname, None, socket.AF_INET, socket.SOCK_DGRAM)
        
        for addr_info in addrs:
            ip = addr_info[4][0]
            if ip != '127.0.0.1' and not ip.startswith('169.254.'):
                addresses.append(ip)
        
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.connect(('8.8.8.8', 80))
            primary_ip = s.getsockname()[0]
            s.close()
            if primary_ip not in addresses:
                addresses.insert(0, primary_ip)
        except Exception:
            pass
            
    except Exception as e:
        logger.debug(f"Error getting local IP addresses: {e}")
    
    return addresses


def _run_server(port, host):
    """Internal function to run the server in a thread."""
    global httpd
    
    server_address = (host, port)
    httpd = HTTPServer(server_address, WebRequestHandler)
    
    logger.info(f"Web server starting on http://{host}:{port}")
    logger.info(f"Access the web UI locally at http://localhost:{port}")
    
    network_ips = get_local_ip_addresses()
    if network_ips:
        logger.info("Network access available at:")
        for ip in network_ips:
            logger.info(f"  http://{ip}:{port}")
    else:
        logger.info("Network IP addresses could not be determined")
    
    try:
        httpd.serve_forever()
    except Exception as e:
        logger.error(f"Web server error: {e}")


def start_server(port=8080, host='0.0.0.0'):
    """
    Start the web server in a background thread.
    
    Args:
        port: Port to listen on (default: 8080)
        host: Host to bind to (default: 0.0.0.0)
    """
    global server_thread
    
    if server_thread and server_thread.is_alive():
        logger.warning("Web server is already running")
        return
    
    server_thread = threading.Thread(target=_run_server, args=(port, host), daemon=True)
    server_thread.start()
    logger.info("Web server thread started")


def stop_server():
    """Stop the web server."""
    global httpd, server_thread
    
    if httpd:
        try:
            httpd.shutdown()
            logger.info("Web server stopped")
        except Exception as e:
            logger.warning(f"Error stopping web server: {e}")
        httpd = None
    
    server_thread = None

