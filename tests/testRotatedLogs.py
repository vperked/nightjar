"""Tests for detection and removal of rotated log archives such as syslog.1."""

import os
import time

import pytest

from core.logs import LogMonitor

SECONDS_PER_DAY = 86400


@pytest.fixture
def log_monitor(tmp_path):
    monitor = LogMonitor(log_file=str(tmp_path / 'app.log'), threshold_mb=0.001)
    monitor.journal_log_path = None
    monitor.rotated_log_retention_days = 7.0
    return monitor


def _age(path, days):
    stamp = time.time() - days * SECONDS_PER_DAY
    os.utime(path, (stamp, stamp))


@pytest.mark.parametrize('name', [
    'syslog.1',
    'kern.log.1',
    'syslog.2.gz',
    'kern.log.4.gz',
    'auth.log.2.bz2',
    'daemon.log.old',
    'syslog-20240115',
    'access.log.10',
    'messages.1.xz',
    'server_monitor.log.5',
])
def test_rotated_names_are_detected(name):
    assert LogMonitor.is_rotated_log(name) is True


@pytest.mark.parametrize('name', [
    'syslog',
    'kern.log',
    'auth.log',
    'messages',
    'daemon.log',
    'lastlog',
    'wtmp',
    'app.log',
])
def test_live_logs_are_not_treated_as_rotated(name):
    assert LogMonitor.is_rotated_log(name) is False


def test_discovery_finds_rotated_logs_only(tmp_path, log_monitor):
    (tmp_path / 'syslog').write_text('live')
    (tmp_path / 'syslog.1').write_text('rotated')
    (tmp_path / 'kern.log').write_text('live')
    (tmp_path / 'kern.log.1').write_text('rotated')
    (tmp_path / 'syslog.2.gz').write_bytes(b'\x1f\x8b compressed')

    found = {os.path.basename(entry['path']) for entry in log_monitor.get_rotated_log_files(str(tmp_path))}

    assert found == {'syslog.1', 'kern.log.1', 'syslog.2.gz'}


def test_discovery_skips_journal_directory(tmp_path, log_monitor):
    journal = tmp_path / 'journal'
    journal.mkdir()
    (journal / 'system@0001.journal~').write_text('x')
    (journal / 'archived.journal.1').write_text('x')
    (tmp_path / 'syslog.1').write_text('rotated')

    found = [entry['path'] for entry in log_monitor.get_rotated_log_files(str(tmp_path))]

    assert found == [str(tmp_path / 'syslog.1')]


def test_discovery_reports_age(tmp_path, log_monitor):
    rotated = tmp_path / 'syslog.1'
    rotated.write_text('rotated')
    _age(rotated, 10)

    entry = log_monitor.get_rotated_log_files(str(tmp_path))[0]

    assert entry['rotated'] is True
    assert entry['age_days'] == pytest.approx(10, abs=0.1)


def test_aged_rotated_log_is_deleted(tmp_path, log_monitor):
    rotated = tmp_path / 'syslog.1'
    rotated.write_text('small but old')
    _age(rotated, 30)

    stats = log_monitor.cleanup_rotated_logs(log_dir=str(tmp_path))

    assert not rotated.exists()
    assert stats['files_removed'] == 1


def test_recent_small_rotated_log_is_kept(tmp_path, log_monitor):
    rotated = tmp_path / 'kern.log.1'
    rotated.write_text('recent')
    _age(rotated, 1)

    stats = log_monitor.cleanup_rotated_logs(log_dir=str(tmp_path))

    assert rotated.exists()
    assert stats['files_removed'] == 0


def test_oversized_recent_rotated_log_is_deleted(tmp_path, log_monitor):
    rotated = tmp_path / 'kern.log.1'
    rotated.write_bytes(b'x' * 200_000)
    _age(rotated, 1)

    stats = log_monitor.cleanup_rotated_logs(log_dir=str(tmp_path))

    assert not rotated.exists()
    assert stats['total_size_cleared'] == 200_000


def test_force_removes_every_rotated_archive(tmp_path, log_monitor):
    for name in ('syslog.1', 'kern.log.1', 'syslog.2.gz'):
        path = tmp_path / name
        path.write_text('data')
        _age(path, 0)

    stats = log_monitor.cleanup_rotated_logs(force=True, log_dir=str(tmp_path))

    assert stats['files_removed'] == 3
    assert list(tmp_path.iterdir()) == []


def test_force_does_not_touch_live_logs(tmp_path, log_monitor):
    live = tmp_path / 'syslog'
    live.write_text('live data')
    (tmp_path / 'syslog.1').write_text('rotated')

    log_monitor.cleanup_rotated_logs(force=True, log_dir=str(tmp_path))

    assert live.exists()
    assert live.read_text() == 'live data'


def test_cleanup_log_deletes_rotated_instead_of_trimming(tmp_path, log_monitor):
    rotated = tmp_path / 'syslog.1'
    rotated.write_bytes(b'y' * 500_000)

    log_info = {'path': str(rotated), 'size_bytes': 500_000}

    assert log_monitor.cleanup_log(log_info) is True
    assert not rotated.exists()


def test_cleanup_log_keeps_recent_small_rotated_log(tmp_path, log_monitor):
    rotated = tmp_path / 'syslog.1'
    rotated.write_text('tiny')
    _age(rotated, 0)

    log_info = {'path': str(rotated), 'size_bytes': 4, 'age_days': 0.0}

    assert log_monitor.cleanup_log(log_info) is True
    assert rotated.exists()


def test_compressed_archive_is_removed_not_corrupted(tmp_path, log_monitor):
    archive = tmp_path / 'syslog.2.gz'
    archive.write_bytes(b'\x1f\x8b' + b'z' * 300_000)

    log_info = {'path': str(archive), 'size_bytes': archive.stat().st_size}
    log_monitor.cleanup_log(log_info)

    assert not archive.exists()


def test_missing_rotated_file_is_not_an_error(tmp_path, log_monitor):
    assert log_monitor.delete_rotated_log(str(tmp_path / 'gone.1')) is True


def test_retention_days_are_configurable(monkeypatch, tmp_path):
    monkeypatch.setenv('ROTATED_LOG_RETENTION_DAYS', '1')
    from core.config import settings as config
    config._env_cache.clear()

    monitor = LogMonitor(log_file=str(tmp_path / 'app.log'), threshold_mb=100.0)
    rotated = tmp_path / 'syslog.1'
    rotated.write_text('two days old')
    _age(rotated, 2)

    stats = monitor.cleanup_rotated_logs(log_dir=str(tmp_path))

    assert monitor.rotated_log_retention_days == 1.0
    assert stats['files_removed'] == 1


def test_all_log_sizes_reports_rotated_totals(tmp_path, log_monitor, monkeypatch):
    rotated = tmp_path / 'syslog.1'
    rotated.write_bytes(b'a' * 1000)
    monkeypatch.setattr(
        log_monitor,
        'get_rotated_log_files',
        lambda log_dir='/var/log': [{
            'path': str(rotated),
            'size_bytes': 1000,
            'rotated': True,
            'age_days': 30.0,
        }],
    )

    result = log_monitor.get_all_log_sizes()

    assert result['rotated_total_bytes'] == 1000
    assert any(entry['path'] == str(rotated) for entry in result['exceeded_logs'])
