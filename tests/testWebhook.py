"""Tests for Discord webhook delivery, rate limiting and failure fallbacks."""

import requests
import pytest

from core.webhook import DiscordWebhook, RateLimiter


class FakeResponse:
    def __init__(self, status_code=204, headers=None):
        self.status_code = status_code
        self.headers = headers or {}

    def raise_for_status(self):
        if self.status_code >= 400:
            raise requests.exceptions.HTTPError(f"status {self.status_code}")


class FakeSession:
    def __init__(self, behaviour=None):
        self.calls = []
        self._behaviour = behaviour or (FakeResponse)

    def post(self, url, json=None, timeout=None):
        self.calls.append({'url': url, 'json': json, 'timeout': timeout})
        return self._behaviour()


@pytest.fixture
def webhook(tmp_path, monkeypatch):
    monkeypatch.setenv('WEBHOOK', 'https://discord.com/api/webhooks/1/token')
    monkeypatch.setenv('ALERT_LOG_PATH', str(tmp_path / 'alerts.log'))
    client = DiscordWebhook()
    client.alert_log_path = str(tmp_path / 'alerts.log')
    return client


def test_missing_url_raises(monkeypatch):
    monkeypatch.delenv('WEBHOOK', raising=False)

    with pytest.raises(ValueError, match='Webhook URL not provided'):
        DiscordWebhook(webhook_url=None)


def test_send_embed_posts_payload(webhook):
    session = FakeSession()
    webhook.session = session

    assert webhook.send_embed(title='CPU high', description='90%') is True
    assert len(session.calls) == 1
    assert session.calls[0]['json']['embeds'][0]['title'] == 'CPU high'


def test_send_embed_opens_circuit_after_repeated_failures(webhook):
    def always_timeout():
        raise requests.exceptions.Timeout('slow')

    webhook.session = FakeSession(behaviour=always_timeout)

    for _ in range(5):
        assert webhook.send_embed(title='CPU high') is False

    assert webhook.circuit_breaker.is_open() is True


def test_send_embed_skips_post_when_circuit_open(webhook):
    session = FakeSession()
    webhook.session = session
    webhook.circuit_breaker.failure_count = 99
    webhook.circuit_breaker.state = type(webhook.circuit_breaker.state).OPEN
    webhook.circuit_breaker.last_failure_time = float('inf')

    assert webhook.send_embed(title='CPU high') is False
    assert session.calls == []


def test_failed_send_writes_fallback_log(webhook, tmp_path):
    def always_fail():
        raise requests.exceptions.ConnectionError('no route')

    webhook.session = FakeSession(behaviour=always_fail)

    assert webhook.send_embed(title='Disk full', description='95% used') is False

    alert_log = tmp_path / 'alerts.log'
    assert alert_log.exists()
    assert 'Disk full' in alert_log.read_text()


def test_rate_limited_send_is_dropped_not_slept(webhook, tmp_path):
    session = FakeSession()
    webhook.session = session
    webhook.rate_limiter = RateLimiter(max_calls=2, period=600.0)

    assert webhook.send_embed(title='first') is True
    assert webhook.send_embed(title='second') is True
    assert webhook.send_embed(title='third') is False

    assert len(session.calls) == 2
    assert 'third' in (tmp_path / 'alerts.log').read_text()


def test_send_message_uses_circuit_breaker(webhook):
    session = FakeSession()
    webhook.session = session

    assert webhook.send_message('hello') is True
    assert session.calls[0]['json']['content'] == 'hello'


def test_http_429_is_dropped_without_opening_breaker(webhook):
    webhook.session = FakeSession(behaviour=lambda: FakeResponse(429, {'Retry-After': '5'}))

    assert webhook.send_embed(title='CPU high') is False
    assert webhook.circuit_breaker.is_open() is False


def test_embed_fields_are_truncated(webhook):
    payload = webhook.build_embed_payload(
        title='t' * 500,
        description='d' * 5000,
        fields=[{'name': 'n' * 500, 'value': 'v' * 2000}],
    )
    embed = payload['embeds'][0]

    assert len(embed['title']) == 256
    assert len(embed['description']) == 4096
    assert len(embed['fields'][0]['name']) == 256
    assert len(embed['fields'][0]['value']) == 1024


def test_rate_limiter_allows_up_to_max_calls():
    limiter = RateLimiter(max_calls=3, period=600.0)

    assert [limiter.try_acquire() for _ in range(4)] == [True, True, True, False]


def test_rate_limiter_reports_retry_after():
    limiter = RateLimiter(max_calls=1, period=600.0)
    limiter.try_acquire()

    assert limiter.retry_after() > 0


def test_rate_limiter_is_open_when_idle():
    limiter = RateLimiter(max_calls=2, period=600.0)

    assert limiter.retry_after() == 0.0
