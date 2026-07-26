"""Tests for circuit breaker state transitions and thread safety."""

import threading

import pytest

from core.circuitBreaker import (
    CircuitBreaker,
    CircuitBreakerOpenError,
    CircuitState,
)


def _boom():
    raise RuntimeError('service down')


def test_opens_after_failure_threshold():
    breaker = CircuitBreaker(failure_threshold=3, timeout=60.0)

    for _ in range(3):
        with pytest.raises(RuntimeError):
            breaker.call(_boom)

    assert breaker.is_open() is True


def test_stays_closed_below_threshold():
    breaker = CircuitBreaker(failure_threshold=3, timeout=60.0)

    for _ in range(2):
        with pytest.raises(RuntimeError):
            breaker.call(_boom)

    assert breaker.is_open() is False


def test_open_breaker_rejects_calls_without_invoking_function():
    breaker = CircuitBreaker(failure_threshold=1, timeout=60.0)
    with pytest.raises(RuntimeError):
        breaker.call(_boom)

    calls = []

    with pytest.raises(CircuitBreakerOpenError):
        breaker.call(lambda: calls.append(1))

    assert calls == []


def test_half_open_after_timeout_then_closes_on_successes():
    breaker = CircuitBreaker(failure_threshold=1, timeout=0.0, success_threshold=2)
    with pytest.raises(RuntimeError):
        breaker.call(_boom)
    assert breaker.is_open() is True

    assert breaker.call(lambda: 'ok') == 'ok'
    assert breaker.get_state() is CircuitState.HALF_OPEN

    assert breaker.call(lambda: 'ok') == 'ok'
    assert breaker.get_state() is CircuitState.CLOSED


def test_half_open_failure_reopens():
    breaker = CircuitBreaker(failure_threshold=1, timeout=0.0, success_threshold=2)
    with pytest.raises(RuntimeError):
        breaker.call(_boom)

    with pytest.raises(RuntimeError):
        breaker.call(_boom)

    assert breaker.get_state() is CircuitState.OPEN


def test_success_resets_failure_count_while_closed():
    breaker = CircuitBreaker(failure_threshold=3, timeout=60.0)
    with pytest.raises(RuntimeError):
        breaker.call(_boom)

    breaker.call(lambda: 'ok')

    assert breaker.failure_count == 0


def test_reset_returns_to_closed():
    breaker = CircuitBreaker(failure_threshold=1, timeout=60.0)
    with pytest.raises(RuntimeError):
        breaker.call(_boom)

    breaker.reset()

    assert breaker.get_state() is CircuitState.CLOSED
    assert breaker.failure_count == 0


def test_concurrent_failures_do_not_corrupt_state():
    breaker = CircuitBreaker(failure_threshold=50, timeout=60.0)
    barrier = threading.Barrier(10)

    def worker():
        barrier.wait()
        for _ in range(20):
            try:
                breaker.call(_boom)
            except (RuntimeError, CircuitBreakerOpenError):
                pass

    threads = [threading.Thread(target=worker) for _ in range(10)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=30)

    assert all(not thread.is_alive() for thread in threads)
    assert breaker.get_state() is CircuitState.OPEN
    assert breaker.failure_count >= 50


def test_concurrent_successes_keep_breaker_closed():
    breaker = CircuitBreaker(failure_threshold=5, timeout=60.0)
    results = []
    barrier = threading.Barrier(8)

    def worker():
        barrier.wait()
        for _ in range(25):
            results.append(breaker.call(lambda: 1))

    threads = [threading.Thread(target=worker) for _ in range(8)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=30)

    assert len(results) == 200
    assert breaker.get_state() is CircuitState.CLOSED
