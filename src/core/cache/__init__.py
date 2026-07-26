"""
Python bytecode and __pycache__ cleanup helpers.
"""

from core.cache.cache import (
    find_cache_directories,
    find_pyc_files,
    clear_cache,
    clear_cache_on_startup,
    clear_cache_on_exit,
)

__all__ = [
    'clear_cache',
    'clear_cache_on_exit',
    'clear_cache_on_startup',
    'find_cache_directories',
    'find_pyc_files',
]
