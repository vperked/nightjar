"""
Golden-output tests pinning the behaviour of the monitoring loop.

Regenerate after an intentional behaviour change with:  UPDATE_GOLDEN=1 pytest tests/testRunMonitorGolden.py
"""

import json
import os
from pathlib import Path

import pytest

from runMonitorHarness import Script, run_scenario

GOLDEN_DIR = Path(__file__).parent / 'golden'


def _check(name, events):
    path = GOLDEN_DIR / f'{name}.json'
    lines = [json.dumps(event, sort_keys=True) for event in events]
    if os.environ.get('UPDATE_GOLDEN'):
        path.write_text('\n'.join(lines) + '\n')
        return
    expected = path.read_text().splitlines()
    for index, (got, want) in enumerate(zip(lines, expected)):
        assert got == want, f'{name}: first difference at event {index}\n got: {got}\nwant: {want}'
    assert len(lines) == len(expected), f'{name}: {len(lines)} events, expected {len(expected)}'


def _part(mount, percent, nvme=False):
    return {'mountpoint': mount, 'device': f'/dev{mount}', 'percent': percent, 'total': 1000, 'used': 500,
            'free': 500, 'is_nvme': nvme}


def test_quiet_system(monkeypatch, tmp_path):
    events = run_scenario(monkeypatch, tmp_path, {}, Script(), cycles=12)
    _check('quiet', events)


def test_quiet_system_without_webhooks(monkeypatch, tmp_path):
    env = {'WEBHOOK': '', 'DISK_WEBHOOK': '', 'CRASH_WEBHOOK': ''}
    script = Script(cpu=lambda c: [95.0, 99.0], memory=lambda c: 91.0, temp=lambda c: 90.0,
                    network=lambda c: 2000.0, partitions=lambda c: [_part('/', 82.0)])
    events = run_scenario(monkeypatch, tmp_path, env, script, cycles=14)
    _check('no_webhooks', events)


def test_resource_pressure_with_backoff_and_recovery(monkeypatch, tmp_path):
    def cpu(c):
        return [95.0, 99.0] if c < 50 else [5.0, 5.0]

    def mem(c):
        return 92.0 if 5 <= c < 45 else 30.0

    def gpu(c):
        util = 90.0 if c < 40 else 10.0
        return {'utilization': {'gpu': util}, 'info': {'name': 'Fake GPU'}, 'memory': {'used': 1},
                'temperature': 70, 'power': 100}

    def temp(c):
        return 90.0 if 3 <= c < 45 else 40.0

    def net(c):
        return 2000.0 if c < 30 else 1.0

    procs = ([{'pid': 11, 'name': 'burner', 'cpu_percent': 80.0, 'memory_percent': 5.0}],
             [{'pid': 12, 'name': 'hog', 'cpu_percent': 1.0, 'memory_percent': 30.0}])
    script = Script(cpu=cpu, memory=mem, gpu=gpu, temp=temp, network=net, processes=lambda c: procs)
    env = {'TEMP_ALERT_DURATION': '120', 'ALERT_MAX_COOLDOWN': '3600'}
    events = run_scenario(monkeypatch, tmp_path, env, script, cycles=80)
    _check('resource_pressure', events)


def test_disk_pressure_cleanup_and_alerts(monkeypatch, tmp_path):
    def parts(c):
        if c < 30:
            return [_part('/', 96.0), _part('/data', 88.0), _part('/mnt/nvme', 78.0, nvme=True)]
        if c < 45:
            return [_part('/', 90.0), _part('/data', 70.0)]
        return [_part('/', 40.0), _part('/data', 40.0)]

    events = run_scenario(monkeypatch, tmp_path, {}, Script(partitions=parts), cycles=60)
    _check('disk_pressure', events)


def test_disk_total_when_partitions_unavailable(monkeypatch, tmp_path):
    script = Script(partitions=lambda c: [])
    # no partitions -> total disk usage is 0, so force the "overall" alert path via threshold
    events = run_scenario(monkeypatch, tmp_path, {'DISK_THRESHOLD': '0'}, script, cycles=6)
    _check('disk_total_only', events)


def test_torrent_lifecycle(monkeypatch, tmp_path):
    def torrent(h, state, **kw):
        base = {'hash': h, 'name': f'T-{h}', 'state': state, 'dlspeed': 0, 'upspeed': 0, 'progress': 0.3,
                'ratio': 0.0, 'size': 100, 'eta': 0, 'seeds': 1, 'peers': 1, 'save_path': '/x',
                'downloaded': 1, 'uploaded': 1, 'category': '', 'progress_percent': 30.0}
        base.update(kw)
        return base

    def torrents(c):
        if c == 9:
            return None
        items = [
            torrent('stall', 'stalleddl'),
            torrent('ratio', 'uploading', progress=1.0, ratio=1.5, progress_percent=100.0),
            torrent('paused', 'pausedup', progress=1.0, ratio=0.1, progress_percent=100.0),
            torrent('error', 'error'),
        ]
        if c < 8:
            items.append(torrent('dl', 'downloading', dlspeed=100))
        else:
            items.append(torrent('dl', 'uploading', progress=1.0, ratio=0.1, progress_percent=100.0))
        return items

    env = {
        'STALLED_TORRENT_THRESHOLD': '60', 'STALLED_TORRENT_ACTION_COOLDOWN': '60',
        'SEEDING_RATIO_LIMIT': '1.0', 'INACTIVE_TORRENT_AUTO_REMOVE': 'true',
        'SEEDING_RATIO_DELETE_FILES': 'true', 'SEEDING_RATIO_ACTION_COOLDOWN': '60',
    }
    events = run_scenario(monkeypatch, tmp_path, env, Script(torrents=torrents), cycles=30, qbit=True)
    _check('torrents', events)


def test_torrents_default_policy_is_conservative(monkeypatch, tmp_path):
    def torrents(c):
        return [{'hash': 'p', 'name': 'paused', 'state': 'pausedup', 'dlspeed': 0, 'upspeed': 0, 'progress': 1.0,
                 'ratio': 5.0, 'size': 1, 'eta': 0, 'seeds': 0, 'peers': 0, 'save_path': '/x', 'downloaded': 1,
                 'uploaded': 1, 'category': '', 'progress_percent': 100.0}]

    events = run_scenario(monkeypatch, tmp_path, {}, Script(torrents=torrents), cycles=8, qbit=True)
    assert not [e for e in events if e[0] == 'call' and e[1] == 'qbit.remove']
    _check('torrents_default', events)


def test_logs_kernel_media_and_freeze(monkeypatch, tmp_path):
    media = tmp_path / 'media'
    media.mkdir()

    def sizes(c):
        exceeded = [{'path': '/var/log/app.log', 'size_bytes': 300 * 1024 * 1024}] if 2 <= c < 12 else []
        return {'exceeded_logs': exceeded, 'threshold_formatted': '100.00 MB'}

    def stability(c):
        if 3 <= c < 9:
            return {'risk_level': 'high', 'risk_score': 70, 'indicators': ['a', 'b'], 'recommendations': ['fix']}
        if 9 <= c < 13:
            return {'risk_level': 'critical', 'risk_score': 90, 'indicators': ['c'], 'recommendations': []}
        return {'risk_level': 'low'}

    def crashes(c):
        if c == 3:
            return {'unexpected_reboot': {'previous_boot_time': 1_699_000_000, 'current_boot_time': 1_699_900_000}}
        if c == 5:
            return {'early_warnings': [{'severity': 'critical', 'keyword': 'MCE', 'message': 'machine check'}],
                    'hardware_errors': [
                        {'type': 'high_interrupts', 'device': 'eth0', 'count': 2_500_000, 'severity': 'critical'},
                        {'source': 'nvme', 'count_increase': 3, 'count': 9, 'error_type': 'io', 'severity': 'warning'},
                        {'source': 'ata', 'count': 4, 'error_type': 'crc', 'device': 'sda'},
                        {'source': 'edac', 'message': 'x' * 300},
                        {'type': 'misc', 'source': 'pci'},
                        {'weird': True},
                    ]}
        if c == 9:
            return {'crashes_found': [{'keyword': 'panic', 'message': 'Kernel panic', 'source': 'dmesg'},
                                      {'keyword': 'oops', 'message': 'Oops', 'source': 'journal'}]}
        return {}

    def frozen(c):
        return {'is_frozen': 5 <= c < 9, 'reasons': ['no heartbeat']}

    def media_size(c):
        return {'size': 900 * 1024 ** 3} if c >= 3 else {'size': 1}

    env = {
        'MEDIA_MONITOR_ENABLED': 'true', 'MEDIA_DIRECTORY': str(media), 'MEDIA_CHECK_INTERVAL': '2',
        'MEDIA_DIRECTORY_THRESHOLD_GB': '500', 'FREEZE_MONITOR_ENABLED': 'true',
    }
    script = Script(log_sizes=sizes, stability=stability, crashes=crashes, frozen=frozen, media_size=media_size)
    events = run_scenario(monkeypatch, tmp_path, env, script, cycles=24, freeze=True)
    _check('logs_kernel_media_freeze', events)


def test_aggressive_log_cleanup_when_disk_full(monkeypatch, tmp_path):
    script = Script(partitions=lambda c: [_part('/', 93.0)])
    events = run_scenario(monkeypatch, tmp_path, {}, script, cycles=14)
    _check('aggressive_logs', events)


def test_errors_are_contained_and_reported(monkeypatch, tmp_path):
    script = Script(raise_in_cpu=lambda c: c in (2, 3))
    events = run_scenario(monkeypatch, tmp_path, {}, script, cycles=8)
    _check('errors', events)


@pytest.mark.parametrize('name', ['quiet', 'resource_pressure', 'disk_pressure', 'torrents', 'logs_kernel_media_freeze'])
def test_goldens_are_not_trivial(name):
    if os.environ.get('UPDATE_GOLDEN'):
        pytest.skip('regenerating')
    assert len((GOLDEN_DIR / f'{name}.json').read_text().splitlines()) > 20


def _hot(c):
    return [97.0, 99.0]


def _torrent(h, state, **kw):
    base = {'hash': h, 'name': f'T-{h}', 'state': state, 'dlspeed': 0, 'upspeed': 0, 'progress': 0.3,
            'ratio': 0.0, 'size': 100, 'eta': 0, 'seeds': 1, 'peers': 1, 'save_path': '/x',
            'downloaded': 1, 'uploaded': 1, 'category': '', 'progress_percent': 30.0}
    base.update(kw)
    return base


def test_webhook_failures_and_monitor_exceptions(monkeypatch, tmp_path):
    media = tmp_path / 'media'
    media.mkdir()
    breaks = {
        'memory': {3, 4}, 'gpu': {5}, 'partitions': {6}, 'network': {7, 8}, 'temp': {9}, 'disk_io': {10},
        'processes': {11, 12}, 'logs': {13}, 'kernel': {14, 15}, 'torrents': {16}, 'freeze': {17},
        'media': {18}, 'cleanup': {19}, 'largest': {20, 21}, 'cleanlog_fail': {22, 23}, 'logsize': {24},
    }

    def parts(c):
        return [_part('/', 96.0), _part('/data', 88.0), _part('/mnt/nvme', 78.0, nvme=True)]

    def gpu(c):
        return {'utilization': {'gpu': 95.0}, 'info': {}, 'memory': {}, 'temperature': 1, 'power': 1}

    procs = ([{'pid': 1, 'name': 'a', 'cpu_percent': 90.0, 'memory_percent': 1.0}],
             [{'pid': 2, 'name': 'b', 'cpu_percent': 1.0, 'memory_percent': 50.0}])
    torrents = lambda c: [_torrent('s', 'stalleddl'), _torrent('r', 'uploading', progress=1.0, ratio=3.0)]  # noqa: E731
    script = Script(
        cpu=_hot, memory=lambda c: 95.0, gpu=gpu, temp=lambda c: 95.0, network=lambda c: 3000.0,
        partitions=parts, processes=lambda c: procs, torrents=torrents,
        log_sizes=lambda c: {'exceeded_logs': [{'path': '/var/log/a.log', 'size_bytes': 10 ** 9}],
                             'threshold_formatted': '100 MB'},
        stability=lambda c: {'risk_level': 'critical', 'risk_score': 99, 'indicators': ['i'], 'recommendations': []},
        crashes=lambda c: {'crashes_found': [{'keyword': 'panic', 'message': 'm', 'source': 's'}]},
        frozen=lambda c: {'is_frozen': True, 'reasons': ['r']}, media_size=lambda c: {'size': 10 ** 13},
        boom=lambda name, c: c in breaks.get(name, ()),
        webhook_ok=lambda name, method, n: False,
    )
    env = {'MEDIA_MONITOR_ENABLED': 'true', 'MEDIA_DIRECTORY': str(media), 'MEDIA_CHECK_INTERVAL': '1',
           'FREEZE_MONITOR_ENABLED': 'true', 'TEMP_ALERT_DURATION': '0', 'INACTIVE_TORRENT_AUTO_REMOVE': 'true',
           'SEEDING_RATIO_LIMIT': '1', 'STALLED_TORRENT_THRESHOLD': '30', 'STALLED_TORRENT_ACTION_COOLDOWN': '60'}
    events = run_scenario(monkeypatch, tmp_path, env, script, cycles=40, qbit=True, freeze=True)
    _check('chaos', events)


def test_emergency_cleanup_with_nothing_to_free_and_with_errors(monkeypatch, tmp_path):
    script = Script(partitions=lambda c: [_part('/', 98.0)],
                    boom=lambda name, c: (name == 'nospace' and c < 6) or (name == 'cleanup' and 6 <= c < 10))
    events = run_scenario(monkeypatch, tmp_path, {}, script, cycles=16)
    _check('emergency_edge_cases', events)


def test_graceful_signal_shutdown_sends_notification(monkeypatch, tmp_path):
    script = Script(signal_at=5)
    events = run_scenario(monkeypatch, tmp_path, {'WEB_UI_ENABLED': 'true', 'FREEZE_MONITOR_ENABLED': 'true'},
                          script, cycles=20, freeze=True)
    assert any(e[0] == 'webhook' and e[2] == 'send_shutdown_notification' for e in events)
    _check('graceful_shutdown', events)


def test_loop_crash_is_reported(monkeypatch, tmp_path):
    script = Script(sleep_raises_at=4)
    events = run_scenario(monkeypatch, tmp_path, {'WEB_UI_ENABLED': 'true', 'FREEZE_MONITOR_ENABLED': 'true'},
                          script, cycles=20, freeze=True)
    assert any(e[0] == 'webhook' and e[3] != '' and 'Crashed' in e[4] for e in events)
    _check('loop_crash', events)


def test_system_update_job_runs_when_due(monkeypatch, tmp_path):
    env = {'SYSTEM_UPDATE_ENABLED': 'true', 'SYSTEM_UPDATE_INTERVAL_SECONDS': '300', 'SYSTEM_UPDATE_APPLY': 'true'}
    events = run_scenario(monkeypatch, tmp_path, env, Script(), cycles=14)
    assert any('SYSTEM_UPDATE' in str(e) for e in events)
    _check('system_update', events)


def test_qbittorrent_startup_without_active_torrents(monkeypatch, tmp_path):
    events = run_scenario(monkeypatch, tmp_path, {}, Script(torrents=lambda c: []), cycles=6, qbit=True)
    _check('qbit_idle', events)


def test_qbittorrent_startup_failure_is_contained(monkeypatch, tmp_path):
    script = Script(torrents=lambda c: [_torrent('a', 'downloading', dlspeed=5)],
                    boom=lambda name, c: name == 'torrents' and c == -1)
    events = run_scenario(monkeypatch, tmp_path, {}, script, cycles=6, qbit=True)
    _check('qbit_startup_failure', events)
