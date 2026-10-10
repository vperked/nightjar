"""Tests for stalled torrent detection and recovery helpers."""

from unittest.mock import MagicMock

import pytest

from core.qbittorrent import (
    QBittorrentMonitor,
    is_download_stalled,
    is_inactive_torrent,
    is_seeding_ratio_met,
    should_remove_torrent,
    stalled_action_plan,
)


@pytest.mark.parametrize('torrent,expected', [
    ({'state': 'stalleddl', 'dlspeed': 0, 'progress': 0.4}, True),
    ({'state': 'stalledDL', 'dlspeed': 0, 'progress': 0.0}, True),
    ({'state': 'stalleddl', 'dlspeed': 1024, 'progress': 0.4}, False),
    ({'state': 'stalleddl', 'dlspeed': 0, 'progress': 1.0}, False),
    ({'state': 'downloading', 'dlspeed': 0, 'progress': 0.4}, False),
    ({'state': 'stalledup', 'dlspeed': 0, 'upspeed': 0, 'progress': 1.0}, False),
    ({'state': 'uploading', 'dlspeed': 0, 'upspeed': 0, 'progress': 1.0}, False),
])
def test_is_download_stalled(torrent, expected):
    assert is_download_stalled(torrent) is expected


@pytest.mark.parametrize('torrent,expected', [
    ({'state': 'pausedup'}, True),
    ({'state': 'stoppedup'}, True),
    ({'state': 'pauseddl'}, True),
    ({'state': 'stoppeddl'}, True),
    ({'state': 'error'}, False),
    ({'state': 'missingFiles'}, False),
    ({'state': 'uploading'}, False),
    ({'state': 'stalledup'}, False),
    ({'state': 'downloading'}, False),
    ({'state': 'stalleddl'}, False),
    ({'state': 'queueddl'}, False),
    ({'state': 'queuedup'}, False),
    ({'state': 'checkingdl'}, False),
    ({'state': 'moving'}, False),
])
def test_is_inactive_torrent(torrent, expected):
    assert is_inactive_torrent(torrent) is expected


@pytest.mark.parametrize('torrent,min_ratio,expected', [
    ({'state': 'uploading', 'progress': 1.0, 'ratio': 1.0}, 1.0, True),
    ({'state': 'stalledup', 'progress': 1.0, 'ratio': 1.25}, 1.0, True),
    ({'state': 'forcedup', 'progress': 1.0, 'ratio': 1.0}, 1.0, True),
    ({'state': 'queuedup', 'progress': 1.0, 'ratio': 0.99}, 1.0, False),
    ({'state': 'uploading', 'progress': 0.95, 'ratio': 2.0}, 1.0, False),
    ({'state': 'pausedup', 'progress': 1.0, 'ratio': 2.0}, 1.0, False),
    ({'state': 'stoppedup', 'progress': 1.0, 'ratio': 2.0}, 1.0, False),
    ({'state': 'checkingup', 'progress': 1.0, 'ratio': 2.0}, 1.0, False),
    ({'state': 'downloading', 'progress': 1.0, 'ratio': 2.0}, 1.0, False),
    ({'state': 'uploading', 'progress': 1.0, 'ratio': 0.5}, 0.5, True),
    ({'state': 'uploading', 'progress': 1.0, 'ratio': 5.0}, 0.0, False),
])
def test_is_seeding_ratio_met(torrent, min_ratio, expected):
    assert is_seeding_ratio_met(torrent, min_ratio=min_ratio) is expected


@pytest.mark.parametrize('torrent,min_ratio,expected', [
    ({'state': 'pausedup', 'progress': 1.0, 'ratio': 0.1}, 1.0, 'inactive'),
    ({'state': 'pauseddl', 'progress': 0.5, 'ratio': 0.0}, 1.0, 'inactive'),
    ({'state': 'error', 'progress': 0.5, 'ratio': 0.0}, 1.0, None),
    ({'state': 'uploading', 'progress': 1.0, 'ratio': 1.0}, 1.0, 'ratio'),
    ({'state': 'uploading', 'progress': 1.0, 'ratio': 0.5}, 1.0, None),
    ({'state': 'downloading', 'progress': 0.4, 'ratio': 0.0}, 1.0, None),
])
def test_should_remove_torrent(torrent, min_ratio, expected):
    assert should_remove_torrent(torrent, min_ratio=min_ratio) is expected


def test_get_stalled_torrents_ignores_idle_seeds(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    torrents = [
        {'hash': 'a', 'state': 'stalleddl', 'dlspeed': 0, 'progress': 0.2},
        {'hash': 'b', 'state': 'stalledup', 'dlspeed': 0, 'upspeed': 0, 'progress': 1.0},
        {'hash': 'c', 'state': 'downloading', 'dlspeed': 0, 'progress': 0.5},
    ]
    monkeypatch.setattr(monitor, 'get_all_torrents', lambda: torrents)

    stalled = monitor.get_stalled_torrents()

    assert [t['hash'] for t in stalled] == ['a']


def test_get_inactive_torrents(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    torrents = [
        {'hash': 'a', 'state': 'pausedup', 'progress': 1.0, 'ratio': 0.2},
        {'hash': 'b', 'state': 'uploading', 'progress': 1.0, 'ratio': 0.2},
        {'hash': 'c', 'state': 'error', 'progress': 0.5, 'ratio': 0.0},
        {'hash': 'd', 'state': 'downloading', 'progress': 0.1, 'ratio': 0.0},
    ]
    monkeypatch.setattr(monitor, 'get_all_torrents', lambda: torrents)

    inactive = monitor.get_inactive_torrents()

    assert [t['hash'] for t in inactive] == ['a']


def test_get_ratio_complete_torrents(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    torrents = [
        {'hash': 'a', 'state': 'uploading', 'progress': 1.0, 'ratio': 1.0},
        {'hash': 'b', 'state': 'stalledup', 'progress': 1.0, 'ratio': 0.8},
        {'hash': 'c', 'state': 'pausedup', 'progress': 1.0, 'ratio': 2.0},
        {'hash': 'd', 'state': 'stoppedup', 'progress': 1.0, 'ratio': 1.5},
    ]
    monkeypatch.setattr(monitor, 'get_all_torrents', lambda: torrents)

    complete = monitor.get_ratio_complete_torrents(min_ratio=1.0)

    assert [t['hash'] for t in complete] == ['a']


def test_recover_stalled_torrent_runs_reannounce_and_resume(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'reannounce_torrent', lambda torrent_hash: True)
    monkeypatch.setattr(monitor, 'resume_torrent', lambda torrent_hash: True)

    result = monitor.recover_stalled_torrent('abc123')

    assert result['success'] is True
    assert result['actions'] == ['reannounce', 'resume']


def test_recover_stalled_torrent_partial_failure(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'reannounce_torrent', lambda torrent_hash: True)
    monkeypatch.setattr(monitor, 'resume_torrent', lambda torrent_hash: False)

    result = monitor.recover_stalled_torrent('abc123')

    assert result['success'] is True
    assert result['actions'] == ['reannounce']


def test_recover_stalled_torrent_total_failure(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'reannounce_torrent', lambda torrent_hash: False)
    monkeypatch.setattr(monitor, 'resume_torrent', lambda torrent_hash: False)

    result = monitor.recover_stalled_torrent('abc123')

    assert result['success'] is False
    assert result['actions'] == []


def test_handle_ratio_complete_torrent_deletes(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    calls = []

    def fake_delete(torrent_hash, delete_files=False):
        calls.append((torrent_hash, delete_files))
        return True

    monkeypatch.setattr(monitor, 'delete_torrent', fake_delete)

    result = monitor.handle_ratio_complete_torrent('abc123')

    assert result['success'] is True
    assert result['actions'] == ['delete']
    assert calls == [('abc123', False)]


def test_handle_ratio_complete_torrent_deletes_files(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    calls = []

    def fake_delete(torrent_hash, delete_files=False):
        calls.append((torrent_hash, delete_files))
        return True

    monkeypatch.setattr(monitor, 'delete_torrent', fake_delete)

    result = monitor.handle_ratio_complete_torrent('abc123', delete_files=True)

    assert result['success'] is True
    assert result['actions'] == ['delete_files']
    assert calls == [('abc123', True)]


def test_handle_ratio_complete_torrent_failure(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'delete_torrent', lambda torrent_hash, delete_files=False: False)

    result = monitor.handle_ratio_complete_torrent('abc123')

    assert result['success'] is False
    assert result['actions'] == []


def test_reannounce_torrent_calls_api(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    client = MagicMock()
    breaker = MagicMock()
    breaker.call.side_effect = lambda fn: fn()
    monitor.client = client
    monitor.circuit_breaker = breaker
    monkeypatch.setattr(monitor, '_ensure_connected', lambda: True)

    assert monitor.reannounce_torrent('deadbeef') is True
    client.torrents_reannounce.assert_called_once_with(torrent_hashes='deadbeef')


def test_resume_torrent_calls_api(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    client = MagicMock()
    breaker = MagicMock()
    breaker.call.side_effect = lambda fn: fn()
    monitor.client = client
    monitor.circuit_breaker = breaker
    monkeypatch.setattr(monitor, '_ensure_connected', lambda: True)

    assert monitor.resume_torrent('deadbeef') is True
    client.torrents_resume.assert_called_once_with(torrent_hashes='deadbeef')


def test_pause_torrent_calls_api(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    client = MagicMock()
    breaker = MagicMock()
    breaker.call.side_effect = lambda fn: fn()
    monitor.client = client
    monitor.circuit_breaker = breaker
    monkeypatch.setattr(monitor, '_ensure_connected', lambda: True)

    assert monitor.pause_torrent('deadbeef') is True
    client.torrents_pause.assert_called_once_with(torrent_hashes='deadbeef')


def test_delete_torrent_calls_api(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    client = MagicMock()
    breaker = MagicMock()
    breaker.call.side_effect = lambda fn: fn()
    monitor.client = client
    monitor.circuit_breaker = breaker
    monkeypatch.setattr(monitor, '_ensure_connected', lambda: True)

    assert monitor.delete_torrent('deadbeef', delete_files=True) is True
    client.torrents_delete.assert_called_once_with(
        torrent_hashes='deadbeef',
        delete_files=True,
    )


def test_stalled_action_plan_waits_for_threshold():
    plan = stalled_action_plan(
        {'stalled_since': 100.0, 'stalled_recovery_attempts': 0, 'notified_stalled': False},
        now=200.0,
        threshold=300.0,
        cooldown=1800.0,
        max_recovery_attempts=2,
    )
    assert plan == {'recover': False, 'alert': False, 'abandon': False}


def test_stalled_action_plan_alerts_and_recovers_once():
    tracked = {
        'stalled_since': 0.0,
        'stalled_recovery_attempts': 0,
        'notified_stalled': False,
        'last_stalled_action': None,
    }
    first = stalled_action_plan(
        tracked, now=400.0, threshold=300.0, cooldown=1800.0, max_recovery_attempts=2,
    )
    assert first == {'recover': True, 'alert': True, 'abandon': False}

    tracked['stalled_recovery_attempts'] = 1
    tracked['notified_stalled'] = True
    tracked['last_stalled_action'] = 400.0

    during_cooldown = stalled_action_plan(
        tracked, now=500.0, threshold=300.0, cooldown=1800.0, max_recovery_attempts=2,
    )
    assert during_cooldown == {'recover': False, 'alert': False, 'abandon': False}

    second = stalled_action_plan(
        tracked, now=2300.0, threshold=300.0, cooldown=1800.0, max_recovery_attempts=2,
    )
    assert second == {'recover': True, 'alert': False, 'abandon': False}

    tracked['stalled_recovery_attempts'] = 2
    done = stalled_action_plan(
        tracked, now=4200.0, threshold=300.0, cooldown=1800.0, max_recovery_attempts=2,
    )
    assert done == {'recover': False, 'alert': False, 'abandon': False}


def test_get_torrent_summary_fetches_once(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monitor._connected = True
    monitor._last_error = None
    calls = {'count': 0}
    torrents = [
        {'state': 'downloading', 'dlspeed': 100, 'upspeed': 0, 'downloaded': 10, 'uploaded': 1},
        {'state': 'uploading', 'dlspeed': 0, 'upspeed': 50, 'downloaded': 20, 'uploaded': 5},
    ]

    def fake_all():
        calls['count'] += 1
        return torrents

    monkeypatch.setattr(monitor, 'get_all_torrents', fake_all)

    summary = monitor.get_torrent_summary()

    assert calls['count'] == 1
    assert summary['total'] == 2
    assert summary['downloading'] == 1
    assert summary['seeding'] == 1
    assert summary['total_download_speed'] == 100
    assert summary['total_upload_speed'] == 50


def test_stalled_action_plan_retries_alert_after_recovery_exhausted():
    plan = stalled_action_plan(
        {
            'stalled_since': 0.0,
            'stalled_recovery_attempts': 2,
            'notified_stalled': False,
            'last_stalled_action': 100.0,
        },
        now=2000.0,
        threshold=300.0,
        cooldown=1800.0,
        max_recovery_attempts=2,
    )
    assert plan == {'recover': False, 'alert': True, 'abandon': True}
