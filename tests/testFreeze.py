"""Tests for freeze detection metric staleness and threshold handling."""

import time

import pytest

from core.freeze import FreezeMonitor


@pytest.fixture
def monitor():
    return FreezeMonitor(check_interval=1.0)


def test_first_update_sets_metric_timestamp(monitor):
    assert monitor.last_metric_update is None

    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)

    assert monitor.last_metric_update is not None


def test_unchanged_metrics_refresh_timestamp_when_idle(monitor):
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)

    assert monitor.last_metric_update >= first_update


def test_unchanged_busy_metrics_do_not_refresh_timestamp(monitor):
    monitor.update_metrics(cpu_percent=55.0, memory_percent=20.0)
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(cpu_percent=55.0, memory_percent=20.0)

    assert monitor.last_metric_update == first_update


def test_changed_cpu_refreshes_timestamp(monitor):
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(cpu_percent=55.0, memory_percent=20.0)

    assert monitor.last_metric_update > first_update


def test_changed_memory_refreshes_timestamp(monitor):
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(cpu_percent=10.0, memory_percent=44.0)

    assert monitor.last_metric_update > first_update


def test_changed_disk_io_refreshes_timestamp(monitor):
    monitor.update_metrics(10.0, 20.0, disk_io={'read_bytes': 1, 'write_bytes': 1})
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(10.0, 20.0, disk_io={'read_bytes': 500, 'write_bytes': 1})

    assert monitor.last_metric_update > first_update


def test_changed_network_io_refreshes_timestamp(monitor):
    monitor.update_metrics(10.0, 20.0, network_io={'bytes_sent': 1, 'bytes_recv': 1})
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(10.0, 20.0, network_io={'bytes_sent': 1, 'bytes_recv': 900})

    assert monitor.last_metric_update > first_update


def test_sub_threshold_change_is_idle_refresh(monitor):
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)
    first_update = monitor.last_metric_update

    time.sleep(0.01)
    monitor.update_metrics(cpu_percent=10.05, memory_percent=20.05)

    assert monitor.last_metric_update >= first_update


def test_stalled_metrics_flag_freeze(monitor):
    monitor.set_freeze_threshold(10.0)
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)
    monitor.last_metric_update = time.time() - 60.0

    status = monitor.check_freeze_status()

    assert status['is_frozen'] is True
    assert any('Metrics stalled' in reason for reason in status['reasons'])


def test_fresh_metrics_do_not_flag_freeze(monitor):
    monitor.set_freeze_threshold(30.0)
    monitor.update_metrics(cpu_percent=10.0, memory_percent=20.0)

    status = monitor.check_freeze_status()

    assert not any('Metrics stalled' in reason for reason in status['reasons'])


def test_heartbeat_timeout_flags_freeze(monitor):
    monitor.set_heartbeat_timeout(20.0)
    monitor.last_heartbeat = time.time() - 300.0

    status = monitor.check_freeze_status()

    assert status['is_frozen'] is True
    assert any('Heartbeat timeout' in reason for reason in status['reasons'])


def test_freeze_threshold_has_lower_bound(monitor):
    monitor.set_freeze_threshold(1.0)

    assert monitor.freeze_threshold == 10.0
