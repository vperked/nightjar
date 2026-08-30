"""Tests for kernel reboot detection across monitor restarts."""

import json
from pathlib import Path

from core.kernel.monitor import KernelMonitor


def test_pending_reboot_alert_from_persisted_boot_time(tmp_path, monkeypatch):
    persistence = tmp_path / 'kernel_state.json'
    persistence.write_text(json.dumps({
        'boot_time': 1000.0,
        'last_reported_errors': {},
        'last_alert_time': {},
        'last_edac_counts': {},
        'saved_at': 2000.0,
    }))

    monkeypatch.setattr(KernelMonitor, '_get_boot_time', lambda self: 5000.0)
    monitor = KernelMonitor(persistence_file=str(persistence))

    assert monitor._pending_reboot_alert is not None
    assert monitor._pending_reboot_alert['previous_boot_time'] == 1000.0
    assert monitor._pending_reboot_alert['current_boot_time'] == 5000.0

    reboot = monitor._check_unexpected_reboot()
    assert reboot is not None
    assert reboot['previous_boot_time'] == 1000.0
    assert monitor._pending_reboot_alert is None

    assert monitor._check_unexpected_reboot() is None


def test_journalctl_crash_check_runs_once(monkeypatch):
    monitor = KernelMonitor.__new__(KernelMonitor)
    monitor.is_linux = True
    monitor.detected_crashes = set()
    calls = []

    class Result:
        returncode = 0
        stdout = '{"MESSAGE": "kernel panic on boot", "__REALTIME_TIMESTAMP": "1"}' + '\n'

    def fake_run(cmd, **kwargs):
        calls.append(cmd)
        return Result()

    monkeypatch.setattr('core.kernel.monitor.subprocess.run', fake_run)

    crashes = monitor._check_journalctl(since_minutes=60)

    assert len(calls) == 1
    assert '--grep' in calls[0]
    assert any(crash['keyword'] == 'kernel panic' for crash in crashes)
