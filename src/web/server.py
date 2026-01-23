import json
import threading
import socket
import time
import psutil
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path
from typing import Dict, Optional, Callable, Any
from concurrent.futures import ThreadPoolExecutor, TimeoutError as FutureTimeoutError
from logger import get_logger
from util.security import validate_path

logger = get_logger('WebServer')


class WebServerState:
    """Web server state to avoid global variables."""

    def __init__(self):
        self.cpu_monitor = None
        self.memory_monitor = None
        self.system_monitor = None
        self.gpu_monitor = None
        self.disk_monitor = None
        self.network_monitor = None
        self.sensors_monitor = None
        self.httpd = None
        self.server_thread = None
        self.crash_webhook = None


_state = WebServerState()

_process_cache = {
    'top_cpu': [],
    'top_memory': [],
    'last_update': 0.0,
    'cache_interval': 5.0
}

_network_cache = {
    'data': {},
    'last_update': 0.0,
    'cache_interval': 2.0,
    'last_io': None,
    'last_io_time': 0.0
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

_cpu_cache = {
    'data': {},
    'last_update': 0.0,
    'cache_interval': 1.0
}

_process_cache_lock = threading.Lock()
_network_cache_lock = threading.Lock()
_gpu_cache_lock = threading.Lock()
_disk_cache_lock = threading.Lock()
_cpu_cache_lock = threading.Lock()


def set_monitors(
    cpu: 'CpuMonitor',
    memory: 'MemoryMonitor',
    system: 'SystemMonitor',
    gpu: 'GpuMonitor',
    disk: 'DiskMonitor',
    network: 'NetworkMonitor',
    sensors: 'SensorsMonitor'
) -> None:
    """
    Set monitor instances to use for stats collection in the web server.

    Args:
        cpu (CpuMonitor): CPU monitor instance
        memory (MemoryMonitor): Memory monitor instance
        system (SystemMonitor): System monitor instance
        gpu (GpuMonitor): GPU monitor instance
        disk (DiskMonitor): Disk monitor instance
        network (NetworkMonitor): Network monitor instance
        sensors (SensorsMonitor): Sensors monitor instance
    """
    _state.cpu_monitor = cpu
    _state.memory_monitor = memory
    _state.system_monitor = system
    _state.gpu_monitor = gpu
    _state.disk_monitor = disk
    _state.network_monitor = network
    _state.sensors_monitor = sensors


def set_crash_webhook(webhook: 'DiscordWebhook') -> None:
    """
    Set crash webhook for sending error notifications.

    Args:
        webhook (DiscordWebhook): DiscordWebhook instance for crash/error notifications
    """
    _state.crash_webhook = webhook


def create_request_handler(monitors: Optional[Dict] = None):
    """
    Factory function to create WebRequestHandler with dependency injection.

    Args:
        monitors: Optional dictionary with monitor instances. If None, uses global state.

    Returns:
        WebRequestHandler class with monitors injected
    """
    class WebRequestHandler(BaseHTTPRequestHandler):
        """HTTP request handler for serving the web UI and API."""

        def __init__(self, *args, **kwargs):
            """Initialize handler with injected monitors."""
            if monitors:
                self.monitors = monitors
            else:
                self.monitors = {
                    'cpu': _state.cpu_monitor,
                    'memory': _state.memory_monitor,
                    'system': _state.system_monitor,
                    'gpu': _state.gpu_monitor,
                    'disk': _state.disk_monitor,
                    'network': _state.network_monitor,
                    'sensors': _state.sensors_monitor,
                }
            try:
                super().__init__(*args, **kwargs)
            except (ConnectionAbortedError, ConnectionResetError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection closed during initialization: {e}")
            except Exception as e:
                logger.error(f"Unexpected error in request handler initialization: {e}", exc_info=True)

        def handle(self):
            """
            Override handle to catch connection errors during request processing.
            
            This catches errors that occur during the entire request lifecycle,
            including reading the request line and headers.
            """
            try:
                super().handle()
            except (ConnectionAbortedError, ConnectionResetError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection closed by client during request handling: {e}")
            except Exception as e:
                logger.error(f"Unexpected error handling request: {e}", exc_info=True)

        def handle_one_request(self):
            """
            Override handle_one_request to catch connection errors during request parsing.
            
            This catches errors that occur when reading the request line specifically.
            """
            try:
                super().handle_one_request()
            except (ConnectionAbortedError, ConnectionResetError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection closed during request: {e}")
            except Exception as e:
                logger.error(f"Unexpected error processing request: {e}", exc_info=True)

        def log_message(self, format_str, *args):
            """
            Override to use our logger instead of default logging.

            Args:
                format_str: Log message format string
                *args: Arguments for format string
            """
            logger.info(f"{self.address_string()} - {format_str % args}")

        def _add_security_headers(self):
            """
            Add security headers to response.

            Includes Content-Security-Policy, X-Content-Type-Options,
            X-Frame-Options, X-XSS-Protection, and Referrer-Policy.
            """
            try:
                csp = ("default-src 'self'; "
                       "script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.tailwindcss.com; "
                       "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "
                       "font-src 'self' https://fonts.gstatic.com data:; "
                       "img-src 'self' data: https:; "
                       "connect-src 'self'; "
                       "frame-ancestors 'none'")
                self.send_header('Content-Security-Policy', csp)
                self.send_header('X-Content-Type-Options', 'nosniff')
                self.send_header('X-Frame-Options', 'DENY')
                self.send_header('X-XSS-Protection', '1; mode=block')
                self.send_header('Referrer-Policy', 'no-referrer')
            except Exception as e:
                logger.error(f"Error adding security headers: {e}", exc_info=True)

        def _add_cors_headers(self, allowed_origin='*'):
            """
            Add CORS headers to response.

            Args:
                allowed_origin: Allowed origin for CORS (default: '*' for same-origin only)
            """
            try:
                self.send_header('Access-Control-Allow-Origin', allowed_origin)
                self.send_header('Access-Control-Allow-Methods', 'GET, OPTIONS')
                self.send_header('Access-Control-Allow-Headers', 'Content-Type')
            except Exception as e:
                logger.error(f"Error adding CORS headers: {e}")

        def _add_connection_headers(self):
            """
            Add connection keep-alive headers to maintain persistent connections.
            """
            try:
                self.send_header('Connection', 'keep-alive')
                self.send_header('Keep-Alive', 'timeout=60, max=1000')
            except Exception as e:
                logger.debug(f"Error adding connection headers: {e}")

        def validate_query_params(self):
            """
            Validate query parameters from request.

            Returns:
                Tuple of (is_valid, error_message)
                - is_valid: True if parameters are valid
                - error_message: Error message if invalid, None otherwise
            """
            from urllib.parse import urlparse, parse_qs

            try:
                parsed = urlparse(self.path)
                query_params = parse_qs(parsed.query)

                for key, values in query_params.items():
                    if not key or len(key) > 100:
                        return False, f"Invalid parameter name: {key}"
                    for value in values:
                        if value and len(value) > 1000:
                            return False, f"Parameter value too long: {key}"
                        if value and ('..' in value or '\x00' in value):
                            return False, f"Suspicious characters in parameter: {key}"

                return True, None
            except Exception as e:
                logger.warning(f"Error validating query parameters: {e}")
                return False, f"Error parsing query parameters: {e}"

        def do_GET(self):
            """
            Handle GET requests.

            Routes requests to appropriate handlers:
            - '/' or '/index.html' -> serve_html()
            - '/api/stats' -> serve_stats()
            - '/health' -> serve_health()
            - '/favicon.ico' -> send_no_content()
            - '/js/*' -> serve_static_file()
            - Other -> 404 Not Found

            Validates query parameters before processing requests.
            """
            try:
                is_valid, error_msg = self.validate_query_params()
                if not is_valid:
                    logger.warning(f"Invalid query parameters: {error_msg}")
                    self.send_error_safe(400, f"Bad Request: {error_msg}")
                    return

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
                logger.error(f"Unexpected error handling request: {e}", exc_info=True)

        def do_OPTIONS(self):
            """
            Handle OPTIONS requests for CORS preflight.

            Responds to CORS preflight requests with appropriate headers.
            """
            try:
                self.send_response(200)
                self._add_cors_headers(allowed_origin='*')
                self._add_security_headers()
                self.end_headers()
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error handling OPTIONS: {e}")
            except Exception as e:
                logger.error(f"Error handling OPTIONS request: {e}")

        def send_no_content(self):
            """
            Send a 204 No Content response.

            Used for favicon requests and other requests that don't need content.
            """
            try:
                self.send_response(204)
                self._add_security_headers()
                self.end_headers()
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error sending no content: {e}")
            except Exception as e:
                logger.error(f"Unexpected error sending no content: {e}", exc_info=True)

        def send_error_safe(self, code, message=None):
            """
            Send an error response, handling connection errors gracefully.

            Args:
                code: HTTP status code
                message: Optional error message
            """
            try:
                self.send_error(code, message)
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error sending error {code}: {e}")
            except Exception as e:
                logger.error(f"Unexpected error sending error {code}: {e}", exc_info=True)

        def serve_html(self):
            """
            Serve the main HTML dashboard page.

            Returns the index.html file from the site directory.
            """
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
                self._add_security_headers()
                self.end_headers()
                self.wfile.write(content)
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error serving HTML: {e}")
            except Exception as e:
                logger.error(f"Error serving HTML: {e}", exc_info=True)
                try:
                    self.send_error_safe(500, f"Internal Server Error: {str(e)}")
                except Exception:
                    pass

        def serve_static_file(self, path):
            """
            Serve static files like JavaScript, CSS, and images.

            Args:
                path: Request path (e.g., '/js/main.js')

            Security: Validates that the file is within the site directory to prevent
            path traversal attacks using validate_path() from security module.
            """
            try:
                site_dir = Path(__file__).parent / 'site'
                file_path = site_dir / path.lstrip('/')

                is_valid, resolved_path, error = validate_path(
                    str(file_path),
                    allowed_base_paths=[str(site_dir)],
                    allow_symlinks=False
                )

                if not is_valid or not resolved_path:
                    logger.warning(f"Path validation failed for {path}: {error}")
                    self.send_error_safe(403, "Forbidden")
                    return

                resolved_path_obj = Path(resolved_path)
                if not resolved_path_obj.exists() or not resolved_path_obj.is_file():
                    logger.warning(f"File not found: {resolved_path} (requested: {path})")
                    self.send_error_safe(404, "File not found")
                    return

                with open(resolved_path, 'rb') as f:
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
                self._add_security_headers()
                self.end_headers()
                self.wfile.write(content)
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error serving static file: {e}")
            except Exception as e:
                logger.error(f"Error serving static file {path}: {e}", exc_info=True)
                try:
                    self.send_error_safe(500, f"Internal Server Error: {str(e)}")
                except Exception:
                    pass

        def serve_stats(self):
            """
            Serve system statistics as JSON.

            Collects data from all monitors and returns as JSON response.
            Logs a warning if stats collection takes longer than 1 second.
            """
            try:
                start_time = time.time()
                stats = self.get_system_stats()
                elapsed = time.time() - start_time

                if elapsed > 1.0:
                    logger.warning(f"Stats collection took {elapsed:.2f}s (slow)")

                if not isinstance(stats, dict):
                    logger.error("get_system_stats returned invalid type, using empty dict")
                    stats = {}

                import os
                debug_mode = os.getenv('DEBUG', 'false').lower() == 'true'
                indent = 2 if debug_mode else None
                response = json.dumps(stats, indent=indent).encode('utf-8')

                self.send_response(200)
                self.send_header('Content-type', 'application/json')
                self.send_header('Content-length', str(len(response)))
                self._add_connection_headers()
                self._add_security_headers()
                self._add_cors_headers(allowed_origin='*')
                self.end_headers()
                self.wfile.write(response)
                self.wfile.flush()
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error serving stats: {e}")
            except Exception as e:
                logger.error(f"Error serving stats: {e}", exc_info=True)
                try:
                    import os
                    debug_mode = os.getenv('DEBUG', 'false').lower() == 'true'
                    indent = 2 if debug_mode else None
                    error_response = json.dumps({'error': str(e), 'timestamp': time.time()}, indent=indent).encode('utf-8')
                    self.send_response(500)
                    self.send_header('Content-type', 'application/json')
                    self.send_header('Content-length', str(len(error_response)))
                    self._add_connection_headers()
                    self._add_security_headers()
                    self._add_cors_headers(allowed_origin='*')
                    self.end_headers()
                    self.wfile.write(error_response)
                    self.wfile.flush()
                except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                    logger.debug(f"Connection error sending error response: {e}")
                except Exception as e:
                    logger.error(f"Failed to send error response: {e}", exc_info=True)

        def _run_with_timeout(self, func: Callable[[], Any], timeout: float = 5.0, default_value: Any = None) -> Any:
            """
            Run a function with a timeout using ThreadPoolExecutor.

            Args:
                func: Function to execute
                timeout: Maximum time to wait in seconds (default: 5.0)
                default_value: Value to return if timeout occurs (default: None)

            Returns:
                Function result or default_value if timeout
            """
            try:
                with ThreadPoolExecutor(max_workers=1) as executor:
                    future = executor.submit(func)
                    return future.result(timeout=timeout)
            except FutureTimeoutError:
                logger.warning(f"Health check operation timed out after {timeout}s")
                return default_value
            except Exception as e:
                logger.error(f"Error in health check operation: {e}")
                raise

        def serve_health(self):
            """
            Serve health check endpoint with comprehensive system checks.

            Performs health checks on CPU, memory, disk, system uptime, and temperature.
            Returns appropriate HTTP status codes:
            - 200 OK: All systems healthy
            - 503 Service Unavailable: System degraded (high resource usage)
            - 500 Internal Server Error: Critical failure

            Returns JSON with detailed status of each component.
            """
            try:
                import time
                import psutil
                from datetime import datetime

                health_status = 'ok'
                checks = {}
                issues = []

                if self.monitors.get('cpu'):
                    try:
                        cpu_percent = self._run_with_timeout(
                            lambda: self.monitors['cpu'].get_cpu_percent(interval=0.1),
                            timeout=5.0,
                            default_value=None
                        )
                        if cpu_percent is None:
                            checks['cpu'] = {'status': 'error', 'error': 'Health check timed out'}
                            issues.append('CPU monitor timed out')
                            health_status = 'error'
                        else:
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

                if self.monitors.get('memory'):
                    try:
                        memory_result = self._run_with_timeout(
                            lambda: (self.monitors['memory'].get_memory_percent(), self.monitors['memory'].get_memory_info()),
                            timeout=5.0,
                            default_value=None
                        )
                        if memory_result is None:
                            checks['memory'] = {'status': 'error', 'error': 'Health check timed out'}
                            issues.append('Memory monitor timed out')
                            health_status = 'error'
                        else:
                            memory_percent, memory_info = memory_result
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

                if self.monitors.get('disk'):
                    try:
                        disk_usage = self._run_with_timeout(
                            lambda: self.monitors['disk'].get_total_disk_usage(),
                            timeout=5.0,
                            default_value=None
                        )
                        if disk_usage is None:
                            checks['disk'] = {'status': 'error', 'error': 'Health check timed out'}
                            issues.append('Disk monitor timed out')
                            health_status = 'error'
                        else:
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
                    system_result = self._run_with_timeout(
                        lambda: psutil.boot_time(),
                        timeout=2.0,
                        default_value=None
                    )
                    if system_result is None:
                        checks['system'] = {'status': 'error', 'error': 'Health check timed out'}
                        issues.append('System info check timed out')
                        if health_status == 'ok':
                            health_status = 'degraded'
                    else:
                        boot_time = system_result
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

                if self.monitors.get('sensors'):
                    try:
                        sensors = self.monitors['sensors']
                        temp_result = self._run_with_timeout(
                            lambda: sensors.get_cpu_temperature() if sensors.is_available() else None,
                            timeout=3.0,
                            default_value=None
                        )
                        if sensors.is_available():
                            cpu_temp = temp_result
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
                self.send_header('Content-length', str(len(response)))
                self._add_security_headers()
                self._add_cors_headers(allowed_origin='*')
                self.end_headers()
                self.wfile.write(response)
            except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                logger.debug(f"Connection error serving health: {e}")
            except Exception as e:
                logger.error(f"Error serving health: {e}", exc_info=True)
                try:
                    error_response = json.dumps({
                        'status': 'error',
                        'error': str(e),
                        'timestamp': time.time()
                    }).encode('utf-8')
                    self.send_response(500)
                    self.send_header('Content-type', 'application/json')
                    self.send_header('Content-length', str(len(error_response)))
                    self._add_security_headers()
                    self._add_cors_headers(allowed_origin='*')
                    self.end_headers()
                    self.wfile.write(error_response)
                except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
                    logger.debug(f"Connection error sending health error response: {e}")

        def get_system_stats(self) -> Dict:
            """
            Collect current system statistics with caching for performance.

            Gathers data from all available monitors (CPU, memory, disk, network, GPU, sensors).
            Uses caching to avoid expensive operations on every request.

            Returns:
                Dictionary containing system statistics from all monitors
                Always returns a valid dict, even if some monitors fail.
            """
            global _process_cache, _network_cache, _gpu_cache, _disk_cache, _cpu_cache
            global _process_cache_lock, _network_cache_lock, _gpu_cache_lock, _disk_cache_lock, _cpu_cache_lock

            stats = {}
            current_time = time.time()

            try:
                if self.monitors.get('system'):
                    try:
                        stats['system'] = self.monitors['system'].get_system_info()
                        stats['uptime'] = self.monitors['system'].get_uptime()
                    except Exception as e:
                        logger.warning(f"Error getting system info: {e}")

                if self.monitors.get('cpu'):
                    try:
                        cpu_mon = self.monitors['cpu']
                        with _cpu_cache_lock:
                            time_since_cpu_update = current_time - _cpu_cache['last_update']
                            if time_since_cpu_update >= _cpu_cache['cache_interval']:
                                stats['cpu'] = cpu_mon.get_cpu_info()
                                stats['cpu']['cpu_percent'] = cpu_mon.get_cpu_percent(interval=0)
                                _cpu_cache['data'] = stats['cpu']
                                _cpu_cache['last_update'] = current_time
                            else:
                                stats['cpu'] = _cpu_cache['data']
                    except Exception as e:
                        logger.warning(f"Error getting CPU info: {e}")
                        with _cpu_cache_lock:
                            stats['cpu'] = _cpu_cache.get('data', {})

                if self.monitors.get('memory'):
                    try:
                        stats['memory'] = self.monitors['memory'].get_memory_stats()
                    except Exception as e:
                        logger.warning(f"Error getting memory info: {e}")

                if self.monitors.get('disk'):
                    try:
                        with _disk_cache_lock:
                            time_since_disk_update = current_time - _disk_cache['last_update']
                            if time_since_disk_update >= _disk_cache['cache_interval']:
                                disk_data = self.monitors['disk'].get_total_disk_usage()
                                _disk_cache['data'] = {'total': disk_data}
                                _disk_cache['last_update'] = current_time
                                stats['disk'] = _disk_cache['data']
                            else:
                                stats['disk'] = _disk_cache['data']
                    except Exception as e:
                        logger.warning(f"Error getting disk info: {e}")
                        with _disk_cache_lock:
                            stats['disk'] = _disk_cache.get('data', {})

                if self.monitors.get('gpu'):
                    try:
                        gpu_mon = self.monitors['gpu']
                        if gpu_mon.nvidia_smi_available and gpu_mon.get_gpu_count() > 0:
                            with _gpu_cache_lock:
                                time_since_gpu_update = current_time - _gpu_cache['last_update']
                                if time_since_gpu_update >= _gpu_cache['cache_interval']:
                                    gpu_data = gpu_mon.get_gpu_comprehensive(0)
                                    _gpu_cache['data'] = gpu_data
                                    _gpu_cache['last_update'] = current_time
                                    stats['gpu'] = _gpu_cache['data']
                                else:
                                    stats['gpu'] = _gpu_cache['data']
                    except Exception as e:
                        logger.warning(f"Error getting GPU info: {e}")
                        with _gpu_cache_lock:
                            stats['gpu'] = _gpu_cache.get('data', {})

                if self.monitors.get('sensors'):
                    try:
                        sensors = self.monitors['sensors']
                        if sensors.is_available():
                            temp = sensors.get_cpu_temperature()
                            if temp is not None:
                                stats['temperature'] = temp
                    except Exception as e:
                        logger.warning(f"Error getting temperature: {e}")

                if self.monitors.get('network'):
                    try:
                        with _network_cache_lock:
                            time_since_network_update = current_time - _network_cache['last_update']
                            if time_since_network_update >= _network_cache['cache_interval']:
                                current_io = psutil.net_io_counters()
                                if current_io is None:
                                    stats['network'] = _network_cache.get('data', {})
                                else:
                                    if _network_cache['last_io'] is not None and _network_cache['last_io_time'] > 0:
                                        time_diff = current_time - _network_cache['last_io_time']
                                        if time_diff > 0:
                                            bandwidth = {
                                                'interface_name': 'system-wide',
                                                'bytes_sent_per_sec': (current_io.bytes_sent - _network_cache['last_io'].bytes_sent) / time_diff,
                                                'bytes_recv_per_sec': (current_io.bytes_recv - _network_cache['last_io'].bytes_recv) / time_diff,
                                                'packets_sent_per_sec': (current_io.packets_sent - _network_cache['last_io'].packets_sent) / time_diff,
                                                'packets_recv_per_sec': (current_io.packets_recv - _network_cache['last_io'].packets_recv) / time_diff,
                                            }
                                            _network_cache['data'] = bandwidth
                                            _network_cache['last_update'] = current_time
                                            stats['network'] = bandwidth
                                        else:
                                            stats['network'] = _network_cache.get('data', {})
                                    else:
                                        bandwidth = {
                                            'interface_name': 'system-wide',
                                            'bytes_sent_per_sec': 0,
                                            'bytes_recv_per_sec': 0,
                                            'packets_sent_per_sec': 0,
                                            'packets_recv_per_sec': 0,
                                        }
                                        _network_cache['data'] = bandwidth
                                        stats['network'] = bandwidth

                                    _network_cache['last_io'] = current_io
                                    _network_cache['last_io_time'] = current_time
                            else:
                                stats['network'] = _network_cache['data']
                    except Exception as e:
                        logger.warning(f"Error getting network info: {e}")
                        with _network_cache_lock:
                            stats['network'] = _network_cache.get('data', {})

                if self.monitors.get('system'):
                    try:
                        system_mon = self.monitors['system']
                        stats['processes'] = system_mon.get_process_count()

                        with _process_cache_lock:
                            time_since_process_update = current_time - _process_cache['last_update']
                            if time_since_process_update >= _process_cache['cache_interval']:
                                try:
                                    _process_cache['top_cpu'] = system_mon.get_top_processes(limit=5, sort_by='cpu')
                                    _process_cache['top_memory'] = system_mon.get_top_processes(limit=5, sort_by='memory')
                                    _process_cache['last_update'] = current_time
                                except Exception as e:
                                    logger.debug(f"Error updating process cache: {e}")

                            stats['top_processes_cpu'] = _process_cache['top_cpu']
                            stats['top_processes_memory'] = _process_cache['top_memory']
                    except Exception as e:
                        logger.warning(f"Error getting process info: {e}")
                        stats['processes'] = {'total': 0}
                        with _process_cache_lock:
                            stats['top_processes_cpu'] = _process_cache.get('top_cpu', [])
                            stats['top_processes_memory'] = _process_cache.get('top_memory', [])
                else:
                    stats['processes'] = {'total': 0}
                    stats['top_processes_cpu'] = []
                    stats['top_processes_memory'] = []

            except Exception as e:
                logger.error(f"Critical error in get_system_stats: {e}", exc_info=True)
                if not stats:
                    stats = {
                        'system': {},
                        'uptime': {},
                        'cpu': {},
                        'memory': {},
                        'disk': {},
                        'network': {},
                        'processes': {'total': 0},
                        'top_processes_cpu': [],
                        'top_processes_memory': []
                    }

            return stats

    return WebRequestHandler


WebRequestHandler = create_request_handler()


def get_local_ip_addresses():
    """
    Get local network IP addresses.

    Retrieves all non-loopback, non-link-local IP addresses for the local machine.
    Attempts to determine the primary IP by connecting to an external address.

    Returns:
        List of IP address strings
    """
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


def _run_server(port: int, host: str) -> None:
    """
    Internal function to run the HTTP server in a thread.

    Args:
        port (int): Port to listen on
        host (str): Host address to bind to

    This function is called by the server thread and runs the server's main loop.
    """
    server_address = (host, port)

    try:
        _state.httpd = HTTPServer(server_address, WebRequestHandler)
    except (OSError, socket.error) as e:
        error_str = str(e).lower()
        is_port_in_use = (
            port == 8080 and (
                "address already in use" in error_str or
                "address already in use" in str(e) or
                getattr(e, 'errno', None) in (98, 10048) or
                "already in use" in error_str
            )
        )

        if is_port_in_use:
            logger.warning(f"Port {port} is already in use, trying port 8081 instead")
            port = 8081
            server_address = (host, port)
            try:
                handler_class = create_request_handler({
                    'cpu': _state.cpu_monitor,
                    'memory': _state.memory_monitor,
                    'system': _state.system_monitor,
                    'gpu': _state.gpu_monitor,
                    'disk': _state.disk_monitor,
                    'network': _state.network_monitor,
                    'sensors': _state.sensors_monitor,
                })
                _state.httpd = HTTPServer(server_address, handler_class)
                logger.info(f"Successfully bound to port {port}")
            except (OSError, socket.error) as e2:
                logger.error(f"Failed to bind to port {port}: {e2}")
                if _state.crash_webhook:
                    try:
                        _state.crash_webhook.send_embed(
                            title='Web Server Port Binding Error',
                            description=f'Failed to bind web server to port {port}',
                            fields=[
                                {'name': 'Error Type', 'value': type(e2).__name__, 'inline': True},
                                {'name': 'Error Message', 'value': str(e2)[:500], 'inline': False},
                                {'name': 'Port', 'value': str(port), 'inline': True},
                            ],
                            color=0x000000,
                            footer='Server Monitor - Web Server Error',
                            timestamp=True
                        )
                    except Exception as webhook_error:
                        logger.warning(f"Failed to send port binding error to crash webhook: {webhook_error}")
                raise
        else:
            raise

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
        _state.httpd.serve_forever()
    except Exception as e:
        logger.error(f"Web server error: {e}")
        if _state.crash_webhook:
            try:
                import traceback
                traceback_text = traceback.format_exc()
                if len(traceback_text) > 1000:
                    traceback_text = traceback_text[:1000] + "... (truncated)"

                crash_webhook.send_embed(
                    title='Web Server Error',
                    description=f'An error occurred in the web server',
                    fields=[
                        {'name': 'Error Type', 'value': type(e).__name__, 'inline': True},
                        {'name': 'Error Message', 'value': str(e)[:500], 'inline': False},
                        {'name': 'Traceback', 'value': f"```\n{traceback_text}\n```", 'inline': False},
                    ],
                    color=0x000000,
                    footer='Server Monitor - Web Server Error',
                    timestamp=True
                )
            except Exception as webhook_error:
                logger.warning(f"Failed to send web server error to crash webhook: {webhook_error}")


def start_server(port: int = 8080, host: str = '0.0.0.0') -> None:
    """
    Start the web server in a background thread.

    Args:
        port (int): Port to listen on (default: 8080)
        host (str): Host to bind to (default: 0.0.0.0)
    """
    if _state.server_thread and _state.server_thread.is_alive():
        logger.warning("Web server is already running")
        return

    _state.server_thread = threading.Thread(target=_run_server, args=(port, host), daemon=True)
    _state.server_thread.start()
    logger.info("Web server thread started")


def stop_server(timeout: float = 5.0) -> None:
    """
    Stop the web server with timeout to prevent blocking.

    Args:
        timeout (float): Maximum time to wait for server shutdown in seconds (default: 5.0)
    """
    import time
    import threading

    if _state.httpd:
        try:
            shutdown_complete = threading.Event()
            shutdown_error = [None]

            def shutdown_thread():
                try:
                    _state.httpd.shutdown()
                    shutdown_complete.set()
                except Exception as e:
                    shutdown_error[0] = e
                    shutdown_complete.set()

            shutdown_thread_obj = threading.Thread(target=shutdown_thread, daemon=True)
            shutdown_thread_obj.start()

            if shutdown_complete.wait(timeout=timeout):
                if shutdown_error[0]:
                    logger.warning(f"Error stopping web server: {shutdown_error[0]}")
                else:
                    logger.info("Web server stopped")
            else:
                logger.warning(f"Web server shutdown timed out after {timeout}s, forcing exit")
        except KeyboardInterrupt:
            logger.warning("Web server shutdown interrupted by user")
        except Exception as e:
            logger.warning(f"Error stopping web server: {e}")
        finally:
            _state.httpd = None

    _state.server_thread = None

