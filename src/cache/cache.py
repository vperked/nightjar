"""
Cache clearing utility for Python cache files.
Clears __pycache__ directories and .pyc files on startup and shutdown.
"""

import os
import shutil
from typing import List
from logger import get_logger

logger = get_logger('Cache')


def find_cache_directories(root_dir: str = None) -> List[str]:
    """
    Find all __pycache__ directories in the project.

    Args:
        root_dir: Root directory to search (default: project root)

    Returns:
        List of paths to __pycache__ directories
    """
    if root_dir is None:
        root_dir = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

    cache_dirs = []
    for root, dirs, files in os.walk(root_dir):
        if any(ignore in root for ignore in ['venv', '.venv', 'env', '.env', 'node_modules', '.git']):
            continue

        if '__pycache__' in dirs:
            cache_dirs.append(os.path.join(root, '__pycache__'))

    return cache_dirs


def find_pyc_files(root_dir: str = None) -> List[str]:
    """
    Find all .pyc files in the project.

    Args:
        root_dir: Root directory to search (default: project root)

    Returns:
        List of paths to .pyc files
    """
    if root_dir is None:
        root_dir = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

    pyc_files = []
    for root, dirs, files in os.walk(root_dir):
        if any(ignore in root for ignore in ['venv', '.venv', 'env', '.env', 'node_modules', '.git']):
            continue

        for file in files:
            if file.endswith('.pyc'):
                pyc_files.append(os.path.join(root, file))

    return pyc_files


def clear_cache(verbose: bool = True) -> dict:
    """
    Clear all Python cache files and directories.

    Args:
        verbose: Whether to print information about cleared cache

    Returns:
        Dictionary with statistics about cleared cache
    """
    stats = {
        'cache_dirs_removed': 0,
        'pyc_files_removed': 0,
        'total_size_cleared': 0,
        'errors': []
    }

    root_dir = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    cache_dirs = find_cache_directories(root_dir)
    pyc_files = find_pyc_files(root_dir)

    for cache_dir in cache_dirs:
        try:
            total_size = 0
            for dirpath, dirnames, filenames in os.walk(cache_dir):
                for filename in filenames:
                    filepath = os.path.join(dirpath, filename)
                    try:
                        total_size += os.path.getsize(filepath)
                    except (OSError, FileNotFoundError):
                        pass

            shutil.rmtree(cache_dir)
            stats['cache_dirs_removed'] += 1
            stats['total_size_cleared'] += total_size

            if verbose:
                logger.debug(f"Removed cache directory: {cache_dir} ({_format_bytes(total_size)})")
        except Exception as e:
            error_msg = f"Error removing {cache_dir}: {e}"
            stats['errors'].append(error_msg)
            if verbose:
                logger.warning(error_msg)

    for pyc_file in pyc_files:
        try:
            file_size = os.path.getsize(pyc_file)
            os.remove(pyc_file)
            stats['pyc_files_removed'] += 1
            stats['total_size_cleared'] += file_size

            if verbose:
                logger.debug(f"Removed .pyc file: {pyc_file} ({_format_bytes(file_size)})")
        except Exception as e:
            error_msg = f"Error removing {pyc_file}: {e}"
            stats['errors'].append(error_msg)
            if verbose:
                logger.warning(error_msg)

    return stats


def _format_bytes(bytes_value: int) -> str:
    """
    Format bytes to human-readable format.

    Args:
        bytes_value: Value in bytes

    Returns:
        Formatted string (e.g., "1.50 KB")
    """
    if bytes_value == 0:
        return "0 B"
    for unit in ['B', 'KB', 'MB', 'GB']:
        if bytes_value < 1024.0:
            return f"{bytes_value:.2f} {unit}"
        bytes_value /= 1024.0
    return f"{bytes_value:.2f} TB"


def clear_cache_on_startup(verbose: bool = True) -> dict:
    """
    Clear cache on startup. This should be called at the beginning of main().

    Args:
        verbose: Whether to print information about cleared cache

    Returns:
        Dictionary with statistics about cleared cache
    """
    if verbose:
        logger.info("Clearing cache...")

    stats = clear_cache(verbose=False)

    if verbose:
        if stats['cache_dirs_removed'] > 0 or stats['pyc_files_removed'] > 0:
            logger.info(f"Cache cleared ({stats['cache_dirs_removed']} dirs, {stats['pyc_files_removed']} files)")
        else:
            logger.info("No cache found")

    return stats


def clear_cache_on_exit(verbose: bool = True) -> dict:
    """
    Clear cache on exit. This should be called during shutdown.

    Args:
        verbose: Whether to print information about cleared cache

    Returns:
        Dictionary with statistics about cleared cache
    """
    if verbose:
        logger.info("Clearing cache...")

    stats = clear_cache(verbose=False)

    if verbose:
        if stats['cache_dirs_removed'] > 0 or stats['pyc_files_removed'] > 0:
            logger.info(f"Cache cleared ({stats['cache_dirs_removed']} dirs, {stats['pyc_files_removed']} files)")
        else:
            logger.info("No cache found")

    return stats


if __name__ == "__main__":
    logger.info("Clearing Python cache...")
    stats = clear_cache(verbose=True)
    logger.info("Cache clearing complete:")
    logger.info(f"  Cache directories removed: {stats['cache_dirs_removed']}")
    logger.info(f"  .pyc files removed: {stats['pyc_files_removed']}")
    logger.info(f"  Total size cleared: {_format_bytes(stats['total_size_cleared'])}")
    if stats['errors']:
        logger.warning(f"  Errors: {len(stats['errors'])}")
        for error in stats['errors']:
            logger.warning(f"    - {error}")

