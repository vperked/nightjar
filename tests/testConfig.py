"""Tests for environment configuration parsing, validation and safe defaults."""

import pytest

from core.config import settings as config
from core.config import (
    DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD,
    DEFAULT_DISK_CLEANUP_THRESHOLD,
    DEFAULT_SYSTEM_UPDATE_APPLY,
    DEFAULT_SYSTEM_UPDATE_ENABLED,
    DEFAULT_WEB_HOST,
    clamp_threshold,
    get_cached_env,
    get_env_bool,
    get_env_float,
    get_env_int,
    validate_config,
)


@pytest.mark.parametrize('raw,expected', [
    ('true', True),
    ('TRUE', True),
    ('1', True),
    ('yes', True),
    ('on', True),
    ('false', False),
    ('0', False),
    ('no', False),
    ('off', False),
])
def test_get_env_bool_accepted_values(monkeypatch, raw, expected):
    monkeypatch.setenv('SOME_FLAG', raw)

    assert get_env_bool('SOME_FLAG', not expected) is expected


def test_get_env_bool_invalid_falls_back(monkeypatch):
    monkeypatch.setenv('SOME_FLAG', 'maybe')

    assert get_env_bool('SOME_FLAG', True) is True


def test_get_env_bool_empty_falls_back(monkeypatch):
    monkeypatch.setenv('SOME_FLAG', '   ')

    assert get_env_bool('SOME_FLAG', True) is True


def test_get_env_float_out_of_range_uses_default(monkeypatch):
    monkeypatch.setenv('CPU_THRESHOLD', '150')

    assert get_env_float('CPU_THRESHOLD', 80.0, min_val=0.0, max_val=100.0) == 80.0


def test_get_env_float_typed_cache_hits(monkeypatch):
    monkeypatch.setenv('CPU_THRESHOLD', '42.5')

    assert get_env_float('CPU_THRESHOLD', 80.0, min_val=0.0, max_val=100.0) == 42.5
    monkeypatch.setenv('CPU_THRESHOLD', '99.0')
    assert get_env_float('CPU_THRESHOLD', 80.0, min_val=0.0, max_val=100.0) == 42.5


def test_get_env_float_in_range(monkeypatch):
    monkeypatch.setenv('CPU_THRESHOLD', '42.5')

    assert get_env_float('CPU_THRESHOLD', 80.0, min_val=0.0, max_val=100.0) == 42.5


@pytest.mark.parametrize('raw', ['abc', '', '   ', 'NaNaN'])
def test_get_env_float_invalid_uses_default(monkeypatch, raw):
    monkeypatch.setenv('CPU_THRESHOLD', raw)

    assert get_env_float('CPU_THRESHOLD', 80.0) == 80.0


def test_get_env_int_below_minimum_uses_default(monkeypatch):
    monkeypatch.setenv('WEB_PORT', '0')

    assert get_env_int('WEB_PORT', 8080, min_val=1, max_val=65535) == 8080


def test_get_env_int_invalid_uses_default(monkeypatch):
    monkeypatch.setenv('WEB_PORT', 'not-a-port')

    assert get_env_int('WEB_PORT', 8080) == 8080


def test_invalid_env_value_is_not_cached(monkeypatch):
    monkeypatch.setenv('LOG_FILE', '/var/log/app.log; rm -rf /')

    value = get_cached_env('LOG_FILE', '/var/log/default.log')

    assert value == '/var/log/default.log'
    assert config._env_cache['LOG_FILE'] == '/var/log/default.log'


def test_valid_env_value_is_cached(monkeypatch):
    monkeypatch.setenv('LOG_FILE', '/var/log/app.log')

    assert get_cached_env('LOG_FILE', '/tmp/default.log') == '/var/log/app.log'
    assert config._env_cache['LOG_FILE'] == '/var/log/app.log'


@pytest.mark.parametrize('value,low,high', [
    (-50.0, 0.0, 100.0),
    (150.0, 0.0, 100.0),
    (50.0, 0.0, 100.0),
    (0.0, 0.0, 100.0),
    (100.0, 0.0, 100.0),
])
def test_clamp_threshold_stays_in_range(value, low, high):
    result = clamp_threshold(value, low, high)

    assert low <= result <= high


def test_system_updates_are_off_by_default():
    assert DEFAULT_SYSTEM_UPDATE_ENABLED is False
    assert DEFAULT_SYSTEM_UPDATE_APPLY is False


def test_web_host_defaults_to_loopback():
    assert DEFAULT_WEB_HOST == '127.0.0.1'


def test_cleanup_thresholds_are_conservative():
    assert DEFAULT_DISK_CLEANUP_THRESHOLD >= 80.0
    assert DEFAULT_DISK_AGGRESSIVE_CLEANUP_THRESHOLD > DEFAULT_DISK_CLEANUP_THRESHOLD


def test_validate_config_rejects_out_of_range_threshold(monkeypatch):
    monkeypatch.setenv('CPU_THRESHOLD', '500')

    with pytest.raises(ValueError, match='CPU_THRESHOLD'):
        validate_config()


def test_validate_config_rejects_non_numeric_threshold(monkeypatch):
    monkeypatch.setenv('MEMORY_THRESHOLD', 'high')

    with pytest.raises(ValueError, match='MEMORY_THRESHOLD'):
        validate_config()


def test_validate_config_rejects_traversal_in_paths(monkeypatch):
    monkeypatch.setenv('LOG_FILE', '/var/log/../../etc/passwd')

    with pytest.raises(ValueError, match='LOG_FILE'):
        validate_config()


def test_validate_config_passes_with_sane_values(monkeypatch):
    monkeypatch.setenv('CPU_THRESHOLD', '80')
    monkeypatch.setenv('MEMORY_THRESHOLD', '80')
    monkeypatch.setenv('CHECK_INTERVAL', '5')
    monkeypatch.delenv('LOG_FILE', raising=False)
    monkeypatch.delenv('MEDIA_DIRECTORY', raising=False)
    monkeypatch.delenv('ALERT_LOG_PATH', raising=False)

    validate_config()
