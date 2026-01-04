"""
Utility functions for formatting and common operations.
"""

import psutil

CPU_COUNT = psutil.cpu_count(logical=True)


def format_bytes(bytes_value: int, precision: int = 2) -> str:
    """
    Convert bytes to human-readable format.
    
    Args:
        bytes_value: Value in bytes
        precision: Number of decimal places (default: 2)
        
    Returns:
        Formatted string (e.g., "1.50 GB")
    """
    if bytes_value == 0:
        return "0 B"
    
    for unit in ['B', 'KB', 'MB', 'GB', 'TB', 'PB']:
        if bytes_value < 1024.0:
            return f"{bytes_value:.{precision}f} {unit}"
        bytes_value /= 1024.0
    return f"{bytes_value:.{precision}f} EB"


def format_bytes_per_second(bytes_per_sec: float, precision: int = 2) -> str:
    """
    Format bytes per second to human-readable format.
    
    Args:
        bytes_per_sec: Bytes per second
        precision: Number of decimal places (default: 2)
        
    Returns:
        Formatted string (e.g., "1.50 MB/s")
    """
    if bytes_per_sec == 0:
        return "0 B/s"
    
    for unit in ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s']:
        if bytes_per_sec < 1024.0:
            return f"{bytes_per_sec:.{precision}f} {unit}"
        bytes_per_sec /= 1024.0
    return f"{bytes_per_sec:.{precision}f} PB/s"


def format_megabytes(mb_value: int, precision: int = 2) -> str:
    """
    Format megabytes to human-readable format.
    
    Args:
        mb_value: Value in megabytes
        precision: Number of decimal places (default: 2)
        
    Returns:
        Formatted string (e.g., "1.50 GB")
    """
    if mb_value < 1024:
        return f"{mb_value} MB"
    else:
        return f"{mb_value / 1024:.{precision}f} GB"


def format_temperature(temp_celsius: float, precision: int = 1) -> str:
    """
    Format temperature for display.
    
    Args:
        temp_celsius: Temperature in Celsius
        precision: Number of decimal places (default: 1)
        
    Returns:
        Formatted temperature string (e.g., "45.5°C")
    """
    if temp_celsius is None:
        return "N/A"
    return f"{temp_celsius:.{precision}f}°C"


def format_fan_speed(speed_rpm: int) -> str:
    """
    Format fan speed for display.
    
    Args:
        speed_rpm: Fan speed in RPM
        
    Returns:
        Formatted fan speed string (e.g., "1500 RPM")
    """
    if speed_rpm is None:
        return "N/A"
    return f"{speed_rpm} RPM"


def format_percentage(value: float, precision: int = 1) -> str:
    """
    Format percentage value.
    
    Args:
        value: Percentage value (0-100)
        precision: Number of decimal places (default: 1)
        
    Returns:
        Formatted percentage string (e.g., "45.5%")
    """
    return f"{value:.{precision}f}%"

