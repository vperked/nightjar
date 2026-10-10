import json
import threading
import urllib.request
from http.server import HTTPServer


from core.processes import monitor as process_module
from core.processes.monitor import ProcessMonitor
from core.web import server as web_server


class _Proc:
    def __init__(self, pid, cpu, mem):
        self.info = {'pid': pid, 'name': f'p{pid}', 'cpu_percent': cpu, 'memory_percent': mem, 'status': 'running'}


def _fake_iter(calls):
    def process_iter(attrs):
        calls.append(tuple(attrs))
        return [_Proc(1, 10.0, 1.0), _Proc(2, 80.0, 2.0), _Proc(3, 5.0, 50.0)]
    return process_iter


def test_callers_within_max_age_share_one_scan(monkeypatch):
    calls = []
    monkeypatch.setattr(process_module.psutil, 'process_iter', _fake_iter(calls))
    monkeypatch.setattr(ProcessMonitor, '_enrich_snapshots', lambda self, s: None)
    monitor = ProcessMonitor()

    dashboard = monitor.get_process_overview(limit=2, max_age=60)
    alert_loop = monitor.get_top_cpu_and_memory(limit=2)

    assert len(calls) == 1
    assert [p['pid'] for p in dashboard['top_cpu']] == [2, 1]
    assert [p['pid'] for p in alert_loop[1]] == [3, 2]
    assert dashboard['count'] == {'total': 3, 'by_status': {'running': 3}}


def test_stale_or_smaller_scan_is_refreshed(monkeypatch):
    calls = []
    monkeypatch.setattr(process_module.psutil, 'process_iter', _fake_iter(calls))
    monkeypatch.setattr(ProcessMonitor, '_enrich_snapshots', lambda self, s: None)
    monitor = ProcessMonitor()

    monitor.get_process_overview(limit=1, max_age=60)
    monitor.get_process_overview(limit=3, max_age=60)
    monitor.get_process_overview(limit=3, max_age=0)

    assert len(calls) == 3


def test_dashboard_reads_the_shared_scan(monkeypatch):
    class Processes:
        def get_process_overview(self, limit=5, max_age=0):
            row = {'pid': 7, 'name': 'x', 'cpu_percent': 1.0, 'memory_percent': 2.0, 'username': 'secret'}
            return {'count': {'total': 1, 'by_status': {}}, 'top_cpu': [row], 'top_memory': [row]}

    monkeypatch.delenv('WEB_AUTH_TOKEN', raising=False)
    from core.config import settings as config
    config._env_cache.clear()
    handler = web_server.create_request_handler({'processes': Processes()})
    httpd = HTTPServer(('127.0.0.1', 0), handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        port = httpd.server_address[1]
        stats = json.loads(urllib.request.urlopen(f'http://127.0.0.1:{port}/api/stats', timeout=5).read())
    finally:
        httpd.shutdown()
        httpd.server_close()

    assert stats['processes'] == {'total': 1, 'by_status': {}}
    assert stats['top_processes_cpu'] == [{'pid': 7, 'name': 'x', 'cpu_percent': 1.0, 'memory_percent': 2.0}]
