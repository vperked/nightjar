"""
Secret redaction for log output.
"""

import logging
import re

_WEBHOOK_TOKEN = re.compile(r'(/api/webhooks/\d+/)[A-Za-z0-9_\-.~%]+')


def redact_secrets(text: str) -> str:
    """
    Mask secrets that commonly leak through exception messages.

    requests embeds the full request URL in connection errors, and a Discord
    webhook URL carries its token in the path.

    Args:
        text: Text about to be written to a log

    Returns:
        The text with webhook tokens replaced by a placeholder
    """
    return _WEBHOOK_TOKEN.sub(r'\1<redacted>', text)


class RedactingFormatter(logging.Formatter):
    """Formatter that redacts secrets from the final line, traceback included."""

    def format(self, record: logging.LogRecord) -> str:
        return redact_secrets(super().format(record))
