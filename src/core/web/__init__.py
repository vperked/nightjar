"""
Local HTTP dashboard and metrics API.
"""

from core.web.server import (
    WebServerState,
    set_monitors,
    set_crash_webhook,
    create_request_handler,
    WebRequestHandler,
    get_local_ip_addresses,
    start_server,
    stop_server,
)

__all__ = [
    'WebRequestHandler',
    'WebServerState',
    'create_request_handler',
    'get_local_ip_addresses',
    'set_crash_webhook',
    'set_monitors',
    'start_server',
    'stop_server',
]
