"""
Discord webhook client with rate limiting and circuit breaking.
"""

from core.webhook.client import (
    RateLimitedError,
    RateLimiter,
    DiscordWebhook,
)

__all__ = [
    'DiscordWebhook',
    'RateLimitedError',
    'RateLimiter',
]
