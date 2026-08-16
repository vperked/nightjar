"""
Discord webhook client with rate limiting and circuit breaking.
"""

from core.webhook.client import (
    RateLimiter,
    DiscordWebhook,
)

__all__ = [
    'DiscordWebhook',
    'RateLimiter',
]
