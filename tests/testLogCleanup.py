"""Tests for log trimming behaviour, including data retention guarantees."""

import pytest

from core.logs import LogMonitor


@pytest.fixture
def log_monitor(tmp_path):
    monitor = LogMonitor(log_file=str(tmp_path / 'app.log'), threshold_mb=0.001)
    monitor.system_log_paths = [str(tmp_path / 'syslog')]
    monitor.journal_log_path = None
    return monitor


def test_oversized_log_keeps_most_recent_data(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    line_count = 5000
    log_path.write_bytes(b''.join(f'line-{i}\n'.encode() for i in range(line_count)))
    original_size = log_path.stat().st_size

    assert log_monitor._cleanup_regular_log(log_path, force=False) is True

    new_size = log_path.stat().st_size
    content = log_path.read_bytes()

    assert new_size < original_size
    assert content.endswith(f'line-{line_count - 1}\n'.encode())
    assert b'line-0\n' not in content


def test_system_log_is_not_emptied_without_force(tmp_path, log_monitor):
    syslog = tmp_path / 'syslog'
    syslog.write_bytes(b'x' * 200_000)

    assert log_monitor._cleanup_regular_log(syslog, force=False) is True

    assert syslog.stat().st_size > 0


def test_trimmed_size_is_near_threshold(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'y' * 500_000)

    log_monitor._cleanup_regular_log(log_path, force=False)

    assert log_path.stat().st_size == int(log_monitor.threshold_bytes)


def test_trim_preserves_inode(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'z' * 300_000)
    original_inode = log_path.stat().st_ino

    log_monitor._cleanup_regular_log(log_path, force=False)

    assert log_path.stat().st_ino == original_inode


def test_log_below_threshold_is_untouched(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'small payload')

    assert log_monitor._cleanup_regular_log(log_path, force=False) is True
    assert log_path.read_bytes() == b'small payload'


def test_force_empties_log(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'w' * 300_000)

    assert log_monitor._cleanup_regular_log(log_path, force=True) is True
    assert log_path.stat().st_size == 0


def test_empty_file_is_a_noop(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'')

    assert log_monitor._cleanup_regular_log(log_path, force=True) is True
    assert log_path.stat().st_size == 0


def test_missing_file_returns_false(tmp_path, log_monitor):
    assert log_monitor._cleanup_regular_log(tmp_path / 'missing.log', force=False) is False


def test_cleanup_log_skips_when_below_threshold(tmp_path, log_monitor):
    log_path = tmp_path / 'app.log'
    log_path.write_bytes(b'tiny')

    log_info = {'path': str(log_path), 'size_bytes': 4}

    assert log_monitor.cleanup_log(log_info) is True
    assert log_path.read_bytes() == b'tiny'


def test_cleanup_log_handles_empty_info(log_monitor):
    assert log_monitor.cleanup_log({}) is False
