"""
Host capability detection and threshold auto-adaptation.
"""

from core.detection.detect import (
    analyze_system_usage_patterns,
    detect_system_config,
    apply_system_adaptations,
    prompt_save_config,
    save_auto_detected_config,
)

__all__ = [
    'analyze_system_usage_patterns',
    'apply_system_adaptations',
    'detect_system_config',
    'prompt_save_config',
    'save_auto_detected_config',
]
