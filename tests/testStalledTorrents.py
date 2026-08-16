"""Tests for stalled torrent detection and recovery helpers."""

from unittest.mock import MagicMock

import pytest

from core.qbittorrent import QBittorrentMonitor, is_download_stalled, is_seeding_ratio_met


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


def test_get_ratio_complete_torrents(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    torrents = [
        {'hash': 'a', 'state': 'uploading', 'progress': 1.0, 'ratio': 1.0},
        {'hash': 'b', 'state': 'stalledup', 'progress': 1.0, 'ratio': 0.8},
        {'hash': 'c', 'state': 'pausedup', 'progress': 1.0, 'ratio': 2.0},
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


def test_handle_ratio_complete_torrent_pauses(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'pause_torrent', lambda torrent_hash: True)

    result = monitor.handle_ratio_complete_torrent('abc123')

    assert result['success'] is True
    assert result['actions'] == ['pause']


def test_handle_ratio_complete_torrent_failure(monkeypatch):
    monitor = QBittorrentMonitor.__new__(QBittorrentMonitor)
    monkeypatch.setattr(monitor, 'pause_torrent', lambda torrent_hash: False)

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
