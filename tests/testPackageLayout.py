"""Tests that enforce the core/<subsystem>/ package layout conventions."""

import ast
import importlib
import re
from pathlib import Path

import pytest

SRC_DIR = Path(__file__).resolve().parents[1] / 'src'
CORE_DIR = SRC_DIR / 'core'

SUBSYSTEMS = sorted(
    path.name for path in CORE_DIR.iterdir()
    if path.is_dir() and not path.name.startswith('__')
)


def test_subsystems_were_discovered():
    assert len(SUBSYSTEMS) > 20


def test_src_contains_only_main_and_core():
    entries = {
        path.name for path in SRC_DIR.iterdir()
        if path.name not in {'__pycache__', '.env', '.gitignore'}
        and not path.name.endswith('.log')
    }

    assert entries == {'main.py', 'core'}


@pytest.mark.parametrize('subsystem', SUBSYSTEMS)
def test_subsystem_is_a_package_with_an_impl_module(subsystem):
    package = CORE_DIR / subsystem

    assert (package / '__init__.py').exists(), f'{subsystem} is missing __init__.py'

    impl_modules = [p for p in package.glob('*.py') if p.name != '__init__.py']

    assert impl_modules, f'{subsystem} has no implementation module'


@pytest.mark.parametrize('subsystem', SUBSYSTEMS)
def test_subsystem_imports_and_exports_its_public_api(subsystem):
    module = importlib.import_module(f'core.{subsystem}')

    assert hasattr(module, '__all__'), f'core.{subsystem} defines no __all__'
    assert module.__all__, f'core.{subsystem} exports nothing'

    for name in module.__all__:
        assert hasattr(module, name), f'core.{subsystem}.__all__ lists missing {name}'


@pytest.mark.parametrize('subsystem', SUBSYSTEMS)
def test_no_private_names_are_exported(subsystem):
    module = importlib.import_module(f'core.{subsystem}')
    private = [name for name in module.__all__ if name.startswith('_')]

    assert private == [], f'core.{subsystem} exports private names: {private}'


@pytest.mark.parametrize('subsystem', SUBSYSTEMS)
def test_module_logger_is_not_part_of_public_api(subsystem):
    module = importlib.import_module(f'core.{subsystem}')

    assert 'logger' not in module.__all__


def test_no_module_imports_the_old_flat_paths():
    stale = ('monitor.', 'util.', 'discord.', 'web.', 'cache.', 'observability.')
    offenders = []

    for path in list(SRC_DIR.rglob('*.py')) + list(Path(__file__).parent.glob('*.py')):
        tree = ast.parse(path.read_text())
        for node in ast.walk(tree):
            if isinstance(node, ast.ImportFrom) and node.module:
                if node.module.startswith(stale):
                    offenders.append(f'{path.name}: from {node.module}')
            elif isinstance(node, ast.Import):
                for alias in node.names:
                    if alias.name.startswith(stale):
                        offenders.append(f'{path.name}: import {alias.name}')

    assert offenders == [], f'imports still use the pre-refactor layout: {offenders}'


CAMEL_CASE_MODULE = re.compile(r'^[a-z][a-zA-Z0-9]*\.py$')
CAMEL_CASE_PACKAGE = re.compile(r'^[a-z][a-zA-Z0-9]*$')

RESERVED_FILENAMES = {'__init__.py', 'conftest.py'}


def test_all_module_filenames_are_camel_case():
    offenders = []

    for path in list(SRC_DIR.rglob('*.py')) + list(Path(__file__).parent.glob('*.py')):
        if path.name in RESERVED_FILENAMES:
            continue
        if not CAMEL_CASE_MODULE.match(path.name):
            offenders.append(path.name)

    assert offenders == [], f'filenames are not camelCase: {sorted(set(offenders))}'


def test_all_package_directories_are_camel_case():
    offenders = [
        path.name for path in CORE_DIR.iterdir()
        if path.is_dir() and not path.name.startswith('__')
        and not CAMEL_CASE_PACKAGE.match(path.name)
    ]

    assert offenders == [], f'package directories are not camelCase: {offenders}'


def test_dashboard_frontend_ships_with_the_web_package():
    site = CORE_DIR / 'web' / 'site'

    assert (site / 'index.html').is_file()
    assert (site / 'js' / 'main.js').is_file()


def test_web_server_resolves_the_frontend_directory():
    from core.web import server

    site = Path(server.__file__).parent / 'site'

    assert (site / 'index.html').is_file()
