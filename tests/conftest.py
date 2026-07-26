"""Shared pytest fixtures and import path setup for the server monitor test suite."""

import sys
from pathlib import Path

import pytest

SRC_DIR = Path(__file__).resolve().parents[1] / 'src'
if str(SRC_DIR) not in sys.path:
    sys.path.insert(0, str(SRC_DIR))


@pytest.fixture(autouse=True)
def clear_env_cache():
    """Reset the config module env cache so each test sees its own environment."""
    from core.config import settings as config

    config._env_cache.clear()
    yield
    config._env_cache.clear()
