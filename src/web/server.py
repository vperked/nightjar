import json
import threading
import socket
import signal
import time
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
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
    
    def log_message(self, format, *args):
        """Override to use our logger instead of default logging."""
        logger.info(f"{self.address_string()} - {format % args}")
    
    def do_GET(self):
        """Handle GET requests."""
        try:
            if self.path == '/' or self.path == '/index.html':
                self.serve_html()
            elif self.path == '/api/stats':
                self.serve_stats()
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
    
    def get_system_stats(self):
        """Collect current system statistics."""
        global cpu_monitor, memory_monitor, system_monitor, gpu_monitor
        global disk_monitor, network_monitor, sensors_monitor
        
        stats = {}
        
        if system_monitor:
            try:
                stats['system'] = system_monitor.get_system_info()
                stats['uptime'] = system_monitor.get_uptime()
            except Exception as e:
                logger.warning(f"Error getting system info: {e}")
        
        if cpu_monitor:
            try:
                stats['cpu'] = cpu_monitor.get_cpu_info()
                stats['cpu']['cpu_percent'] = cpu_monitor.get_cpu_percent(interval=0.05)
            except Exception as e:
                logger.warning(f"Error getting CPU info: {e}")
        
        if memory_monitor:
            try:
                stats['memory'] = memory_monitor.get_memory_stats()
            except Exception as e:
                logger.warning(f"Error getting memory info: {e}")
        
        if disk_monitor:
            try:
                stats['disk'] = {
                    'total': disk_monitor.get_total_disk_usage()
                }
            except Exception as e:
                logger.warning(f"Error getting disk info: {e}")
        
        if gpu_monitor:
            try:
                if gpu_monitor.nvidia_smi_available and gpu_monitor.get_gpu_count() > 0:
                    gpu_data = gpu_monitor.get_gpu_comprehensive(0)
                    stats['gpu'] = gpu_data
            except Exception as e:
                logger.warning(f"Error getting GPU info: {e}")
        
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
                bandwidth = network_monitor.get_bandwidth_usage(interval=0.2)
                if bandwidth:
                    stats['network'] = bandwidth
            except Exception as e:
                logger.warning(f"Error getting network info: {e}")
        
        if system_monitor:
            try:
                stats['processes'] = system_monitor.get_process_count()
            except Exception as e:
                logger.warning(f"Error getting process count: {e}")
        
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

