"""
Utility functions for formatting and common operations.
"""

import psutil

CPU_COUNT = psutil.cpu_count(logical=True)

BYTES_PER_KB = 1024
BYTES_PER_MB = 1024 * 1024
BYTES_PER_GB = 1024 * 1024 * 1024
BYTES_PER_TB = 1024 * 1024 * 1024 * 1024


def format_bytes(bytes_value: int, precision: int = 2) -> str:
    """
    Convert bytes to human-readable format.
    Uses decimal conversion (1000 MB = 1 GB) for values >= 1000 MB for cleaner display.
    
    Args:
        bytes_value: Value in bytes
        precision: Number of decimal places (default: 2)
        
    Returns:
        Formatted string (e.g., "1.50 GB")
    """
    if bytes_value == 0:
        return "0 B"
    
    mb_value = bytes_value / BYTES_PER_MB
    
    if mb_value >= 1000:
        gb_value = mb_value / 1000.0
        if gb_value >= 1000:
            tb_value = gb_value / 1000.0
            if tb_value >= 1000:
                pb_value = tb_value / 1000.0
                if pb_value >= 1000:
                    eb_value = pb_value / 1000.0
                    return f"{eb_value:.{precision}f} EB"
                return f"{pb_value:.{precision}f} PB"
            return f"{tb_value:.{precision}f} TB"
        return f"{gb_value:.{precision}f} GB"
    
    for unit in ['B', 'KB', 'MB']:
        if bytes_value < BYTES_PER_KB:
            return f"{bytes_value:.{precision}f} {unit}"
        bytes_value /= BYTES_PER_KB
    return f"{bytes_value:.{precision}f} MB"


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
        if bytes_per_sec < BYTES_PER_KB:
            return f"{bytes_per_sec:.{precision}f} {unit}"
        bytes_per_sec /= BYTES_PER_KB
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
    if mb_value < BYTES_PER_KB:
        return f"{mb_value} MB"
    else:
        return f"{mb_value / BYTES_PER_KB:.{precision}f} GB"


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


def format_duration(seconds: float) -> str:
    """
    Format duration in seconds to human-readable format.
    
    Args:
        seconds: Duration in seconds
        
    Returns:
        Formatted string (e.g., "2 hours, 15 minutes, 30 seconds")
    """
    if seconds < 60:
        return f"{seconds:.1f} seconds"
    
    minutes = int(seconds // 60)
    secs = seconds % 60
    
    if minutes < 60:
        if secs < 1:
            return f"{minutes} minute{'s' if minutes != 1 else ''}"
        return f"{minutes} minute{'s' if minutes != 1 else ''}, {secs:.1f} second{'s' if secs != 1 else ''}"
    
    hours = minutes // 60
    mins = minutes % 60
    
    if hours < 24:
        parts = [f"{hours} hour{'s' if hours != 1 else ''}"]
        if mins > 0:
            parts.append(f"{mins} minute{'s' if mins != 1 else ''}")
        if secs >= 1:
            parts.append(f"{int(secs)} second{'s' if int(secs) != 1 else ''}")
        return ", ".join(parts)
    
    days = hours // 24
    hrs = hours % 24
    
    parts = [f"{days} day{'s' if days != 1 else ''}"]
    if hrs > 0:
        parts.append(f"{hrs} hour{'s' if hrs != 1 else ''}")
    if mins > 0:
        parts.append(f"{mins} minute{'s' if mins != 1 else ''}")
    if secs >= 1 and len(parts) < 3:
        parts.append(f"{int(secs)} second{'s' if int(secs) != 1 else ''}")
    
    return ", ".join(parts)
