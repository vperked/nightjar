"""
Timeout helpers for long-running monitor and webhook calls.
"""

from typing import Optional, Callable, TypeVar
import threading

from core.observability import Component, get_logger

logger = get_logger(Component.CORE)

T = TypeVar('T')

TIMEOUT_EXECUTOR_WORKERS = 8
DEFAULT_WEBHOOK_TIMEOUT = 10.0

_ACTIVE_TIMEOUT_THREADS = 0
_ACTIVE_TIMEOUT_LOCK = threading.Lock()


def run_with_timeout(
    func: Callable[[], T],
    timeout: float = 5.0,
    default_value: Optional[T] = None,
    operation_name: str = "operation"
) -> Optional[T]:
    """
    Run a function with a timeout on a daemon worker thread.

    Daemon workers are used so timed-out I/O cannot exhaust a shared pool or
    block process exit. If many workers are still alive after timeouts, a
    warning is logged (hang backlog), but new calls still start immediately.

    Args:
        func: Function to execute (callable that takes no arguments)
        timeout: Maximum time to wait in seconds (default: 5.0)
        default_value: Value to return if timeout occurs (default: None)
        operation_name: Name of the operation for logging (default: "operation")

    Returns:
        Function result or default_value if timeout
    """
    global _ACTIVE_TIMEOUT_THREADS

    box: dict = {}
    done = threading.Event()

    def worker() -> None:
        global _ACTIVE_TIMEOUT_THREADS
        try:
            box['result'] = func()
        except BaseException as exc:
            box['error'] = exc
        finally:
            done.set()
            with _ACTIVE_TIMEOUT_LOCK:
                _ACTIVE_TIMEOUT_THREADS = max(0, _ACTIVE_TIMEOUT_THREADS - 1)

    with _ACTIVE_TIMEOUT_LOCK:
        _ACTIVE_TIMEOUT_THREADS += 1
        active = _ACTIVE_TIMEOUT_THREADS

    if active > TIMEOUT_EXECUTOR_WORKERS:
        logger.warning(
            f"{operation_name}: {active} timeout workers still active "
            f"(possible I/O hang backlog)"
        )

    thread = threading.Thread(
        target=worker,
        name=f"monitor-timeout-{operation_name[:48]}",
        daemon=True,
    )
    thread.start()

    if done.wait(timeout):
        if 'error' in box:
            logger.error(f"Error in {operation_name}: {box['error']}")
            raise box['error']
        return box.get('result', default_value)

    logger.warning(f"{operation_name} timed out after {timeout}s, using default value")
    return default_value


def send_webhook(
    func: Callable[[], T],
    operation_name: str,
    timeout: float = DEFAULT_WEBHOOK_TIMEOUT,
    default_value: Optional[T] = False,
) -> Optional[T]:
    """
    Send a Discord webhook call behind run_with_timeout.

    Args:
        func: Zero-argument callable that performs the webhook send
        operation_name: Name used in timeout/error logs
        timeout: Maximum seconds to wait for Discord (default: 10.0)
        default_value: Value returned on timeout/failure scheduling (default: False)

    Returns:
        Webhook call result, or default_value on timeout
    """
    return run_with_timeout(
        func,
        timeout=timeout,
        default_value=default_value,
        operation_name=operation_name,
    )

__all__ = [
    'DEFAULT_WEBHOOK_TIMEOUT',
    'TIMEOUT_EXECUTOR_WORKERS',
    'run_with_timeout',
    'send_webhook',
]
