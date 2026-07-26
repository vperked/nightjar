"""
Typed exception hierarchy and error helpers.
"""

from core.errors.exceptions import (
    MonitorError,
    ConfigurationError,
    MonitorInitializationError,
    MonitorExecutionError,
    WebhookError,
    log_and_continue,
    handle_connection_errors,
)

__all__ = [
    'ConfigurationError',
    'MonitorError',
    'MonitorExecutionError',
    'MonitorInitializationError',
    'WebhookError',
    'handle_connection_errors',
    'log_and_continue',
]
