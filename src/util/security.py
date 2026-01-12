"""
Security utilities for path validation and sanitization.
Provides functions to prevent path traversal attacks and validate user inputs,
environment variables, and subprocess paths.
"""

import os
import re
from pathlib import Path
from typing import Optional, List, Tuple
from logger import get_logger

logger = get_logger('Security')


class SecurityError(Exception):
    """Raised when a security validation fails."""
    pass


def validate_path(path: str, allowed_base_paths: Optional[List[str]] = None, 
                 allow_symlinks: bool = False) -> Tuple[bool, Optional[str], Optional[str]]:
    """
    Validate a file path to prevent path traversal attacks.
    
    Args:
        path: Path to validate
        allowed_base_paths: List of allowed base paths (if None, only checks for traversal)
        allow_symlinks: Whether to allow symlinks (default: False for security)
        
    Returns:
        Tuple of (is_valid, resolved_path, error_message)
        - is_valid: True if path is safe
        - resolved_path: Real resolved path if valid, None otherwise
        - error_message: Error message if invalid, None otherwise
    """
    if not path or not isinstance(path, str):
        return False, None, "Path must be a non-empty string"
    
    if '..' in path or path.startswith('/') and not path.startswith('//'):
        normalized = os.path.normpath(path)
        if '..' in normalized or normalized != os.path.normpath(path):
            return False, None, "Path contains traversal sequences (..)"
    
    if '\x00' in path:
        return False, None, "Path contains null bytes"
    
    suspicious_patterns = [
        r'[;&|`$()]',  # Command injection characters
        r'<|>',  # Redirection characters
    ]
    for pattern in suspicious_patterns:
        if re.search(pattern, path):
            return False, None, f"Path contains suspicious characters: {pattern}"
    
    try:
        resolved = os.path.realpath(path)
        
        if not allow_symlinks:
            if os.path.islink(path):
                return False, None, "Symlinks are not allowed"
        
        if allowed_base_paths:
            resolved_allowed = [os.path.realpath(base) for base in allowed_base_paths if os.path.exists(base)]
            is_allowed = any(
                resolved == base or resolved.startswith(base + os.sep)
                for base in resolved_allowed
            )
            if not is_allowed:
                return False, None, f"Path is outside allowed directories: {resolved}"
        
        return True, resolved, None
        
    except (OSError, ValueError) as e:
        return False, None, f"Error resolving path: {str(e)}"


def sanitize_path(path: str, base_path: Optional[str] = None) -> Optional[str]:
    """
    Sanitize a path by removing dangerous characters and resolving it.
    
    Args:
        path: Path to sanitize
        base_path: Base path to restrict to (optional)
        
    Returns:
        Sanitized path or None if invalid
    """
    if not path:
        return None
    
    path = path.replace('\x00', '')
    
    try:
        normalized = os.path.normpath(path)
    except (ValueError, OSError):
        return None
    
    if base_path:
        try:
            base_real = os.path.realpath(base_path)
            path_real = os.path.realpath(normalized)
            
            if not path_real.startswith(base_real + os.sep) and path_real != base_real:
                return None
        except (OSError, ValueError):
            return None
    
    return normalized


def validate_environment_variable(key: str, value: str, 
                                 max_length: int = 10000,
                                 allowed_patterns: Optional[List[str]] = None,
                                 blocked_patterns: Optional[List[str]] = None) -> Tuple[bool, Optional[str]]:
    """
    Validate an environment variable value for security.
    
    Args:
        key: Environment variable key
        value: Environment variable value
        max_length: Maximum allowed length
        allowed_patterns: List of regex patterns that are allowed (whitelist)
        blocked_patterns: List of regex patterns that are blocked (blacklist)
        
    Returns:
        Tuple of (is_valid, error_message)
    """
    if not value:
        return True, None
    
    if len(value) > max_length:
        return False, f"Environment variable {key} exceeds maximum length ({max_length})"
    
    if blocked_patterns is None:
        blocked_patterns = [
            r'[;&|`$()<>]',  # Command injection characters
            r'<script',  # XSS attempts
            r'javascript:',  # JavaScript injection
            r'eval\s*\(',  # Code execution attempts
            r'exec\s*\(',  # Code execution attempts
            r'__import__',  # Python import injection
        ]
    
    for pattern in blocked_patterns:
        if re.search(pattern, value, re.IGNORECASE):
            return False, f"Environment variable {key} contains blocked pattern: {pattern}"
    
    if allowed_patterns:
        is_allowed = any(re.match(pattern, value) for pattern in allowed_patterns)
        if not is_allowed:
            return False, f"Environment variable {key} does not match allowed patterns"
    
    return True, None


def safe_subprocess_path(path: str) -> Optional[str]:
    """
    Prepare a path for safe use in subprocess calls.
    
    Args:
        path: Path to prepare
        
    Returns:
        Safe path string or None if invalid
    """
    is_valid, resolved_path, error = validate_path(path, allow_symlinks=False)
    
    if not is_valid:
        logger.warning(f"Unsafe path rejected for subprocess: {error}")
        return None
    
    if re.search(r'[;&|`$()<>]', resolved_path):
        logger.warning(f"Path contains shell metacharacters: {resolved_path}")
        return None
    
    return resolved_path

