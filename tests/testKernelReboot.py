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
