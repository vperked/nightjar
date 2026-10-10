"""
Characterization harness for core.runtime.run_monitor.

Runs the real monitoring loop against scripted fake monitors, a fake clock and recording
webhooks, and returns an ordered event list (log lines, webhook calls, cleanup calls).
The golden files in tests/golden pin that output so refactors of the loop can be proven
behaviour-preserving.
"""

import json
import logging
import re
import sys
import time
import traceback
from datetime import datetime
from pathlib import Path
from typing import Any, Callable, Optional

T0 = 1_700_000_000.0

_TS = re.compile(r'\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}')
_ELAPSED = re.compile(r'\((\d+\.\d+)s\)')


TMP_PATH = ['']


def normalize(text: str) -> str:
    """Strip wall-clock and temp-directory noise from a message."""
    if TMP_PATH[0]:
        text = text.replace(TMP_PATH[0], '<TMP>')
    return _ELAPSED.sub('(Ns)', _TS.sub('TS', text))


class StopRun(KeyboardInterrupt):
    """Raised by the CPU fake to end the loop through the normal Ctrl+C shutdown path."""


class Recorder:
    """Shared ordered event list."""

    def __init__(self):
        self.events: list[list] = []

    def add(self, *event: Any) -> None:
        self.events.append(list(event))


def _j(value: Any) -> str:
    return normalize(json.dumps(value, sort_keys=True, default=str))


class Script:
    """Per-scenario functions of the cycle number that drive every fake."""

    def __init__(self, **overrides: Callable):
        self.cpu: Callable = lambda c: [10.0, 12.0]
        self.memory: Callable = lambda c: 40.0
        self.gpu: Optional[Callable] = None
        self.temp: Callable = lambda c: 45.0
        self.network: Callable = lambda c: 1.0
        self.partitions: Callable = lambda c: [
            {'mountpoint': '/', 'device': '/dev/sda1', 'percent': 40.0, 'total': 100, 'used': 40, 'free': 60},
        ]
        self.processes: Callable = lambda c: ([], [])
        self.torrents: Optional[Callable] = None
        self.log_sizes: Callable = lambda c: {'exceeded_logs': [], 'threshold_formatted': '100.00 MB'}
        self.stability: Callable = lambda c: {'risk_level': 'low'}
        self.crashes: Callable = lambda c: {}
        self.frozen: Callable = lambda c: {'is_frozen': False, 'reasons': []}
        self.media_size: Callable = lambda c: None
        self.raise_in_cpu: Callable = lambda c: False
        self.boom: Callable = lambda name, c: False      # (monitor name, cycle) -> raise RuntimeError
        self.webhook_ok: Callable = lambda name, method, n: True
        self.signal_at: Optional[int] = None             # cycle at which SIGTERM is "delivered"
        self.sleep_raises_at: Optional[int] = None       # nth time.sleep() raises (loop crash path)
        for name, fn in overrides.items():
            setattr(self, name, fn)


class Clock:
    def __init__(self):
        self.now = T0

    def time(self) -> float:
        return self.now

    def sleep(self, seconds: float) -> None:
        self.now += seconds


class State:
    """Mutable cycle counter shared by the fakes."""

    def __init__(self, script: Script, recorder: Recorder, max_cycles: int):
        self.script = script
        self.rec = recorder
        self.max_cycles = max_cycles
        self.cycle = -1
        self.handlers: dict[Any, Any] = {}

    def boom(self, name: str) -> None:
        if self.script.boom(name, self.cycle):
            raise RuntimeError(f"{name} exploded")


def build_fakes(state: State) -> dict[str, Any]:
    s, rec = state.script, state.rec

    class Cpu:
        cpu_count = 2
        cpu_count_physical = 2

        def get_cpu_percent_per_core(self, interval=0.0):
            state.cycle += 1
            if state.cycle >= state.max_cycles:
                raise StopRun()
            if s.signal_at is not None and state.cycle == s.signal_at:
                import signal
                state.handlers[signal.SIGTERM](signal.SIGTERM, None)
            if s.raise_in_cpu(state.cycle):
                raise RuntimeError("cpu sensor exploded")
            return s.cpu(state.cycle)

        def get_cpu_freq(self):
            return {'current': 2400.0}

        def get_cpu_load_average(self):
            return (1.0, 1.0, 1.0)

    class Memory:
        def get_memory_info_human(self):
            return {'total': '16.00 GB'}

        def get_memory_info(self):
            state.boom('memory')
            return {'percent': s.memory(state.cycle), 'available': 1}

        def get_swap_info(self):
            return {'percent': 1.0}

    class System:
        def get_system_info(self):
            return {'hostname': 'testbox', 'platform': 'Linux', 'platform_release': '6.0', 'architecture': 'x86_64'}

        def get_system_summary(self):
            return {'uptime': '1 day'}

        def get_all_info(self, include_top_processes=False):
            return {'info': 'all'}

        def get_disk_io(self):
            state.boom('disk_io')
            return {'read_bytes': 1}

        def get_uptime(self):
            return {'formatted': '1 day'}

    class Gpu:
        nvidia_smi_available = s.gpu is not None

        def get_gpu_comprehensive(self, index):
            state.boom('gpu')
            return s.gpu(state.cycle) if s.gpu else {}

        def get_gpu_count(self):
            return 1

    class Disk:
        def get_all_partitions(self):
            state.boom('partitions')
            return s.partitions(state.cycle)

        def get_total_disk_usage(self, partitions=None):
            parts = partitions if partitions is not None else s.partitions(max(state.cycle, 0))
            top = max((p['percent'] for p in parts), default=0.0)
            return {'percent': top, 'total': 100, 'used': 50, 'free': 50}

        def get_disk_usage(self, path):
            parts = s.partitions(max(state.cycle, 0))
            top = max((p['percent'] for p in parts), default=0.0)
            return {'percent': top}

        def cleanup_logs_and_restart_services(self, log_monitor=None):
            state.boom('cleanup')
            rec.add('call', 'disk.cleanup_logs_and_restart_services')
            return {'logs_cleaned': 2, 'total_size_cleared': 2048}

        def emergency_cleanup_disk_space(self, mountpoint='/', log_monitor=None):
            state.boom('cleanup')
            if s.boom('nospace', state.cycle):
                return {'total_size_cleared': 0, 'files_removed': 0, 'dirs_removed': 0, 'logs_cleaned': 0,
                        'errors': []}
            rec.add('call', 'disk.emergency_cleanup', mountpoint)
            return {'total_size_cleared': 5000, 'files_removed': 3, 'dirs_removed': 1, 'logs_cleaned': 1,
                    'errors': ['x'], 'initial_usage': 97.0, 'final_usage': 96.0}

        def cleanup_disk_space(self, mountpoint='/', target_free_percent=20.0, log_monitor=None):
            state.boom('cleanup')
            if s.boom('nospace', state.cycle):
                return {'total_size_cleared': 0, 'files_removed': 0, 'dirs_removed': 0, 'logs_cleaned': 0,
                        'errors': []}
            rec.add('call', 'disk.cleanup_disk_space', mountpoint, target_free_percent)
            return {'total_size_cleared': 4096, 'files_removed': 2, 'dirs_removed': 1, 'logs_cleaned': 0,
                    'errors': [], 'initial_usage': 88.0, 'final_usage': 86.0}

        def get_largest_directories_human(self, path, limit=5):
            state.boom('largest')
            return [{'path': f'{path}/big', 'size': '1.00 GB'}]

        def get_directory_size(self, path):
            state.boom('media')
            return s.media_size(state.cycle)

        def get_subdirectories_sizes(self, path):
            return [{'path': f'{path}/a', 'size': 10}]

        def get_directory_size_human(self, path, size_info=None):
            return {'path': path, 'size': '10 B'}

    class Network:
        def get_bandwidth_usage(self, interval=1.0):
            state.boom('network')
            mb = s.network(state.cycle)
            if mb is None:
                return None
            return {'bytes_sent_per_sec': mb * 1024 * 1024 / 2, 'bytes_recv_per_sec': mb * 1024 * 1024 / 2}

        def get_total_network_io(self):
            return {'bytes_sent': 1}

    class Sensors:
        def is_available(self):
            return True

        def get_cpu_temperature(self):
            state.boom('temp')
            return s.temp(max(state.cycle, 0))

        def format_temperature(self, t):
            return f'{t}C'

        def get_fan_speeds(self):
            return [{'label': 'cpu_fan', 'current': 1200}]

    class Processes:
        def get_top_processes(self, limit=5, sort_by='cpu'):
            return s.processes(state.cycle)[0 if sort_by == 'cpu' else 1]

        def get_top_cpu_and_memory(self, limit=5):
            state.boom('processes')
            return s.processes(state.cycle)

    class Logs:
        threshold_bytes = 100 * 1024 * 1024

        def set_threshold(self, mb):
            rec.add('call', 'logs.set_threshold', mb)

        def cleanup_rotated_logs(self, force=False):
            rec.add('call', 'logs.cleanup_rotated_logs', force)
            return {'files_removed': 1 if force else 0, 'total_size_cleared': 100}

        def get_all_log_sizes(self):
            state.boom('logs')
            return dict(s.log_sizes(state.cycle), application_log=None, system_logs=[], journal_log=None)

        def get_all_log_files_in_directory(self, path):
            return [{'path': '/var/log/big.log', 'size_bytes': 300 * 1024 * 1024}]

        def get_log_size(self, path):
            if s.boom('logsize', state.cycle):
                return None
            size = 300 * 1024 * 1024 if not state.script.__dict__.get('_cleaned', {}).get(path) else 10
            return {'path': path, 'size_bytes': size, 'size_formatted': f'{size}B'}

        def cleanup_log(self, info, force=False):
            rec.add('call', 'logs.cleanup_log', info['path'], force)
            if s.boom('cleanlog_fail', state.cycle):
                return False
            state.script.__dict__.setdefault('_cleaned', {})[info['path']] = True
            return True

    class Kernel:
        def check_system_stability(self):
            state.boom('kernel')
            return s.stability(state.cycle)

        def check_kernel_crashes(self, since_minutes=60):
            state.boom('kernel')
            return s.crashes(state.cycle)

    class Qbit:
        password = 'x'
        last_fetch_ok = True

        def __init__(self):
            self.removed = set()

        def is_available(self):
            return True

        def get_torrent_summary(self):
            return {'total': 1, 'downloading': 1, 'seeding': 0}

        def get_all_torrents(self):
            state.boom('torrents')
            torrents = s.torrents(state.cycle) if s.torrents else []
            self.last_fetch_ok = torrents is not None
            return [t for t in (torrents or []) if t['hash'] not in self.removed]

        def get_downloading_torrents(self, torrents=None):
            return [t for t in (torrents or []) if t['state'] in ('downloading', 'stalleddl')]

        def get_seeding_torrents(self, torrents=None):
            return [t for t in (torrents or []) if t['state'] in ('uploading', 'stalledup')]

        def recover_stalled_torrent(self, h):
            rec.add('call', 'qbit.recover', h)
            return {'success': True, 'actions': ['reannounce', 'resume']}

        def handle_ratio_complete_torrent(self, h, delete_files=False):
            rec.add('call', 'qbit.remove', h, delete_files)
            self.removed.add(h)
            return {'success': True, 'actions': ['delete_files' if delete_files else 'delete']}

    class Freeze:
        def __init__(self, check_interval=5.0):
            pass

        def update_heartbeat(self):
            pass

        def update_metrics(self, **kw):
            rec.add('call', 'freeze.update_metrics', kw.get('cpu_percent'), kw.get('memory_percent'))

        def check_freeze_status(self):
            state.boom('freeze')
            return s.frozen(state.cycle)

        def set_freeze_threshold(self, v):
            pass

        def set_heartbeat_timeout(self, v):
            pass

        def stop_watchdog(self):
            rec.add('call', 'freeze.stop_watchdog')

    return {
        'cpu_monitor': Cpu(), 'memory_monitor': Memory(), 'system_monitor': System(), 'gpu_monitor': Gpu(),
        'disk_monitor': Disk(), 'network_monitor': Network(), 'sensors_monitor': Sensors(),
        'process_monitor': Processes(), 'log_monitor': Logs(), 'kernel_monitor': Kernel(),
        '_qbit': Qbit(), '_freeze': Freeze,
    }


class FakeWebhook:
    recorder: Recorder = None
    script: Script = None
    counts: dict[str, int] = {}

    def __init__(self, webhook_url=None):
        url = webhook_url or __import__('os').environ.get('WEBHOOK')
        if not url:
            raise ValueError("no webhook")
        self.name = url.rsplit('/', 1)[-1]

    def __getattr__(self, method):
        if method.startswith('_'):
            raise AttributeError(method)

        def call(*args, **kwargs):
            FakeWebhook.recorder.add('webhook', self.name, method, _j(args), _j(kwargs))
            key = f'{self.name}.{method}'
            FakeWebhook.counts[key] = FakeWebhook.counts.get(key, 0) + 1
            return FakeWebhook.script.webhook_ok(self.name, method, FakeWebhook.counts[key])
        return call


class FakeWatchdog:
    def __init__(self, **kw):
        pass

    def start(self):
        pass

    def stop(self):
        pass

    def update_heartbeat(self):
        pass

    def reset_alert_state(self):
        pass


class _LogCapture(logging.Handler):
    def __init__(self, recorder: Recorder):
        super().__init__(logging.DEBUG)
        self.recorder = recorder

    def emit(self, record: logging.LogRecord) -> None:
        self.recorder.add('log', record.levelname, record.name.replace('ServerMonitor.', ''),
                          normalize(record.getMessage()))


def run_scenario(monkeypatch, tmp_path, env: dict[str, str], script: Script, cycles: int,
                 qbit: bool = False, freeze: bool = False) -> list[list]:
    """Run run_monitor for `cycles` loop iterations and return the recorded events."""
    import atexit
    import importlib
    import os
    import pkgutil
    import signal

    import core.runtime
    from core.config import settings as config

    importlib.import_module('core.runtime.app')
    TMP_PATH[0] = str(tmp_path)
    recorder = Recorder()
    state = State(script, recorder, cycles)
    fakes = build_fakes(state)
    clock = Clock()

    monkeypatch.setenv('TZ', 'UTC')
    time.tzset()
    base_env = {
        'WEB_UI_ENABLED': 'false', 'WEBHOOK': 'https://discord.com/api/webhooks/1/main',
        'DISK_WEBHOOK': 'https://discord.com/api/webhooks/2/disk',
        'CRASH_WEBHOOK': 'https://discord.com/api/webhooks/3/crash',
        'CHECK_INTERVAL': '60', 'ERROR_LOG_FILE': str(tmp_path / 'errors.log'),
        'KERNEL_MONITOR_ENABLED': 'true', 'KERNEL_CHECK_INTERVAL': '2', 'KERNEL_STABILITY_CHECK_INTERVAL': '2',
        'LOG_CHECK_INTERVAL': '2', 'ALERT_COOLDOWN': '300', 'SYSTEM_UPDATE_ENABLED': 'false',
        'QBITTORRENT_MONITOR': 'true' if qbit else 'false',
        'QBITTORRENT_WEBHOOK': 'https://discord.com/api/webhooks/4/qbit' if qbit else '',
        'FREEZE_MONITOR_ENABLED': 'true' if freeze else 'false',
        'METRICS_REPORT_INTERVAL': '3000',
    }
    # python-dotenv loads the developer's real .env at import time; clear every documented
    # setting so results do not depend on it.
    documented = Path(__file__).resolve().parent.parent / 'env.recommended'
    for key in re.findall(r'^#?\s*([A-Z][A-Z0-9_]+)=', documented.read_text(), re.M):
        monkeypatch.delenv(key, raising=False)
    for key, value in {**base_env, **env}.items():
        if value == '':
            monkeypatch.delenv(key, raising=False)
        else:
            monkeypatch.setenv(key, value)
    config._env_cache.clear()
    config._typed_env_cache.clear()

    FakeWebhook.recorder = recorder
    FakeWebhook.script = script
    FakeWebhook.counts = {}
    sleeps = [0]

    def fake_sleep(seconds):
        sleeps[0] += 1
        if script.sleep_raises_at is not None and sleeps[0] == script.sleep_raises_at:
            raise RuntimeError("sleep exploded")
        clock.sleep(seconds)

    # Real tracebacks embed file paths and line numbers, which change whenever the code moves.
    monkeypatch.setattr(traceback, 'format_exc', lambda *a, **k: f"<traceback {type(sys.exc_info()[1]).__name__}>\n")
    monkeypatch.setattr(time, 'time', clock.time)
    monkeypatch.setattr(time, 'sleep', fake_sleep)
    monkeypatch.setattr(signal, "signal", state.handlers.__setitem__)
    monkeypatch.setattr(atexit, 'register', lambda *a, **k: None)

    import threading
    import types

    class SyncThread:
        """Runs the target inline so thread output is ordered deterministically."""

        def __init__(self, target=None, daemon=None, name=None, **kw):
            self.target = target

        def start(self):
            self.target()

        def is_alive(self):
            return False

    threading_shim = types.SimpleNamespace(Lock=threading.Lock, Thread=SyncThread)

    detection = {'adaptations': []}
    replacements = {
        'DiscordWebhook': FakeWebhook,
        'WatchdogMonitor': FakeWatchdog,
        'FreezeMonitor': fakes['_freeze'],
        'detect_system_config': lambda skip_usage_analysis=False: detection,
        'apply_system_adaptations': lambda cfg: {},
        'clear_cache_on_exit': lambda verbose=True: {},
        'run_system_update': lambda **kw: {'ok': True, 'message': 'ok'},
        'threading': threading_shim,
        'start_server': lambda port=0, host='': recorder.add('call', 'web.start_server', port, host),
        'stop_server': lambda timeout=5.0: recorder.add('call', 'web.stop_server'),
        'set_monitors': lambda *a, **k: recorder.add('call', 'web.set_monitors'),
        'set_crash_webhook': lambda w: recorder.add('call', 'web.set_crash_webhook'),
    }
    for info in pkgutil.walk_packages(core.runtime.__path__, 'core.runtime.'):
        module = importlib.import_module(info.name)
        for name, value in replacements.items():
            if hasattr(module, name):
                monkeypatch.setattr(module, name, value)

    capture = _LogCapture(recorder)
    root = logging.getLogger('ServerMonitor')
    old_level = root.level
    root.setLevel(logging.DEBUG)
    root.addHandler(capture)
    try:
        kwargs = {k: v for k, v in fakes.items() if not k.startswith('_')}
        if qbit:
            kwargs['qbittorrent_monitor'] = fakes['_qbit']
        core.runtime.run_monitor(**kwargs)
    finally:
        root.removeHandler(capture)
        root.setLevel(old_level)
        config._env_cache.clear()
        config._typed_env_cache.clear()

    _ = (os, datetime, sys)
    return recorder.events
