"""Tests for cleanup path allowlisting and temp file removal boundaries."""

import time

import pytest

from core.disk import ALLOWED_CACHE_CLEANUP_BASES, ALLOWED_TEMP_CLEANUP_BASES, DiskMonitor
from core.disk import monitor as disk_module
from core.disk.monitor import _resolve_cleanup_dirs


def test_allowlisted_temp_dir_is_accepted():
    resolved = _resolve_cleanup_dirs(['/tmp'], ALLOWED_TEMP_CLEANUP_BASES)

    assert resolved == ['/tmp']


def test_path_outside_allowlist_is_rejected():
    assert _resolve_cleanup_dirs(['/var/lib'], ALLOWED_TEMP_CLEANUP_BASES) == []


def test_home_directory_is_rejected():
    assert _resolve_cleanup_dirs(['/home'], ALLOWED_TEMP_CLEANUP_BASES) == []


def test_traversal_out_of_allowlist_is_rejected():
    assert _resolve_cleanup_dirs(['/tmp/../etc'], ALLOWED_TEMP_CLEANUP_BASES) == []


def test_subdirectory_of_allowlist_is_accepted():
    resolved = _resolve_cleanup_dirs(['/var/cache/apt/archives'], ALLOWED_CACHE_CLEANUP_BASES)

    assert resolved == ['/var/cache/apt/archives']


def test_blank_entries_are_ignored():
    assert _resolve_cleanup_dirs(['', '   '], ALLOWED_TEMP_CLEANUP_BASES) == []


def test_unsafe_override_allows_arbitrary_path(monkeypatch):
    monkeypatch.setenv('ALLOW_UNSAFE_CLEANUP_PATHS', 'true')

    assert _resolve_cleanup_dirs(['/var/lib'], ALLOWED_TEMP_CLEANUP_BASES) == ['/var/lib']


def test_temp_cleanup_only_removes_aged_files(tmp_path, monkeypatch):
    old_file = tmp_path / 'old.tmp'
    new_file = tmp_path / 'new.tmp'
    old_file.write_text('old')
    new_file.write_text('new')

    old_time = time.time() - (30 * 24 * 60 * 60)
    import os
    os.utime(old_file, (old_time, old_time))

    monkeypatch.setenv('TEMP_DIRS', str(tmp_path))
    monkeypatch.setattr(
        disk_module,
        'ALLOWED_TEMP_CLEANUP_BASES',
        [str(tmp_path)],
        raising=False,
    )
    monkeypatch.setattr(disk_module, '_resolve_cleanup_dirs', lambda c, b: [str(tmp_path)])

    stats = DiskMonitor().cleanup_temporary_files(max_age_days=7)

    assert not old_file.exists()
    assert new_file.exists()
    assert stats['files_removed'] == 1


def test_temp_cleanup_skips_rejected_dirs(monkeypatch):
    monkeypatch.setenv('TEMP_DIRS', '/var/lib')
    monkeypatch.delenv('ALLOW_UNSAFE_CLEANUP_PATHS', raising=False)

    stats = DiskMonitor().cleanup_temporary_files(max_age_days=7)

    assert stats['files_removed'] == 0
    assert stats['dirs_removed'] == 0


@pytest.mark.parametrize('percent,cleanup,aggressive,expected', [
    (39.9, 85.0, 92.0, 'none'),
    (84.9, 85.0, 92.0, 'none'),
    (85.0, 85.0, 92.0, 'standard'),
    (91.9, 85.0, 92.0, 'standard'),
    (92.0, 85.0, 92.0, 'aggressive'),
    (97.0, 85.0, 92.0, 'aggressive'),
])
def test_cleanup_tier_boundaries(percent, cleanup, aggressive, expected):
    if percent < cleanup:
        tier = 'none'
    elif percent < aggressive:
        tier = 'standard'
    else:
        tier = 'aggressive'

    assert tier == expected
