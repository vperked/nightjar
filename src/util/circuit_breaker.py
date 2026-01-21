"""
Circuit breaker pattern implementation for external service calls.

Prevents cascading failures by stopping calls to failing services
and allowing them to recover before retrying.
"""

import time
from typing import Callable, Optional, Any
from enum import Enum
from logger import get_logger

logger = get_logger('CircuitBreaker')


class CircuitState(Enum):
    """Circuit breaker states."""
    CLOSED = 'CLOSED'
    OPEN = 'OPEN'
    HALF_OPEN = 'HALF_OPEN'


class CircuitBreakerOpenError(Exception):
    """Raised when circuit breaker is open and call is rejected."""
    pass


class CircuitBreaker:
    """
    Circuit breaker implementation to prevent cascading failures.

    Monitors failures and opens the circuit after threshold is reached.
    After timeout period, transitions to HALF_OPEN to test recovery.
    """

    def __init__(self, failure_threshold: int = 5, timeout: float = 60.0,
                 success_threshold: int = 2):
        """
        Initialize circuit breaker.

        Args:
            failure_threshold: Number of failures before opening circuit
            timeout: Time in seconds before attempting recovery (half-open)
            success_threshold: Number of successes needed to close circuit from half-open
        """
        self.failure_threshold = failure_threshold
        self.timeout = timeout
        self.success_threshold = success_threshold
        self.failure_count = 0
        self.success_count = 0
        self.last_failure_time: Optional[float] = None
        self.state = CircuitState.CLOSED

    def call(self, func: Callable, *args, **kwargs) -> Any:
        """
        Execute function call with circuit breaker protection.

        Args:
            func: Function to call
            *args: Positional arguments for function
            **kwargs: Keyword arguments for function

        Returns:
            Function return value

        Raises:
            CircuitBreakerOpenError: If circuit is open
            Exception: Any exception raised by the function
        """
        if self.state == CircuitState.OPEN:
            if self.last_failure_time and (time.time() - self.last_failure_time) > self.timeout:
                logger.info(f"Circuit breaker transitioning to HALF_OPEN after {self.timeout}s timeout")
                self.state = CircuitState.HALF_OPEN
                self.success_count = 0
            else:
                raise CircuitBreakerOpenError(
                    f"Circuit breaker is OPEN. Service unavailable. "
                    f"Last failure: {self.last_failure_time}"
                )

        try:
            result = func(*args, **kwargs)

            if self.state == CircuitState.HALF_OPEN:
                self.success_count += 1
                if self.success_count >= self.success_threshold:
                    logger.info("Circuit breaker transitioning to CLOSED - service recovered")
                    self.state = CircuitState.CLOSED
                    self.failure_count = 0
                    self.success_count = 0
            elif self.state == CircuitState.CLOSED:
                self.failure_count = 0

            return result

        except Exception as e:
            self.failure_count += 1
            self.last_failure_time = time.time()

            if self.state == CircuitState.HALF_OPEN:
                logger.warning("Circuit breaker transitioning back to OPEN - service still failing")
                self.state = CircuitState.OPEN
                self.success_count = 0
            elif self.state == CircuitState.CLOSED:
                if self.failure_count >= self.failure_threshold:
                    logger.warning(
                        f"Circuit breaker opening after {self.failure_count} failures. "
                        f"Service will be unavailable for {self.timeout}s"
                    )
                    self.state = CircuitState.OPEN

            raise

    def reset(self):
        """Manually reset circuit breaker to CLOSED state."""
        logger.info("Circuit breaker manually reset to CLOSED")
        self.state = CircuitState.CLOSED
        self.failure_count = 0
        self.success_count = 0
        self.last_failure_time = None

    def get_state(self) -> CircuitState:
        """Get current circuit breaker state."""
        return self.state

    def is_open(self) -> bool:
        """Check if circuit breaker is open."""
        return self.state == CircuitState.OPEN
