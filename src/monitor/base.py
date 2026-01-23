"""
Base monitor class with common functionality.

Provides shared patterns for threshold checking, initialization, and error handling.
"""

from typing import Optional, Dict, Any
from abc import ABC, abstractmethod
from util.error_handling import MonitorError, MonitorInitializationError
from logger import get_logger

logger = get_logger()


class BaseMonitor(ABC):
    """Base class for all monitor modules with common functionality."""

    def __init__(self, name: str):
        """
        Initialize the base monitor.

        Args:
            name: Name of the monitor (e.g., 'CPU', 'Memory', 'Disk')
        """
        self.name = name
        self._initialized = False
        try:
            self._initialize()
            self._initialized = True
        except Exception as e:
            raise MonitorInitializationError(f"Failed to initialize {name} monitor: {e}") from e

    @abstractmethod
    def _initialize(self) -> None:
        """
        Initialize monitor-specific resources.

        Subclasses should implement this method to perform their initialization.
        Raises MonitorInitializationError if initialization fails.
        """
        pass

    def is_initialized(self) -> bool:
        """
        Check if the monitor has been successfully initialized.

        Returns:
            True if initialized, False otherwise
        """
        return self._initialized

    def check_threshold(self, value: float, threshold: float) -> bool:
        """
        Check if a value exceeds a threshold.

        Args:
            value: Current value to check
            threshold: Threshold value to compare against

        Returns:
            True if value >= threshold, False otherwise
        """
        return value >= threshold

    def check_threshold_below(self, value: float, threshold: float) -> bool:
        """
        Check if a value is below a threshold.

        Args:
            value: Current value to check
            threshold: Threshold value to compare against

        Returns:
            True if value <= threshold, False otherwise
        """
        return value <= threshold

    def get_name(self) -> str:
        """
        Get the monitor name.

        Returns:
            Monitor name
        """
        return self.name

    def validate_value(self, value: float, min_val: Optional[float] = None, max_val: Optional[float] = None) -> bool:
        """
        Validate that a value is within acceptable bounds.

        Args:
            value: Value to validate
            min_val: Optional minimum allowed value
            max_val: Optional maximum allowed value

        Returns:
            True if value is within bounds, False otherwise
        """
        if min_val is not None and value < min_val:
            return False
        if max_val is not None and value > max_val:
            return False
        return True


class ThresholdChecker:
    """Utility class for threshold validation and checking."""

    @staticmethod
    def exceeds_threshold(value: float, threshold: float) -> bool:
        """
        Check if a value exceeds a threshold.

        Args:
            value: Current value to check
            threshold: Threshold value to compare against

        Returns:
            True if value >= threshold, False otherwise
        """
        return value >= threshold

    @staticmethod
    def below_threshold(value: float, threshold: float) -> bool:
        """
        Check if a value is below a threshold.

        Args:
            value: Current value to check
            threshold: Threshold value to compare against

        Returns:
            True if value <= threshold, False otherwise
        """
        return value <= threshold

    @staticmethod
    def within_range(value: float, min_val: float, max_val: float) -> bool:
        """
        Check if a value is within a range.

        Args:
            value: Value to check
            min_val: Minimum allowed value (inclusive)
            max_val: Maximum allowed value (inclusive)

        Returns:
            True if value is within range, False otherwise
        """
        return min_val <= value <= max_val

    @staticmethod
    def clamp_value(value: float, min_val: float, max_val: float) -> float:
        """
        Clamp a value to be within a range.

        Args:
            value: Value to clamp
            min_val: Minimum allowed value
            max_val: Maximum allowed value

        Returns:
            Clamped value within [min_val, max_val] range
        """
        return max(min_val, min(max_val, value))
