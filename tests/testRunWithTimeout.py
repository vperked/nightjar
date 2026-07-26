"""Tests for the shared timeout executor used by the monitoring loop."""

import threading
import time

import pytest

import main


def test_returns_function_result():
    assert main.run_with_timeout(lambda: 42, timeout=5.0) == 42


def test_returns_default_on_timeout():
    stop = threading.Event()

    def slow():
        stop.wait(30)
        return 'late'

    try:
        result = main.run_with_timeout(
            slow,
            timeout=0.1,
            default_value='fallback',
            operation_name='slow op',
        )
        assert result == 'fallback'
    finally:
        stop.set()


def test_propagates_exceptions():
    def boom():
        raise ValueError('bad')

    with pytest.raises(ValueError, match='bad'):
        main.run_with_timeout(boom, timeout=5.0)


def test_worker_pool_is_bounded_under_repeated_timeouts():
    stop = threading.Event()

    def slow():
        stop.wait(30)

    before = threading.active_count()
    try:
        for _ in range(20):
            main.run_with_timeout(slow, timeout=0.02, default_value=None)

        growth = threading.active_count() - before
        assert growth <= main.TIMEOUT_EXECUTOR_WORKERS
    finally:
        stop.set()
        time.sleep(0.1)


def test_shared_executor_is_reused():
    first = main._TIMEOUT_EXECUTOR
    main.run_with_timeout(lambda: 1, timeout=5.0)

    assert main._TIMEOUT_EXECUTOR is first
