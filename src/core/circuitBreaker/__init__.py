"""
Thread-safe circuit breaker for external services.
"""

from core.circuitBreaker.breaker import (
    CircuitState,
    CircuitBreakerOpenError,
    CircuitBreaker,
)

__all__ = [
    'CircuitBreaker',
    'CircuitBreakerOpenError',
    'CircuitState',
]
