"""Tests for systemd unit generation and its interpreter selection."""

import os
from pathlib import Path

import pytest

from core.systemd import (
    check_dependencies,
    get_main_script_path,
    get_project_root,
    get_python_executable,
    get_systemd_service_content,
)

PROJECT_ROOT = Path(__file__).resolve().parents[1]


def test_project_root_is_the_directory_containing_src_main():
    root = get_project_root()

    assert root == PROJECT_ROOT
    assert (root / 'src' / 'main.py').is_file()


def test_main_script_path_is_not_doubled():
    main_script = get_main_script_path()

    assert main_script.is_file()
    assert 'src/src' not in str(main_script)
    assert main_script == PROJECT_ROOT / 'src' / 'main.py'


@pytest.mark.skipif(
    not (PROJECT_ROOT / 'venv' / 'bin' / 'python3').exists(),
    reason='project venv not present',
)
def test_venv_interpreter_is_preferred_over_the_running_one():
    assert get_python_executable() == str(PROJECT_ROOT / 'venv' / 'bin' / 'python3')


@pytest.mark.skipif(
    not (PROJECT_ROOT / 'venv' / 'bin' / 'python3').exists(),
    reason='project venv not present',
)
def test_venv_interpreter_path_is_not_symlink_resolved():
    """Resolving venv/bin/python3 yields the system interpreter and loses the venv."""
    venv_python = PROJECT_ROOT / 'venv' / 'bin' / 'python3'
    chosen = Path(get_python_executable())

    assert chosen == venv_python
    if venv_python.is_symlink():
        assert chosen != venv_python.resolve()


def test_chosen_interpreter_can_import_dependencies():
    assert check_dependencies(get_python_executable()) == []


def test_check_dependencies_reports_a_broken_interpreter():
    missing = check_dependencies('/nonexistent/python3')

    assert missing


def test_unit_execstart_uses_the_checked_interpreter():
    content = get_systemd_service_content()
    python_exec = get_python_executable()
    main_script = get_main_script_path()

    assert f'ExecStart={python_exec} {main_script}' in content


def test_unit_has_no_doubled_src_path():
    assert 'src/src' not in get_systemd_service_content()


def test_unit_declares_restart_limits():
    content = get_systemd_service_content()

    assert 'StartLimitIntervalSec=' in content
    assert 'StartLimitBurst=' in content


def test_unit_keeps_basic_hardening():
    content = get_systemd_service_content()

    assert 'NoNewPrivileges=true' in content
    assert 'PrivateTmp=true' in content


def test_unit_working_directory_exists():
    content = get_systemd_service_content()
    line = next(l for l in content.splitlines() if l.startswith('WorkingDirectory='))
    working_dir = line.split('=', 1)[1]

    assert Path(working_dir).is_dir()


def test_unit_is_installable_by_systemd_syntax():
    content = get_systemd_service_content()

    for section in ('[Unit]', '[Service]', '[Install]'):
        assert section in content
    assert 'WantedBy=multi-user.target' in content
