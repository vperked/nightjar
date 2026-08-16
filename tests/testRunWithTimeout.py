"""Tests for timeout-wrapped monitoring helpers."""

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


def test_remains_usable_after_repeated_timeouts():
    stop = threading.Event()

    def slow():
        stop.wait(30)

    try:
        for _ in range(main.TIMEOUT_EXECUTOR_WORKERS * 2 + 2):
            main.run_with_timeout(slow, timeout=0.02, default_value=None)

        assert main.run_with_timeout(lambda: 'ok', timeout=1.0, default_value=None) == 'ok'
    finally:
        stop.set()
        time.sleep(0.05)


def test_timeout_workers_are_daemon_threads():
    stop = threading.Event()
    worker_thread = {}

    def slow():
        worker_thread['thread'] = threading.current_thread()
        stop.wait(30)

    try:
        main.run_with_timeout(slow, timeout=0.05, default_value=None)
        assert worker_thread['thread'].daemon is True
    finally:
        stop.set()


def test_send_webhook_uses_timeout_wrapper():
    calls = []

    def hang():
        calls.append('start')
        time.sleep(30)
        return True

    started = time.time()
    result = main.send_webhook(hang, operation_name='hanging webhook', timeout=0.05)
    elapsed = time.time() - started

    assert result is False
    assert elapsed < 2.0
    assert calls == ['start']
