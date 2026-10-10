import logging
import os
import time

import pytest

from core.disk.monitor import _clean_directory_tree
from core.logs.monitor import LogMonitor
from core.observability.redaction import RedactingFormatter, redact_secrets

TOKEN = 'ZsiI4qgoFDxFHZUIyGFUHaNqG-1CRH9ohrAOqDU31myCjKBc1UL'


def test_redact_secrets_masks_webhook_token():
    text = f"url: /api/webhooks/1457196643467264105/{TOKEN} (Caused by ...)"

    out = redact_secrets(text)

    assert TOKEN not in out
    assert '/api/webhooks/1457196643467264105/<redacted>' in out


def test_formatter_redacts_exception_text():
    record = logging.LogRecord(
        'x', logging.WARNING, __file__, 1,
        f"failed https://discord.com/api/webhooks/1/{TOKEN}", None, None,
    )

    assert TOKEN not in RedactingFormatter('%(message)s').format(record)


def _log_info(path):
    return {'path': str(path), 'size_bytes': path.lstat().st_size}


def test_force_cleanup_does_not_follow_symlink(tmp_path):
    victim = tmp_path / 'victim.txt'
    victim.write_text('x' * 50)
    link = tmp_path / 'app.log'
    os.symlink(victim, link)

    monitor = LogMonitor()
    monitor.cleanup_log({'path': str(link), 'size_bytes': 50}, force=True)

    assert victim.read_text() == 'x' * 50


def test_trim_does_not_follow_symlink(tmp_path):
    victim = tmp_path / 'victim.txt'
    victim.write_text('x' * 5000)
    link = tmp_path / 'app.log'
    os.symlink(victim, link)

    monitor = LogMonitor()
    monitor.threshold_bytes = 10
    monitor.cleanup_log({'path': str(link), 'size_bytes': 5000})

    assert victim.read_text() == 'x' * 5000


def test_force_cleanup_empties_regular_log(tmp_path):
    log = tmp_path / 'app.log'
    log.write_text('data')

    assert LogMonitor().cleanup_log(_log_info(log), force=True) is True
    assert log.stat().st_size == 0


def test_force_cleanup_trims_protected_auth_log(tmp_path):
    log = tmp_path / 'auth.log'
    log.write_text('a' * 100)
    monitor = LogMonitor()
    monitor.threshold_bytes = 40

    monitor.cleanup_log(_log_info(log), force=True)

    assert log.read_text() == 'a' * 40


@pytest.mark.parametrize('name', ['mysql-bin.000123', 'mariadb-bin.000002', 'relay-bin.000004'])
def test_database_binlogs_are_not_rotated_logs(name):
    assert LogMonitor.is_rotated_log(name) is False


def test_clean_tree_removes_only_stale_regular_files(tmp_path):
    old = tmp_path / 'old.tmp'
    new = tmp_path / 'new.tmp'
    old.write_text('o')
    new.write_text('n')
    stale = time.time() - 10 * 86400
    os.utime(old, (stale, stale))
    outside = tmp_path.parent / (tmp_path.name + '-outside')
    outside.mkdir()
    (outside / 'keep.txt').write_text('keep')
    old_link = tmp_path / 'link'
    os.symlink(outside, old_link)
    os.utime(old_link, (stale, stale), follow_symlinks=False)
    sock_dir = tmp_path / '.X11-unix'
    sock_dir.mkdir()
    (sock_dir / 'X0').write_text('live')
    os.utime(sock_dir / 'X0', (stale, stale))
    stats = {'files_removed': 0, 'dirs_removed': 0, 'total_size_cleared': 0, 'errors': []}

    _clean_directory_tree(str(tmp_path), 86400, stats)

    assert not old.exists() and new.exists()
    assert (outside / 'keep.txt').exists()
    assert not os.path.lexists(old_link)
    assert (sock_dir / 'X0').exists()


def test_clean_tree_keeps_fresh_empty_dirs(tmp_path):
    (tmp_path / 'fresh').mkdir()
    stats = {'files_removed': 0, 'dirs_removed': 0, 'total_size_cleared': 0, 'errors': []}

    _clean_directory_tree(str(tmp_path), 86400, stats)

    assert (tmp_path / 'fresh').is_dir()
