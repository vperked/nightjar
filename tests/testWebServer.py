"""Integration tests for the dashboard HTTP server: auth, routing and path safety."""

import json
import socket
import threading
import time
import urllib.error
import urllib.request
from http.server import HTTPServer

import pytest

from core.web import server as web_server


class StubCpuMonitor:
    def get_cpu_percent(self, interval=0.0):
        return 12.5

    def get_cpu_percent_per_core(self, interval=0.0):
        return [10.0, 15.0]

    def get_cpu_freq(self):
        return {'current': 2400.0, 'min': 800.0, 'max': 3600.0}

    def get_cpu_load_average(self):
        return (0.1, 0.2, 0.3)

    def get_cpu_info(self, interval=0.0):
        return {
            'cpu_count_logical': 2,
            'cpu_count_physical': 2,
            'cpu_percent': 12.5,
            'cpu_percent_per_core': [10.0, 15.0],
        }


def _free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


@pytest.fixture
def running_server(monkeypatch, request):
    token = getattr(request, 'param', None)
    from core.config import settings as config
    config._env_cache.clear()

    if token:
        monkeypatch.setenv('WEB_AUTH_TOKEN', token)
    else:
        monkeypatch.delenv('WEB_AUTH_TOKEN', raising=False)

    web_server._state.cpu_monitor = StubCpuMonitor()
    handler = web_server.create_request_handler()
    port = _free_port()
    httpd = HTTPServer(('127.0.0.1', port), handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    time.sleep(0.05)

    yield f'http://127.0.0.1:{port}', token

    httpd.shutdown()
    httpd.server_close()
    thread.join(timeout=5)
    config._env_cache.clear()


def _get(url, token=None):
    request = urllib.request.Request(url)
    if token:
        request.add_header('Authorization', f'Bearer {token}')
    return urllib.request.urlopen(request, timeout=5)


@pytest.mark.parametrize('running_server', [None], indirect=True)
def test_health_endpoint_without_auth(running_server):
    base, _ = running_server

    response = _get(f'{base}/health')

    assert response.status == 200


@pytest.mark.parametrize('running_server', ['s3cret'], indirect=True)
def test_protected_endpoint_rejects_missing_token(running_server):
    base, _ = running_server

    with pytest.raises(urllib.error.HTTPError) as exc:
        _get(f'{base}/api/stats')

    assert exc.value.code == 401


@pytest.mark.parametrize('running_server', ['s3cret'], indirect=True)
def test_protected_endpoint_rejects_wrong_token(running_server):
    base, _ = running_server

    with pytest.raises(urllib.error.HTTPError) as exc:
        _get(f'{base}/api/stats', token='wrong')

    assert exc.value.code == 401


@pytest.mark.parametrize('running_server', ['s3cret'], indirect=True)
def test_protected_endpoint_accepts_valid_token(running_server):
    base, token = running_server

    response = _get(f'{base}/api/stats', token=token)
    payload = json.loads(response.read())

    assert response.status == 200
    assert isinstance(payload, dict)


@pytest.mark.parametrize('running_server', ['s3cret'], indirect=True)
def test_x_auth_token_header_is_accepted(running_server):
    base, token = running_server
    request = urllib.request.Request(f'{base}/api/stats')
    request.add_header('X-Auth-Token', token)

    response = urllib.request.urlopen(request, timeout=5)

    assert response.status == 200


@pytest.mark.parametrize('running_server', ['s3cret'], indirect=True)
def test_favicon_does_not_require_auth(running_server):
    base, _ = running_server

    response = _get(f'{base}/favicon.ico')

    assert response.status == 204


@pytest.mark.parametrize('running_server', [None], indirect=True)
def test_static_handler_rejects_traversal(running_server):
    base, _ = running_server

    with pytest.raises(urllib.error.HTTPError) as exc:
        _get(f'{base}/js/../../../../etc/passwd')

    assert exc.value.code in (403, 404)


@pytest.mark.parametrize('running_server', [None], indirect=True)
def test_unknown_route_returns_404(running_server):
    base, _ = running_server

    with pytest.raises(urllib.error.HTTPError) as exc:
        _get(f'{base}/admin')

    assert exc.value.code == 404


@pytest.mark.parametrize('running_server', [None], indirect=True)
def test_cors_is_not_wildcard_by_default(running_server):
    base, _ = running_server

    response = _get(f'{base}/health')

    assert response.headers.get('Access-Control-Allow-Origin') != '*'


def test_start_server_refuses_public_bind_without_token(monkeypatch, caplog):
    from core.config import settings as config
    config._env_cache.clear()
    monkeypatch.delenv('WEB_AUTH_TOKEN', raising=False)
    web_server._state.server_thread = None

    web_server.start_server(port=_free_port(), host='0.0.0.0')

    assert web_server._state.server_thread is None
