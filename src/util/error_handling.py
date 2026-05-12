"""
Standardized error handling and logging utilities.
Provides consistent patterns for exception handling and logging across the codebase.
"""
from typing import TypeVar, Callable, Any
from functools import wraps
from logger import get_logger

logger = get_logger('ErrorHandling')

T = TypeVar('T')


class MonitorError(Exception):
    """Base exception for monitor-related errors."""
    pass


class ConfigurationError(MonitorError):
    """Raised when configuration is invalid."""
    pass


class MonitorInitializationError(MonitorError):
    """Raised when a monitor fails to initialize."""
    pass


class MonitorExecutionError(MonitorError):
    """Raised when a monitor fails during execution."""
    pass


class WebhookError(MonitorError):
    """Raised when webhook operations fail."""
    pass


def log_and_continue(
    error_message: str,
    log_level: str = 'warning',
    return_value: Any = None
) -> Callable:
    """
    Decorator to catch exceptions, log them, and return a default value.

    Args:
        error_message: Message to log when exception occurs
        log_level: Logging level ('error', 'warning', 'debug')
        return_value: Value to return on exception

    Returns:
        Decorator function
    """
    def decorator(func: Callable[..., T]) -> Callable[..., T]:
        @wraps(func)
        def wrapper(*args, **kwargs) -> T:
            try:
                return func(*args, **kwargs)
            except KeyboardInterrupt:
                raise
            except Exception as e:
                log_func = getattr(logger, log_level, logger.warning)
                log_func(f"{error_message}: {e}")
                return return_value
        return wrapper
    return decorator


def handle_connection_errors(func: Callable[..., T]) -> Callable[..., T]:
    """
    Decorator to handle connection-related errors consistently.
    Logs connection errors at debug level (expected transient issues).
    """
    @wraps(func)
    def wrapper(*args, **kwargs) -> T:
        try:
            return func(*args, **kwargs)
        except (ConnectionAbortedError, BrokenPipeError, OSError) as e:
            logger.debug(f"Connection error in {func.__name__}: {e}")
            raise
        except Exception as e:
            logger.warning(f"Unexpected error in {func.__name__}: {e}")
            raise
    return wrapper

