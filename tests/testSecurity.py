"""Tests for path validation and environment variable validation."""

import os

import pytest

from core.security import (
    sanitize_path,
    validate_environment_variable,
    validate_path,
)


def test_traversal_outside_base_is_rejected(tmp_path):
    base = tmp_path / 'site'
    base.mkdir()

    is_valid, resolved, error = validate_path(
        str(base / '..' / 'secret.txt'),
        allowed_base_paths=[str(base)],
    )

    assert is_valid is False
    assert resolved is None
    assert 'outside allowed directories' in error


def test_classic_traversal_string_is_rejected(tmp_path):
    base = tmp_path / 'site'
    base.mkdir()

    is_valid, _, _ = validate_path('/tmp/../etc/passwd', allowed_base_paths=[str(base)])

    assert is_valid is False


def test_path_inside_base_is_accepted(tmp_path):
    base = tmp_path / 'site'
    base.mkdir()
    target = base / 'js' / 'main.js'
    target.parent.mkdir()
    target.write_text('console.log(1);')

    is_valid, resolved, error = validate_path(str(target), allowed_base_paths=[str(base)])

    assert is_valid is True
    assert resolved == str(target.resolve())
    assert error is None


def test_base_path_itself_is_accepted(tmp_path):
    base = tmp_path / 'site'
    base.mkdir()

    is_valid, _, _ = validate_path(str(base), allowed_base_paths=[str(base)])

    assert is_valid is True


def test_double_dot_in_filename_is_not_traversal(tmp_path):
    target = tmp_path / 'archive..2024.log'
    target.write_text('data')

    is_valid, resolved, error = validate_path(str(target), allowed_base_paths=[str(tmp_path)])

    assert is_valid is True, error
    assert resolved == str(target.resolve())


def test_null_byte_is_rejected():
    is_valid, _, error = validate_path('/tmp/evil\x00.txt')

    assert is_valid is False
    assert 'null bytes' in error


def test_shell_metacharacters_are_rejected():
    is_valid, _, error = validate_path('/tmp/foo; rm -rf /')

    assert is_valid is False
    assert 'suspicious characters' in error


def test_empty_path_is_rejected():
    is_valid, _, error = validate_path('')

    assert is_valid is False
    assert 'non-empty string' in error


def test_symlink_is_rejected_by_default(tmp_path):
    target = tmp_path / 'real.txt'
    target.write_text('data')
    link = tmp_path / 'link.txt'
    link.symlink_to(target)

    is_valid, _, error = validate_path(str(link), allowed_base_paths=[str(tmp_path)])

    assert is_valid is False
    assert 'Symlinks' in error


def test_symlink_allowed_when_opted_in(tmp_path):
    target = tmp_path / 'real.txt'
    target.write_text('data')
    link = tmp_path / 'link.txt'
    link.symlink_to(target)

    is_valid, _, _ = validate_path(
        str(link),
        allowed_base_paths=[str(tmp_path)],
        allow_symlinks=True,
    )

    assert is_valid is True


def test_sanitize_path_rejects_escape(tmp_path):
    base = tmp_path / 'base'
    base.mkdir()

    assert sanitize_path(str(base / '..' / 'other'), base_path=str(base)) is None


def test_sanitize_path_accepts_child(tmp_path):
    base = tmp_path / 'base'
    base.mkdir()
    child = base / 'child.txt'

    assert sanitize_path(str(child), base_path=str(base)) == os.path.normpath(str(child))


@pytest.mark.parametrize('value', [
    'value; rm -rf /',
    '$(whoami)',
    '<script>alert(1)</script>',
    '__import__("os")',
])
def test_blocked_env_patterns(value):
    is_valid, error = validate_environment_variable('SOME_KEY', value)

    assert is_valid is False
    assert 'blocked pattern' in error


def test_env_length_limit():
    is_valid, error = validate_environment_variable('SOME_KEY', 'a' * 20, max_length=10)

    assert is_valid is False
    assert 'maximum length' in error


def test_valid_env_value_passes():
    is_valid, error = validate_environment_variable('WEB_HOST', '127.0.0.1')

    assert is_valid is True
    assert error is None
