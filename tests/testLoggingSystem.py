"""Tests for the observability logging package and its usage conventions."""

import logging
import re
from pathlib import Path

import pytest

from core import observability
from core.observability import Component, configure_logging, get_logger, safe_log
from core.observability.components import KNOWN_COMPONENTS, ROOT
from core.observability.settings import resolve_level

SRC_DIR = Path(__file__).resolve().parents[1] / 'src'


def _source_files():
    return [path for path in SRC_DIR.rglob('*.py') if 'observability' not in path.parts]


def test_stdlib_logging_is_not_shadowed():
    assert Path(logging.__file__).parent != SRC_DIR
    assert not (SRC_DIR / 'logging').exists()


def test_stdlib_logging_handlers_still_importable():
    from logging.handlers import RotatingFileHandler

    assert RotatingFileHandler is not None


def test_component_logger_is_child_of_root():
    logger = get_logger(Component.DISK)

    assert logger.name == f'{ROOT}.Disk'
    assert logger.parent.name == ROOT


def test_root_logger_returned_without_component():
    assert get_logger().name == ROOT


def test_root_name_does_not_nest():
    assert get_logger(ROOT).name == ROOT


def test_handlers_are_attached_only_to_root():
    get_logger(Component.DISK)
    get_logger(Component.WEB)
    get_logger(Component.KERNEL)

    root = logging.getLogger(ROOT)

    assert len(root.handlers) >= 1
    for component in (Component.DISK, Component.WEB, Component.KERNEL):
        assert logging.getLogger(f'{ROOT}.{component}').handlers == []


def test_only_one_file_handler_exists():
    configure_logging(force=True)
    for component in KNOWN_COMPONENTS:
        get_logger(component)

    root = logging.getLogger(ROOT)
    file_handlers = [h for h in root.handlers if isinstance(h, observability.DiskAwareRotatingFileHandler)]

    assert len(file_handlers) <= 1


def test_repeated_configuration_does_not_duplicate_handlers():
    configure_logging(force=True)
    count = len(logging.getLogger(ROOT).handlers)

    for _ in range(5):
        configure_logging()

    assert len(logging.getLogger(ROOT).handlers) == count


def test_component_name_appears_in_output(capsys):
    configure_logging(force=True)
    get_logger(Component.KERNEL).warning('kernel oops detected')

    captured = capsys.readouterr()

    assert 'ServerMonitor.Kernel' in captured.out
    assert 'kernel oops detected' in captured.out


def test_child_records_propagate_once(capsys):
    configure_logging(force=True)
    get_logger(Component.CPU).error('cpu pegged')

    captured = capsys.readouterr()

    assert captured.out.count('cpu pegged') == 1


def test_root_does_not_propagate_to_python_root():
    configure_logging(force=True)

    assert logging.getLogger(ROOT).propagate is False


@pytest.mark.parametrize('raw,expected', [
    ('debug', 'DEBUG'),
    ('INFO', 'INFO'),
    ('  warning  ', 'WARNING'),
    ('nonsense', 'INFO'),
    ('', 'INFO'),
    (None, 'INFO'),
])
def test_level_resolution(monkeypatch, raw, expected):
    monkeypatch.delenv('LOG_LEVEL', raising=False)

    assert resolve_level(raw) == expected


def test_level_override_applies_to_handlers():
    configure_logging(level='DEBUG', force=True)
    root = logging.getLogger(ROOT)

    assert root.level == logging.DEBUG
    assert all(handler.level == logging.DEBUG for handler in root.handlers)

    configure_logging(level='INFO', force=True)


def test_safe_log_swallows_disk_full(capsys):
    def explode(message):
        raise OSError(28, 'No space left on device')

    safe_log(explode, 'important message')

    captured = capsys.readouterr()

    assert 'Disk full' in captured.err
    assert 'important message' in captured.err


def test_safe_log_swallows_unexpected_errors(capsys):
    def explode(message):
        raise RuntimeError('handler exploded')

    safe_log(explode, 'another message')

    assert 'another message' in capsys.readouterr().err


def test_safe_log_passes_through_normally():
    received = []

    safe_log(received.append, 'ok')

    assert received == ['ok']


def test_every_component_name_is_unique():
    names = [
        value for name, value in vars(Component).items()
        if not name.startswith('_') and isinstance(value, str)
    ]

    assert len(names) == len(set(names))


def test_modules_import_logging_from_observability():
    offenders = [
        str(path.relative_to(SRC_DIR))
        for path in _source_files()
        if re.search(r'^from logger import|^import logger$', path.read_text(), re.MULTILINE)
    ]

    assert offenders == [], f"modules still importing the removed logger shim: {offenders}"


def test_all_logger_calls_use_registered_components():
    pattern = re.compile(r'get_logger\(\s*([^)]*?)\s*\)')
    offenders = []

    for path in _source_files():
        for match in pattern.finditer(path.read_text()):
            argument = match.group(1).strip()
            if not argument:
                continue
            if not argument.startswith('Component.'):
                offenders.append(f'{path.relative_to(SRC_DIR)}: {argument}')
                continue
            attribute = argument.split('.', 1)[1]
            if getattr(Component, attribute, None) not in KNOWN_COMPONENTS:
                offenders.append(f'{path.relative_to(SRC_DIR)}: {argument}')

    assert offenders == [], f"unregistered logger components: {offenders}"


def test_logger_shim_is_gone():
    assert not (SRC_DIR / 'logger.py').exists()

    with pytest.raises(ImportError):
        __import__('logger')


def test_package_exposes_public_api():
    for name in ('Component', 'get_logger', 'configure_logging', 'safe_log', 'LOG_FILE'):
        assert hasattr(observability, name)
