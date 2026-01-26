"""
Logging configuration for Server Monitor.
Provides a centralized logging setup with configurable levels.
"""

import logging
import os
import sys
import psutil
from logging.handlers import RotatingFileHandler
from pathlib import Path
from dotenv import load_dotenv

load_dotenv()

LOG_LEVEL = os.getenv('LOG_LEVEL', 'INFO').upper()
LOG_FILE = os.getenv('LOG_FILE', 'server_monitor.log')
LOG_MAX_BYTES = int(os.getenv('LOG_MAX_BYTES', '10485760'))
LOG_BACKUP_COUNT = int(os.getenv('LOG_BACKUP_COUNT', '5'))
DISK_FULL_THRESHOLD = float(os.getenv('DISK_FULL_THRESHOLD', '95.0'))


class DiskAwareRotatingFileHandler(RotatingFileHandler):
    """
    RotatingFileHandler that gracefully handles disk full errors.
    Disables file logging when disk is critically full to prevent crashes.
    """
    
    def __init__(self, *args, **kwargs):
        """Initialize the disk-aware file handler."""
        self._file_logging_disabled = False
        self._disk_full_threshold = DISK_FULL_THRESHOLD
        super().__init__(*args, **kwargs)
    
    def _check_disk_space(self) -> bool:
        """
        Check if disk has enough space for logging.
        
        Returns:
            True if disk has space, False if critically full
        """
        try:
            log_path = Path(self.baseFilename)
            if not log_path.exists():
                log_path = log_path.parent
            
            disk_usage = psutil.disk_usage(str(log_path))
            disk_percent = disk_usage.percent
            
            if disk_percent >= self._disk_full_threshold:
                if not self._file_logging_disabled:
                    self._file_logging_disabled = True
                    print(f"[LOGGER] Disk at {disk_percent:.1f}% - disabling file logging to prevent crashes", file=sys.stderr)
                return False
            else:
                if self._file_logging_disabled and disk_percent < (self._disk_full_threshold - 5.0):
                    self._file_logging_disabled = False
                    print(f"[LOGGER] Disk at {disk_percent:.1f}% - re-enabling file logging", file=sys.stderr)
                return True
        except Exception:
            return True
    
    def emit(self, record):
        """
        Emit a log record, handling disk full errors gracefully.
        
        Args:
            record: Log record to emit
        """
        if self._file_logging_disabled:
            if not self._check_disk_space():
                return
        
        try:
            if not self._check_disk_space():
                return
            
            super().emit(record)
        except OSError as e:
            if e.errno == 28:
                if not self._file_logging_disabled:
                    self._file_logging_disabled = True
                    print(f"[LOGGER] Disk full error detected - disabling file logging: {e}", file=sys.stderr)
            else:
                self.handleError(record)
        except Exception:
            self.handleError(record)


def setup_logger(name: str = 'ServerMonitor', level: str = None) -> logging.Logger:
    """
    Set up and configure a logger instance.

    Args:
        name: Logger name (default: 'ServerMonitor')
        level: Log level (default: uses LOG_LEVEL env var or INFO)

    Returns:
        Configured logger instance
    """
    logger = logging.getLogger(name)

    if level is None:
        level = LOG_LEVEL

    log_level = getattr(logging, level, logging.INFO)
    logger.setLevel(log_level)

    if logger.handlers:
        return logger

    formatter = logging.Formatter(
        fmt='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )

    console_handler = logging.StreamHandler(sys.stdout)
    console_handler.setLevel(log_level)
    console_handler.setFormatter(formatter)
    logger.addHandler(console_handler)

    try:
        log_path = Path(LOG_FILE)
        log_path.parent.mkdir(parents=True, exist_ok=True)

        file_handler = DiskAwareRotatingFileHandler(
            LOG_FILE,
            maxBytes=LOG_MAX_BYTES,
            backupCount=LOG_BACKUP_COUNT
        )
        file_handler.setLevel(log_level)
        file_handler.setFormatter(formatter)
        logger.addHandler(file_handler)
    except (OSError, PermissionError):
        pass

    return logger


def get_logger(name: str = 'ServerMonitor') -> logging.Logger:
    """
    Get or create a logger instance.

    Args:
        name: Logger name (default: 'ServerMonitor')

    Returns:
        Logger instance
    """
    logger = logging.getLogger(name)
    if not logger.handlers:
        return setup_logger(name)
    return logger


def safe_log(logger_func, message: str, *args, **kwargs):
    """
    Safely log a message, handling disk full errors gracefully.
    This ensures critical operations can continue even if logging fails.

    Args:
        logger_func: Logger method to call (e.g., logger.info, logger.warning)
        message: Message to log
        *args: Additional positional arguments for logger
        **kwargs: Additional keyword arguments for logger
    """
    try:
        logger_func(message, *args, **kwargs)
    except OSError as e:
        if e.errno == 28:
            print(f"[LOGGER] Disk full - could not log: {message}", file=sys.stderr)
        else:
            print(f"[LOGGER] Logging error: {e} - Message: {message}", file=sys.stderr)
    except Exception as e:
        print(f"[LOGGER] Unexpected logging error: {e} - Message: {message}", file=sys.stderr)

