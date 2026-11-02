// Demo data for the dashboard when no backend is running (see transport in client.ts). Readings
// are generated from the current time so charts move; checks and settings can be edited and are kept
// for the browser tab. Production builds never include this file.
import type {
  AlertRules,
  AppConfig,
  AppJob,
  Beat,
  CheckSpec,
  ChecksResponse,
  HistoryPoint,
  HistoryResponse,
  LogEntry,
  LogsResponse,
  Mute,
  NetInterface,
  NetworkResponse,
  Notification,
  NotificationCounts,
  NotificationRules,
  NotificationsResponse,
  PlexPlay,
  PlexResponse,
  PlexSample,
  PlexStream,
  PublicStatus,
  QBResponse,
  QBTorrent,
  QuietHours,
  Service,
  ServicesResponse,
  SettingField,
  SettingsResponse,
  SetupStatus,
  StatusDesign,
  Unit,
  Stats,
  StoreApp,
  UpdateSpec,
  UpdateTarget,
  UpdatesResponse,
} from './types';

const GB = 1024 ** 3;
const now = () => Date.now() / 1000;

// noise is a repeatable 0..1 value for (n, seed), so the same moment always draws the same reading.
function noise(n: number, seed = 1): number {
  const x = Math.sin(n * 12.9898 + seed * 78.233) * 43758.5453;
  return x - Math.floor(x);
}

// wave is a smooth daily-ish rhythm with jitter around base.
function wave(t: number, base: number, amplitude: number, period: number, seed: number, jitter = 0.15): number {
  return base + amplitude * Math.sin((t / period) * 2 * Math.PI + seed) + base * jitter * (noise(Math.floor(t / 5), seed) - 0.5);
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
const round = (v: number, places = 1) => Math.round(v * 10 ** places) / 10 ** places;

// ---------- editable state, kept for the tab ----------

interface DemoState {
  checks: CheckSpec[];
  settings: Record<string, string>;
  updates?: UpdateTarget[];
  statusDesign?: StatusDesign;
}

const STORE_KEY = 'servermonitor.demo';

const check = (over: Partial<CheckSpec> & Pick<CheckSpec, 'name' | 'type' | 'target'>): CheckSpec => ({
  interval: 60,
  timeout: 10,
  retries: 2,
  expected_status: null,
  keyword: '',
  verify_tls: true,
  tls_warn_days: 14,
  paused: false,
  ...over,
});

const INITIAL: DemoState = {
  checks: [
    check({ name: 'website', type: 'http', target: 'https://example.com' }),
    check({ name: 'plex', type: 'http', target: 'http://127.0.0.1:32400/identity', interval: 30 }),
    check({ name: 'home-assistant', type: 'http', target: 'http://10.0.0.20:8123', interval: 30 }),
    check({ name: 'nas-backup', type: 'tcp', target: '10.0.0.30:873', retries: 1 }),
    check({ name: 'router', type: 'ping', target: '10.0.0.1', interval: 30 }),
    check({ name: 'example.com cert', type: 'tls', target: 'example.com:443', interval: 3600 }),
    check({ name: 'old-blog', type: 'http', target: 'https://blog.example.com', paused: true }),
  ],
  settings: {},
};

function load(): DemoState {
  try {
    const saved = sessionStorage.getItem(STORE_KEY);
    if (saved) return JSON.parse(saved) as DemoState;
  } catch {
    // Storage unavailable: start fresh.
  }
  return structuredClone(INITIAL);
}

const state = load();

function save() {
  try {
    sessionStorage.setItem(STORE_KEY, JSON.stringify(state));
  } catch {
    // Changes then last until the page reloads.
  }
}

// ---------- qBittorrent ----------

const MB = 1024 ** 2;
const torrent = (over: Partial<QBTorrent> & Pick<QBTorrent, 'hash' | 'name' | 'state' | 'size'>): QBTorrent => ({
  progress: 1,
  dlspeed: 0,
  upspeed: 0,
  ratio: 0,
  eta: 8640000,
  num_seeds: 0,
  num_leechs: 0,
  added_on: now() - 86400,
  completion_on: 0,
  category: '',
  save_path: '/home/media/downloads',
  downloaded: 0,
  uploaded: 0,
  ...over,
});

// Demo torrent changes (stops, deletions, additions) last for the tab.
// group mirrors the page's grouping, enough to tell stopped torrents from moving ones.
const group = (state: string) => (state.startsWith('paused') || state.startsWith('stopped') ? 'paused' : state.endsWith('up') || state === 'uploading' ? 'seeding' : 'downloading');

function qbAct(action: string, hashes: string[]) {
  for (const h of hashes) {
    const current = qbittorrent().qbittorrent!.torrents.find((x) => x.hash === h);
    if (!current) continue;
    const done = current.progress >= 1;
    if (action === 'delete') qbChanges.deleted.add(h);
    else if (action === 'stop') qbChanges.state[h] = done ? 'stoppedup' : 'stoppeddl';
    else if (action === 'start' || action === 'reannounce') qbChanges.state[h] = done ? 'uploading' : 'downloading';
    else if (action === 'recheck') qbChanges.state[h] = done ? 'checkingup' : 'checkingdl';
  }
}

const qbChanges: { state: Record<string, string>; deleted: Set<string>; added: QBTorrent[] } = { state: {}, deleted: new Set(), added: [] };

function qbittorrent(): QBResponse {
  const t = now();
  // speeds gives each active torrent's rate at any moment, so the chart and the list agree.
  const speeds = (at: number) => {
    const rate = (seed: number, base: number) => Math.max(0, Math.round(wave(at, base, base * 0.4, 600, seed)));
    return { linux: rate(1, 6.2 * MB), film: rate(2, 2.4 * MB), debian: rate(3, 1.1 * MB) };
  };
  const { linux, film, debian } = speeds(t);
  const torrents: QBTorrent[] = [
    torrent({
      hash: 'a1',
      name: 'ubuntu-24.04.3-desktop-amd64.iso',
      state: 'downloading',
      size: 6.1 * GB,
      progress: 0.62 + ((t / 3000) % 0.3),
      dlspeed: linux,
      upspeed: Math.round(linux / 9),
      eta: 540,
      num_seeds: 412,
      num_leechs: 38,
      category: 'linux',
      ratio: 0.08,
    }),
    torrent({
      hash: 'a2',
      name: 'Big.Buck.Bunny.2008.2160p.mkv',
      state: 'downloading',
      size: 14.2 * GB,
      progress: 0.31,
      dlspeed: film,
      upspeed: 0,
      eta: 3900,
      num_seeds: 23,
      num_leechs: 4,
      category: 'movies',
    }),
    torrent({
      hash: 'a3',
      name: 'Sintel.2010.1080p.mkv',
      state: 'stalleddl',
      size: 2.8 * GB,
      progress: 0.87,
      eta: 8640000,
      num_seeds: 0,
      num_leechs: 2,
      category: 'movies',
      stalled_for: 1260,
      recoveries: 1,
    }),
    torrent({
      hash: 'a4',
      name: 'debian-13.1.0-amd64-netinst.iso',
      state: 'uploading',
      size: 0.75 * GB,
      upspeed: debian,
      ratio: 3.42,
      num_seeds: 190,
      num_leechs: 12,
      category: 'linux',
      completion_on: now() - 7200,
    }),
    torrent({ hash: 'a5', name: 'Tears.of.Steel.2012.4K.mkv', state: 'stalledup', size: 9.4 * GB, ratio: 1.18, num_seeds: 31, category: 'movies' }),
    torrent({ hash: 'a6', name: 'Elephants.Dream.2006.1080p.mkv', state: 'stoppedup', size: 1.2 * GB, ratio: 2.05, category: 'movies' }),
    torrent({ hash: 'a7', name: 'fedora-workstation-43.iso', state: 'pauseddl', size: 2.3 * GB, progress: 0.12, category: 'linux' }),
  ];
  for (const x of torrents) {
    const st = qbChanges.state[x.hash];
    if (st) {
      x.state = st;
      if (group(st) !== 'downloading' && group(st) !== 'seeding') x.dlspeed = x.upspeed = 0;
    }
  }
  torrents.push(...qbChanges.added);
  const visible = torrents.filter((x) => !qbChanges.deleted.has(x.hash));
  const history = Array.from({ length: 120 }, (_, i) => {
    const ts = Math.round(t - (119 - i) * 10);
    const v = speeds(ts);
    return { ts, dl: v.linux + v.film, up: Math.round(v.linux / 9) + v.debian };
  });
  const totalDL = visible.reduce((n, x) => n + x.dlspeed, 0);
  const totalUP = visible.reduce((n, x) => n + x.upspeed, 0);
  return {
    enabled: true,
    qbittorrent: {
      connected: true,
      version: 'v5.1.4',
      url: 'http://localhost:8080',
      updated: t,
      transfer: {
        dl_info_speed: totalDL,
        up_info_speed: totalUP,
        dl_info_data: 48.3 * GB,
        up_info_data: 12.9 * GB,
        dl_rate_limit: 0,
        up_rate_limit: 5 * MB,
        connection_status: 'connected',
        dht_nodes: 382,
      },
      torrents: visible,
      history,
    },
  };
}

// ---------- systemd services and drives ----------

const unit = (name: string, description: string, over: Partial<Unit> = {}): Unit => ({
  name: name + '.service',
  description,
  active: 'active',
  sub: 'running',
  enabled: 'enabled',
  result: 'success',
  restarts: 0,
  since: now() - 86400 * 6,
  ...over,
});

const demoUnits: Unit[] = [
  unit('bazarr', 'Bazarr for media', { active: 'failed', sub: 'failed', result: 'exit-code', exit_status: 1, since: now() - 1260 }),
  unit('tautulli', 'Tautulli - Stats for Plex Media Server usage', { active: 'activating', sub: 'auto-restart', restarts: 5, flapping: 5, since: now() - 40 }),
  unit('sonarr', 'Sonarr Daemon'),
  unit('radarr', 'Radarr Daemon'),
  unit('plexmediaserver', 'Plex Media Server'),
  unit('qbittorrent@media', 'qBittorrent-nox service for media'),
  unit('nginx', 'A high performance web server and a reverse proxy server'),
  unit('ssh', 'OpenBSD Secure Shell server'),
  unit('server-monitor', 'Server Monitor', { since: now() - 3600 }),
  unit('apt-daily', 'Daily apt download activities', { active: 'failed', sub: 'failed', result: 'exit-code', exit_status: 100, ignored: true }),
  unit('apparmor', 'Load AppArmor profiles', { sub: 'exited' }),
];

const demoLogs: Record<string, string[]> = {
  'bazarr.service': [
    '2026-11-02T00:41:02+0000 demo-server bazarr[8121]: INFO: Bazarr starting up',
    '2026-11-02T00:41:04+0000 demo-server bazarr[8121]: ERROR: database disk image is malformed',
    '2026-11-02T00:41:04+0000 demo-server systemd[1]: bazarr.service: Main process exited, code=exited, status=1/FAILURE',
    "2026-11-02T00:41:04+0000 demo-server systemd[1]: bazarr.service: Failed with result 'exit-code'.",
  ],
  'tautulli.service': [
    "2026-11-02T01:20:11+0000 demo-server python3[901]: ModuleNotFoundError: No module named 'cherrypy'",
    '2026-11-02T01:20:11+0000 demo-server systemd[1]: tautulli.service: Scheduled restart job, restart counter is at 5.',
  ],
};

function demoUnitAction(name: string, action: string): Unit | undefined {
  const u = demoUnits.find((x) => x.name === name);
  if (!u) return undefined;
  if (action === 'stop') Object.assign(u, { active: 'inactive', sub: 'dead', since: now() });
  else Object.assign(u, { active: 'active', sub: 'running', result: 'success', exit_status: 0, flapping: 0, since: now() });
  return u;
}

const demoDrives = () => [
  {
    device: '/dev/nvme0',
    type: 'nvme',
    model: 'WDC PC SN530 SDBPNPZ-512G',
    capacity: 512110190592,
    rotational: false,
    passed: true,
    temperature: 54,
    power_on_hours: 19633,
    percent_used: 214,
    available_spare: 100,
    media_errors: 0,
    critical_warning: 0,
    asleep: false,
    checked: now() - 600,
    health: 'warning' as const,
    problems: ['214% of its rated write endurance used'],
  },
  {
    device: '/dev/sda',
    type: 'sat',
    model: 'WDC WD80EFAX-68KNBN0',
    capacity: 8001563222016,
    rotational: true,
    passed: true,
    temperature: 38,
    power_on_hours: 30112,
    reallocated: 0,
    pending: 0,
    asleep: true,
    checked: now() - 3 * 3600,
    health: 'good' as const,
    problems: [],
  },
];

// ---------- live network ----------

// netAt is the uplink's traffic at a moment: the overview's rhythm plus per-second jitter.
const netAt = (t: number) => ({ rx: Math.max(0, wave(t, 1_400_000, 600_000, 120, 3, 0.5)), tx: Math.max(0, wave(t, 2_600_000, 1_000_000, 180, 4, 0.45)) });

function network(since: number | undefined): NetworkResponse {
  const t = Math.floor(now());
  const from = since === undefined ? t - 120 : Math.floor(since) + 1;
  const up: { ts: number; rx: number; tx: number }[] = [];
  for (let ts = Math.max(from, t - 299); ts <= t; ts++) up.push({ ts, ...netAt(ts) });
  const cur = netAt(t);
  const window = Array.from({ length: 120 }, (_, i) => netAt(t - i));
  const avg = (k: 'rx' | 'tx') => window.reduce((n, s) => n + s[k], 0) / window.length;
  const peak = (k: 'rx' | 'tx') => Math.max(...window.map((s) => s[k]));
  const iface = (name: string, over: Partial<NetInterface>): NetInterface => ({
    name,
    up: true,
    default: false,
    virtual: true,
    rx_bps: 0,
    tx_bps: 0,
    rx_pps: 0,
    tx_pps: 0,
    rx_peak: 0,
    tx_peak: 0,
    rx_avg: 0,
    tx_avg: 0,
    rx_total: 0,
    tx_total: 0,
    errors: 0,
    drops: 0,
    ...over,
  });
  const wg = { rx: cur.rx * 0.08, tx: cur.tx * 0.02 };
  return {
    enabled: true,
    network: {
      interfaces: [
        iface('enp3s0', {
          default: true,
          virtual: false,
          speed_mbps: 1000,
          rx_bps: cur.rx,
          tx_bps: cur.tx,
          rx_pps: cur.rx / 1100,
          tx_pps: cur.tx / 1300,
          rx_avg: avg('rx'),
          tx_avg: avg('tx'),
          rx_peak: peak('rx'),
          tx_peak: peak('tx'),
          rx_total: 177.7 * GB,
          tx_total: 385.8 * GB,
          drops: 6402,
        }),
        iface('wg0', { rx_bps: wg.rx, tx_bps: wg.tx, rx_total: 12.4 * GB, tx_total: 3.1 * GB }),
        iface('docker0', { rx_bps: 2200, tx_bps: 4100, rx_total: 0.8 * GB, tx_total: 1.2 * GB }),
        iface('wlo1', { up: false, virtual: false }),
        iface('lo', { rx_bps: 11000, tx_bps: 11000, rx_total: 123 * GB, tx_total: 123 * GB }),
      ],
      samples: { enp3s0: up, wg0: up.map((x) => ({ ts: x.ts, rx: x.rx * 0.08, tx: x.tx * 0.02 })) },
      processes: [
        { name: 'Plex Media Server', unit: 'plexmediaserver', connections: 10, rx_bps: cur.rx * 0.05, tx_bps: cur.tx * 0.62, source: 'tcp' },
        { name: 'qBittorrent', unit: 'qbittorrent@media', connections: 41, rx_bps: cur.rx * 0.82, tx_bps: cur.tx * 0.31, source: 'qbittorrent' },
        { name: 'Sonarr', unit: 'sonarr', connections: 4, rx_bps: cur.rx * 0.06, tx_bps: 2100, source: 'tcp' },
        { name: 'sshd', unit: 'ssh', connections: 2, rx_bps: 410, tx_bps: 1900, source: 'tcp' },
        { name: 'AdGuardHome', unit: 'AdGuardHome', connections: 6, rx_bps: 120, tx_bps: 90, source: 'tcp' },
      ],
      hosts: [
        { peer: '203.0.113.24', process: 'Plex Media Server', connections: 3, rx_bps: cur.rx * 0.03, tx_bps: cur.tx * 0.55 },
        { peer: '198.51.100.7', process: 'qbittorrent-nox', connections: 1, rx_bps: cur.rx * 0.21, tx_bps: cur.tx * 0.04 },
        { peer: '192.0.2.88', process: 'Sonarr', connections: 2, rx_bps: cur.rx * 0.05, tx_bps: 1400 },
        { peer: '10.0.0.9', process: 'sshd', connections: 2, rx_bps: 410, tx_bps: 1900 },
      ],
      connections: 63,
      since: t - 120,
    },
    history: since === undefined ? Array.from({ length: 360 }, (_, i) => sample(Math.floor(now() / 5) * 5 - (359 - i) * 5)) : undefined,
  };
}

// ---------- updates ----------

const hoursAgo = (h: number) => Math.round(now() - h * 3600);
const target = (over: Partial<UpdateTarget> & Pick<UpdateTarget, 'id' | 'name' | 'kind'>): UpdateTarget => ({
  auto: false,
  paused: false,
  available: false,
  can_update: true,
  checked: hoursAgo(2),
  ...over,
});

const INITIAL_UPDATES: UpdateTarget[] = [
  target({
    id: 'os',
    name: 'OS packages (apt)',
    kind: 'system',
    auto: true,
    available: true,
    pending: ['fwupd 1.9.34-0ubuntu1 → 2.0.20-1ubuntu2', 'linux-firmware 20240318.git3b128b60-0ubuntu2.29 → 20240318.git3b128b60.0ubuntu3.1', 'openssl 3.0.13-0ubuntu3.6 → 3.0.13-0ubuntu3.7'],
    runs: [{ at: hoursAgo(26), ok: true, auto: true, detail: '7 package(s) upgraded with apt' }],
  }),
  target({ id: 'snaps', name: 'Snaps', kind: 'snap' }),
  target({
    id: 'servermonitor',
    name: 'Server Monitor',
    kind: 'git',
    dir: '/opt/servermonitor',
    self: true,
    installed: 'v2.3.0',
    latest: 'v2.3.0-2-g1c9e2aa',
    available: true,
    pending: ['1c9e2aa Updates page', '8a71f0d Faster process scans'],
    update_command: 'make build',
  }),
  target({
    id: 'plex',
    name: 'Plex Media Server',
    kind: 'apt',
    package: 'plexmediaserver',
    unit: 'plexmediaserver',
    installed: '1.43.5.11079-b3b13e506',
    latest: '1.43.5.11079-b3b13e506',
    auto: true,
  }),
  target({
    id: 'sonarr',
    name: 'Sonarr',
    kind: 'arr',
    unit: 'sonarr',
    config_file: '/home/media/.config/Sonarr/config.xml',
    installed: '4.0.14.2939',
    latest: '4.0.15.2941',
    available: true,
    runs: [{ at: hoursAgo(24 * 9), from: '4.0.13.2932', to: '4.0.14.2939', ok: true, auto: false }],
  }),
  target({ id: 'radarr', name: 'Radarr', kind: 'arr', unit: 'radarr', config_file: '/home/media/.config/Radarr/config.xml', installed: '5.26.2.10099', latest: '5.26.2.10099' }),
  target({
    id: 'adguard-home',
    name: 'AdGuard Home',
    kind: 'release',
    repo: 'AdguardTeam/AdGuardHome',
    unit: 'AdGuardHome',
    version_command: '/opt/AdGuardHome/AdGuardHome --version',
    update_command: '/opt/AdGuardHome/AdGuardHome --update',
    installed: '0.107.79',
    latest: 'v0.107.80',
    available: true,
  }),
  target({
    id: 'qbittorrent',
    name: 'qBittorrent',
    kind: 'release',
    repo: 'qbittorrent/qBittorrent',
    tag_prefix: 'release-',
    can_update: false,
    installed: '5.1.4',
    latest: '5.1.4',
    unit: 'qbittorrent@media',
  }),
  target({ id: 'tautulli', name: 'Tautulli', kind: 'git', dir: '/opt/tautulli', unit: 'tautulli', restart: true, error: "the checkout's branch has no upstream to update from", installed: 'v2.15.3' }),
];

state.updates ??= INITIAL_UPDATES;
const updatesList = () => state.updates!;

function updates(): UpdatesResponse {
  return {
    enabled: (state.settings.SYSTEM_UPDATE_ENABLED ?? ENVIRONMENT.SYSTEM_UPDATE_ENABLED) === 'true',
    interval: 21600,
    window: { start: 4, end: 6, label: '04:00–06:00' },
    commands_editable: true,
    kinds: ['system', 'snap', 'apt', 'arr', 'git', 'release'],
    checking: updatesList().some((t) => t.busy === 'checking'),
    last_check: hoursAgo(2),
    targets: updatesList(),
  };
}

// later changes a target after a delay, like the backend's worker would.
function later(id: string, ms: number, change: (t: UpdateTarget) => void) {
  setTimeout(() => {
    const t = updatesList().find((u) => u.id === id);
    if (t) {
      change(t);
      save();
    }
  }, ms);
}

function applyDemoUpdate(t: UpdateTarget) {
  t.busy = 'updating';
  later(t.id, 3500, (u) => {
    const from = u.installed;
    u.runs = [
      { at: now(), from, to: u.latest ?? from, ok: true, auto: false, detail: u.kind === 'system' ? `${u.pending?.length ?? 0} package(s) upgraded with apt` : undefined },
      ...(u.runs ?? []),
    ].slice(0, 5);
    if (u.kind === 'release') u.installed = u.latest?.replace(/^v/, '');
    else u.installed = u.latest;
    u.available = false;
    u.pending = null;
    u.busy = undefined;
    u.checked = now();
  });
}

function checkDemo(t: UpdateTarget) {
  if (t.paused) return;
  t.busy = 'checking';
  later(t.id, 1200, (u) => {
    u.busy = undefined;
    u.checked = now();
  });
}

function saveDemoTarget(spec: UpdateSpec, existing?: UpdateTarget): UpdateTarget | string {
  const name = spec.name?.trim();
  if (!name) return 'name is required';
  const id =
    existing?.id ??
    (spec.id ||
      name
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-|-$/g, ''));
  const { api_key, ...rest } = spec;
  const merged: UpdateTarget = { ...(existing ?? target({ id, name, kind: spec.kind })), ...rest, id, name, api_key_set: !!api_key || existing?.api_key_set };
  merged.can_update = merged.kind !== 'release' || !!merged.update_command;
  return merged;
}

// ---------- services ----------

// Each check gets a fixed personality: typical latency, how flaky it is, and whether it is down now.
function profile(name: string) {
  const seed = [...name].reduce((h, c) => (h * 31 + c.charCodeAt(0)) % 997, 7);
  return {
    seed,
    latency: name === 'router' ? 2 : name.includes('plex') ? 4 : 40 + (seed % 9) * 35,
    flaky: name === 'home-assistant' ? 0.012 : 0.003,
    downNow: name === 'nas-backup',
  };
}

function beatAt(spec: CheckSpec, t: number): Beat {
  const p = profile(spec.name);
  const down = (p.downNow && t > now() - 25 * 60) || noise(Math.floor(t / spec.interval), p.seed) < p.flaky;
  const latency = down ? null : round(Math.max(1, wave(t, p.latency, p.latency * 0.2, 6 * 3600, p.seed, 0.2)));
  return { ts: t, up: !down, latency_ms: latency };
}

function beats(spec: CheckSpec, count: number): Beat[] {
  const end = Math.floor(now() / spec.interval) * spec.interval;
  return Array.from({ length: count }, (_, i) => beatAt(spec, end - (count - 1 - i) * spec.interval));
}

function uptime(spec: CheckSpec, hours: number): number {
  const p = profile(spec.name);
  const down = p.flaky * 100 + (p.downNow ? (25 / 60 / hours) * 100 : 0);
  return round(100 - down, 2);
}

function summary(spec: CheckSpec): Service {
  const recent = spec.paused ? [] : beats(spec, 60);
  const last = recent[recent.length - 1];
  const status: Service['status'] = spec.paused ? 'paused' : last?.up ? 'up' : 'down';
  const detail =
    status === 'down'
      ? spec.type === 'tcp'
        ? 'connect failed: connection refused'
        : 'timed out after 10s'
      : spec.type === 'tls'
        ? 'certificate valid for 61 day(s)'
        : spec.type === 'ping'
          ? 'reply'
          : spec.type === 'tcp'
            ? 'connected'
            : 'HTTP 200';
  return {
    name: spec.name,
    type: spec.type,
    target: spec.target,
    status,
    latency_ms: last?.latency_ms ?? null,
    detail,
    last_check: last?.ts ?? null,
    last_change: status === 'down' ? now() - 25 * 60 : now() - 6 * 86400,
    uptime_24h: spec.paused ? null : uptime(spec, 24),
    uptime_7d: spec.paused ? null : uptime(spec, 168),
    uptime_30d: spec.paused ? null : uptime(spec, 720),
    recent,
  };
}

function history(spec: CheckSpec, hours: number): HistoryPoint[] {
  const end = now();
  const count = Math.min(720, Math.floor((hours * 3600) / spec.interval));
  const width = (hours * 3600) / count;
  return Array.from({ length: count }, (_, i) => {
    const b = beatAt(spec, end - hours * 3600 + i * width);
    return { ts: b.ts, up: b.up, latency_ms: b.latency_ms ?? null, detail: b.up ? '' : 'timed out after 10s', fails: b.up ? 0 : 1 };
  });
}

// ---------- host ----------

function sample(t: number) {
  return {
    ts: t,
    cpu: clamp(wave(t, 22, 9, 900, 1, 0.25), 2, 98),
    memory: clamp(wave(t, 52, 3, 3600, 2, 0.02), 5, 98),
    disk: 61.2,
    rx_bps: Math.max(0, wave(t, 1_400_000, 600_000, 420, 3, 0.35)),
    tx_bps: Math.max(0, wave(t, 2_600_000, 1_000_000, 600, 4, 0.35)),
  };
}

function stats(): Stats {
  const t = Math.floor(now() / 5) * 5;
  const history = Array.from({ length: 360 }, (_, i) => sample(t - (359 - i) * 5));
  const s = history[history.length - 1];
  const perCore = Array.from({ length: 12 }, (_, i) => round(clamp(s.cpu * (0.4 + noise(t / 5 + i, 9) * 1.4), 0, 100)));
  const partition = (mountpoint: string, device: string, total: number, percent: number, nvme = false) => ({
    device,
    mountpoint,
    fstype: 'ext4',
    total,
    used: total * (percent / 100),
    free: total * (1 - percent / 100),
    percent,
    is_nvme: nvme,
  });
  const partitions = [
    partition('/', '/dev/nvme0n1p2', 465.9 * GB, 18.2, true),
    partition('/boot', '/dev/nvme0n1p1', 1.9 * GB, 11.0, true),
    partition('/mnt/media', '/dev/sda1', 7.2 * 1024 * GB, 67.3),
  ];
  const memTotal = 15.3 * GB;
  return {
    timestamp: t,
    system: {
      hostname: 'demo-server',
      platform: 'Linux',
      platform_release: '6.8.0-48-generic',
      platform_version: 'ubuntu 24.04',
      architecture: 'amd64',
      uptime_seconds: 20 * 86400 + 5 * 3600 + 1800 + (t % 3600),
      uptime_formatted: '',
      users: 1,
    },
    cpu: { percent: round(s.cpu), per_core: perCore, cores: 12, freq: { current: 3800, min: 800, max: 4600 }, load: [round(s.cpu / 8, 2), 2.41, 2.12] },
    memory: { total: memTotal, used: memTotal * (s.memory / 100), free: memTotal * 0.1, available: memTotal * (1 - s.memory / 100), percent: round(s.memory) },
    swap: { total: 2 * GB, used: 0.1 * GB, free: 1.9 * GB, available: 1.9 * GB, percent: 5 },
    disk: {
      total: { total: partitions.reduce((a, p) => a + p.total, 0), used: partitions.reduce((a, p) => a + p.used, 0), free: partitions.reduce((a, p) => a + p.free, 0), percent: 61.2, partitions: 3 },
      partitions,
    },
    gpu: {
      name: 'GeForce GTX 1660 Ti',
      utilization: round(clamp(wave(t, 18, 12, 300, 6), 0, 100)),
      memory_percent: 22,
      memory_used_mb: 1320,
      memory_total_mb: 6144,
      temperature: 54,
      power_draw: 31,
      power_limit: 120,
    },
    temperature: round(wave(t, 58, 6, 900, 5, 0.03)),
    fans: [{ label: 'cpu_fan', speed_rpm: Math.round(wave(t, 1250, 150, 900, 5, 0.05)) }],
    network: {
      bandwidth: { bytes_recv_per_sec: s.rx_bps, bytes_sent_per_sec: s.tx_bps },
      interfaces: [
        { name: 'enp4s0', addresses: ['10.0.0.17/24', 'fe80::1/64'], is_up: true, speed: 1000 },
        { name: 'docker0', addresses: ['172.17.0.1/16'], is_up: true, speed: 0 },
        { name: 'wlo1', addresses: null, is_up: false, speed: 0 },
      ],
    },
    processes: {
      total: 341,
      by_status: { running: 2, sleeping: 339 },
      top_cpu: [
        { pid: 2319, name: 'plex-transcoder', cpu_percent: round(clamp(s.cpu * 1.6, 0, 400)), memory_percent: 2.1 },
        { pid: 1182, name: 'qbittorrent-nox', cpu_percent: 6.2, memory_percent: 7.3 },
        { pid: 3573, name: 'node', cpu_percent: 3.4, memory_percent: 2.7 },
        { pid: 1088, name: 'systemd-journald', cpu_percent: 1.1, memory_percent: 0.4 },
        { pid: 941, name: 'dockerd', cpu_percent: 0.6, memory_percent: 1.2 },
      ],
      top_memory: [
        { pid: 1182, name: 'qbittorrent-nox', cpu_percent: 6.2, memory_percent: 7.3 },
        { pid: 3899, name: 'Sonarr', cpu_percent: 0.3, memory_percent: 2.4 },
        { pid: 3573, name: 'node', cpu_percent: 3.4, memory_percent: 2.7 },
        { pid: 2319, name: 'plex-transcoder', cpu_percent: 12, memory_percent: 2.1 },
        { pid: 1832, name: 'postgres', cpu_percent: 0.2, memory_percent: 1.9 },
      ],
    },
    history,
  };
}

// ---------- plex ----------

function plex(summaryView: boolean): PlexResponse {
  const t = now();
  const stream = (id: string, over: Partial<PlexStream>): PlexStream => ({
    id,
    user: 'sam',
    kind: 'movie',
    title: '',
    subtitle: '',
    player: 'Living Room',
    product: 'Plex for Android (TV)',
    platform: 'Android',
    state: 'playing',
    local: true,
    decision: 'direct play',
    hw_transcode: false,
    throttled: false,
    quality: '1080p',
    bitrate_kbps: 9800,
    bandwidth_kbps: 10200,
    duration_ms: 7_200_000,
    offset_ms: 2_100_000,
    progress: 29,
    started_at: t - 2100,
    ...over,
  });
  const streams = [
    stream('1', { title: 'Severance', kind: 'episode', subtitle: 'S02E03 · Who Is Alive?', duration_ms: 3_300_000, offset_ms: 1_900_000, progress: 58, started_at: t - 1900 }),
    stream('2', {
      user: 'alex',
      title: 'Heat',
      subtitle: '1995',
      player: 'iPhone',
      product: 'Plex for iOS',
      platform: 'iOS',
      local: false,
      decision: 'transcode',
      video_decision: 'transcode',
      audio_decision: 'copy',
      hw_transcode: true,
      throttled: true,
      speed: 2.4,
      quality: '4K',
      bitrate_kbps: 4000,
      bandwidth_kbps: 4400,
      duration_ms: 10_200_000,
      offset_ms: 6_100_000,
      progress: 60,
      started_at: t - 4200,
    }),
    stream('3', {
      user: 'kim',
      kind: 'track',
      title: 'Teardrop',
      subtitle: 'Massive Attack · Mezzanine',
      player: 'Web',
      product: 'Plex Web',
      platform: 'Chrome',
      state: 'paused',
      quality: '',
      bitrate_kbps: 320,
      bandwidth_kbps: 330,
      duration_ms: 330_000,
      offset_ms: 120_000,
      progress: 36,
      started_at: t - 400,
    }),
  ];
  const samples: PlexSample[] = Array.from({ length: 360 }, (_, i) => {
    const ts = t - (359 - i) * 15;
    const evening = Math.max(0, Math.sin((ts / 5400) * Math.PI));
    const direct = Math.round(1 + evening * 2 + noise(Math.floor(ts / 600), 3));
    const transcode = noise(Math.floor(ts / 900), 4) > 0.55 ? 1 : 0;
    return { ts, direct, transcode, bandwidth_kbps: Math.round(direct * 8200 + transcode * 4300 + noise(ts, 5) * 900) };
  });
  const plays: PlexPlay[] = [
    { user: 'sam', title: 'Andor', subtitle: 'S02E01 · One Year Later', player: 'Living Room', decision: 'direct play', started: t - 9000, ended: t - 6300, progress: 98 },
    { user: 'alex', title: 'Dune: Part Two', subtitle: '2024', player: 'iPhone', decision: 'transcode', started: t - 20000, ended: t - 11000, progress: 100 },
    { user: 'kim', title: 'Spirited Away', subtitle: '2001', player: 'Bedroom', decision: 'direct stream', started: t - 40000, ended: t - 33000, progress: 74 },
  ];
  return {
    enabled: true,
    plex: {
      url: 'http://127.0.0.1:32400',
      status: 'up',
      detail: '',
      since: t - 5 * 86400,
      checked_at: t - 3,
      latency_ms: 4.2,
      server: { name: 'demo-server', version: '1.41.3.9314', platform: 'Linux', platform_version: '6.8.0', update_available: '1.41.4.9463' },
      streams,
      direct: 2,
      transcode: 1,
      bandwidth_kbps: streams.reduce((a, s) => a + s.bandwidth_kbps, 0),
      wan_kbps: 4400,
      libraries: summaryView
        ? []
        : [
            { key: '1', title: 'Movies', type: 'movie', count: 1284, refreshing: false },
            { key: '2', title: 'TV Shows', type: 'show', count: 213, child_count: 9381, child_label: 'episodes', refreshing: true },
            { key: '3', title: 'Music', type: 'artist', count: 412, child_count: 18220, child_label: 'tracks', refreshing: false },
          ],
      libraries_at: t - 600,
      samples: summaryView ? [] : samples,
      recent: summaryView ? [] : plays,
    },
  };
}

// ---------- settings ----------

type FieldDef = Omit<SettingField, 'value' | 'set' | 'source'>;

const f = (group: string, key: string, label: string, kind: SettingField['kind'], def: string, extra: Partial<FieldDef> = {}): FieldDef => ({
  key,
  group,
  label,
  kind,
  default: def,
  ...extra,
});

// Which demo settings apply without a restart, and which are folded away as advanced, as in the backend.
const DEMO_LIVE = new Set([
  'DASHBOARD_HIDDEN',
  'CHECK_INTERVAL',
  'HISTORY_RETENTION_DAYS',
  'CPU_THRESHOLD',
  'MEMORY_THRESHOLD',
  'TEMP_THRESHOLD',
  'DISK_THRESHOLD',
  'DISK_CLEANUP_THRESHOLD',
  'MONITOR_INDIVIDUAL_PARTITIONS',
  'SYSTEM_UPDATE_ENABLED',
  'SYSTEM_UPDATE_INTERVAL_SECONDS',
  'UPDATES_WINDOW_START',
  'UPDATES_WINDOW_END',
  'STATUS_PAGE_ENABLED',
]);
const DEMO_ADVANCED = new Set(['DISK_WEBHOOK', 'MONITOR_INDIVIDUAL_PARTITIONS', 'KERNEL_IGNORE_DEVICES', 'SYSTEM_UPDATE_INTERVAL_SECONDS', 'GITHUB_TOKEN', 'WEB_HOST', 'WEB_PORT', 'LOG_LEVEL']);

// A representative slice of the real settings (backend/internal/config/settings.go).
const FIELDS: FieldDef[] = [
  f('Monitoring', 'CHECK_INTERVAL', 'Check interval', 'number', '5', { unit: 's', help: 'Seconds between monitoring cycles.', min: 0.1, max: 3600 }),
  f('Monitoring', 'ALERT_COOLDOWN', 'Alert cooldown', 'number', '300', { unit: 's', help: 'Before the same alert repeats. Grows while the condition persists.', min: 0 }),
  f('Monitoring', 'HISTORY_RETENTION_DAYS', 'Keep check history', 'int', '90', { unit: 'days', min: 1, max: 3650 }),
  f('Notifications', 'WEBHOOK', 'Discord webhook', 'secret', '', { help: 'Where alerts go.' }),
  f('Notifications', 'DISK_WEBHOOK', 'Disk webhook', 'secret', '', { help: 'Disk usage, cleanup and media directory alerts.' }),
  f('Notifications', 'DASHBOARD_URL', 'Dashboard address', 'text', '', { help: 'Where alert titles link to, e.g. https://monitor.example.com.' }),
  f('Alert channels', 'ALERT_CHANNELS_MIN_SEVERITY', 'Send to these channels from', 'text', 'INFO', {
    options: ['INFO', 'WARNING', 'CRITICAL'],
    help: 'The least severe alert the channels below receive.',
  }),
  f('Alert channels', 'NTFY_URL', 'ntfy topic', 'text', '', { help: 'Push to phones with ntfy, e.g. https://ntfy.sh/your-topic.' }),
  f('Alert channels', 'TELEGRAM_BOT_TOKEN', 'Telegram bot token', 'secret', '', { help: 'From @BotFather.' }),
  f('Alert channels', 'TELEGRAM_CHAT_ID', 'Telegram chat ID', 'text', ''),
  f('Alert channels', 'SLACK_WEBHOOK', 'Slack webhook', 'secret', '', { help: 'An incoming webhook URL.' }),
  f('Alert channels', 'ALERT_WEBHOOK_URL', 'JSON webhook', 'secret', '', { help: 'Each alert is POSTed as JSON.' }),
  f('Alert channels', 'SMTP_HOST', 'Email server', 'text', ''),
  f('Alert channels', 'SMTP_TO', 'Email to', 'list', '', { help: 'Comma-separated addresses.' }),
  f('Resources', 'CPU_THRESHOLD', 'CPU', 'number', '80', { unit: '%', help: 'Alert when average CPU usage reaches this.', min: 0, max: 100 }),
  f('Resources', 'MEMORY_THRESHOLD', 'Memory', 'number', '80', { unit: '%', min: 0, max: 100 }),
  f('Resources', 'TEMP_THRESHOLD', 'CPU temperature', 'number', '80', { unit: '°C', min: 0 }),
  f('Disks', 'DISK_THRESHOLD', 'Usage alert', 'number', '80', { unit: '%', min: 0, max: 100 }),
  f('Disks', 'DISK_CLEANUP_THRESHOLD', 'Start cleanup', 'number', '85', { unit: '%', help: 'Removes old temp files, caches and rotated logs.', min: 0, max: 100 }),
  f('Disks', 'MONITOR_INDIVIDUAL_PARTITIONS', 'Check each partition', 'bool', 'true', { help: 'Otherwise only the total across disks.' }),
  f('Kernel', 'KERNEL_MONITOR_ENABLED', 'Watch the kernel', 'bool', 'true', { help: 'Crashes, hardware errors, unexpected reboots and stability.' }),
  f('Kernel', 'KERNEL_IGNORE_DEVICES', 'Ignore disk errors on', 'list', '', { help: 'Disks (e.g. sdb) whose I/O error counters to ignore.' }),
  f('Updates', 'SYSTEM_UPDATE_ENABLED', 'Check for updates', 'bool', 'false', { help: 'OS packages, snaps and apps, listed on the Updates page.' }),
  f('Updates', 'SYSTEM_UPDATE_INTERVAL_SECONDS', 'Check every', 'number', '21600', { unit: 's', min: 300 }),
  f('Updates', 'UPDATES_WINDOW_START', 'Auto-update from', 'int', '4', { unit: 'h', min: 0, max: 23, help: 'Hour (local time) automatic updates may start; the same start and end means any time.' }),
  f('Updates', 'UPDATES_WINDOW_END', 'Auto-update until', 'int', '6', { unit: 'h', min: 0, max: 23 }),
  f('Updates', 'UPDATES_NOTIFY', 'Announce updates', 'bool', 'true', { help: 'Alert when updates wait for you; automatic ones alert when installed.' }),
  f('Updates', 'GITHUB_TOKEN', 'GitHub token', 'secret', '', { help: "Optional. Raises GitHub's limit of 60 release checks an hour." }),
  f('Plex', 'PLEX_MONITOR', 'Watch Plex', 'bool', 'false', { help: 'Server state, streams, transcodes and libraries.' }),
  f('Plex', 'PLEX_URL', 'Server URL', 'text', 'http://localhost:32400'),
  f('Plex', 'PLEX_TOKEN', 'Token', 'secret', '', { help: 'X-Plex-Token of a server admin.' }),
  f('Dashboard', 'DASHBOARD_HIDDEN', 'Hidden sections', 'list', '', { help: 'Dashboard areas turned off under Settings → Sections.' }),
  f('Dashboard', 'STATUS_PAGE_ENABLED', 'Public status page', 'bool', 'false', { help: 'Names, status and uptime at /status, without the token.' }),
  f('Dashboard', 'WEB_HOST', 'Listen address', 'text', '127.0.0.1', { startup: true, help: 'Every address needs the admin account to sign in.' }),
  f('Dashboard', 'WEB_PORT', 'Port', 'int', '8080', { startup: true }),
  f('Storage and logging', 'LOG_LEVEL', 'Log level', 'text', 'INFO', { options: ['DEBUG', 'INFO', 'WARNING', 'ERROR'] }),
];

// What .env would have set in this demo.
const ENVIRONMENT: Record<string, string> = {
  WEBHOOK: 'https://discord.com/api/webhooks/demo',
  PLEX_MONITOR: 'true',
  STATUS_PAGE_ENABLED: 'true',
  CHECK_INTERVAL: '10',
  SYSTEM_UPDATE_ENABLED: 'true',
};

function settings(): SettingsResponse {
  const groups: SettingsResponse['groups'] = [];
  for (const def of FIELDS) {
    const raw = state.settings[def.key] ?? ENVIRONMENT[def.key] ?? def.default;
    const source: SettingField['source'] = def.key in state.settings ? 'dashboard' : def.key in ENVIRONMENT ? 'environment' : 'default';
    const field: SettingField = { ...def, value: def.kind === 'secret' ? '' : raw, set: raw !== '', source, live: DEMO_LIVE.has(def.key), advanced: DEMO_ADVANCED.has(def.key) };
    if (groups[groups.length - 1]?.name !== def.group) groups.push({ name: def.group, fields: [] });
    groups[groups.length - 1].fields.push(field);
  }
  return { groups };
}

function validate(values: Record<string, string>): Record<string, string> {
  const problems: Record<string, string> = {};
  for (const [key, raw] of Object.entries(values)) {
    const def = FIELDS.find((d) => d.key === key);
    if (!def) problems[key] = 'unknown setting';
    else if (def.startup) problems[key] = 'only settable in the environment (.env)';
    else if ((def.kind === 'number' || def.kind === 'int') && raw !== '') {
      const v = Number(raw);
      if (Number.isNaN(v) || (def.kind === 'int' && !Number.isInteger(v))) problems[key] = `"${raw}" is not a number`;
      else if (def.min !== undefined && v < def.min) problems[key] = `must be at least ${def.min}`;
      else if (def.max !== undefined && v > def.max) problems[key] = `must be at most ${def.max}`;
    }
  }
  return problems;
}

// ---------- routing ----------

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const error = (status: number, message: string, fields?: Record<string, string>) => json({ error: message, fields }, status);

function normalise(spec: Partial<CheckSpec>): CheckSpec | string {
  const name = spec.name?.trim();
  const target = spec.target?.trim();
  if (!name || !target) return "'name' and 'target' are required";
  if (spec.type === 'http' && !/^https?:\/\//.test(target)) return `${name}: http target must start with http:// or https://`;
  return check({ ...spec, name, target, type: spec.type ?? 'http', interval: Math.max(5, spec.interval ?? 60) } as CheckSpec);
}

// The demo status page follows its design the way the backend's does.
function designOrDefault(): StatusDesign {
  return (
    state.statusDesign ?? {
      title: 'Demo status',
      description: '',
      announcement: '',
      tone: 'info',
      show_uptime: true,
      show_history: true,
      footer: '',
      groups: [{ name: '', items: state.checks.filter((c) => !c.paused).map((c) => ({ check: c.name })) }],
    }
  );
}

function publicStatus(): PublicStatus {
  const d = designOrDefault();
  const byName = new Map(state.checks.map((c) => [c.name, summary(c)]));
  const groups = d.groups.map((g) => ({
    name: g.name,
    services: g.items.flatMap((it) => {
      const r = byName.get(it.check);
      return r
        ? [{ name: it.label || r.name, status: r.status, uptime_24h: d.show_uptime ? r.uptime_24h : null, uptime_30d: d.show_uptime ? r.uptime_30d : null, recent: d.show_history ? r.recent : [] }]
        : [];
    }),
  }));
  const all = groups.flatMap((g) => g.services);
  const watched = all.filter((r) => r.status !== 'paused');
  const down = watched.filter((r) => r.status === 'down').length;
  return {
    title: d.title || 'Demo status',
    description: d.description,
    footer: d.footer,
    show_uptime: d.show_uptime,
    show_history: d.show_history,
    announcement: d.announcement ? { text: d.announcement, tone: d.tone } : undefined,
    overall: down === 0 ? 'operational' : down < watched.length ? 'degraded' : 'outage',
    updated: now(),
    groups: groups.filter((g) => g.services.length),
    services: all,
  };
}

// ---------- notifications ----------

const NOTIFY_AREAS = [
  { id: 'uptime', label: 'Uptime checks' },
  { id: 'services', label: 'System services' },
  { id: 'resources', label: 'CPU, memory and network' },
  { id: 'storage', label: 'Disks and drives' },
  { id: 'system', label: 'Monitor and kernel' },
  { id: 'updates', label: 'Updates' },
  { id: 'torrents', label: 'qBittorrent' },
  { id: 'media', label: 'Plex' },
];
const NOTIFY_CHANNELS = ['Discord', 'ntfy'];

const notifyDefaults = (): AlertRules => Object.fromEntries(NOTIFY_AREAS.map((a) => [a.id, { Discord: a.id === 'torrents' ? 'off' : 'all', ntfy: 'warning' }])) as AlertRules;

const demoNotify: { items: Notification[]; rules: AlertRules; quiet: QuietHours; mutes: Mute[] } = (() => {
  let id = 0;
  const n = (ago: number, area: string, severity: Notification['severity'], title: string, body: string, over: Partial<Notification> = {}): Notification => ({
    id: ++id,
    ts: now() - ago,
    area,
    severity,
    title,
    body,
    fields: [],
    status: 'sent',
    channels: severity === 'critical' || severity === 'warning' || severity === 'ok' ? ['Discord', 'ntfy'] : ['Discord'],
    read: ago > 3 * 3600,
    ...over,
  });
  const items = [
    n(9 * 86400, 'updates', 'ok', 'Sonarr updated', 'Installed and running.', { fields: [{ name: 'Version', value: '4.0.14 → 4.0.15' }] }),
    n(4 * 86400 + 3000, 'storage', 'warning', 'Disk Usage Alert: /mnt/media', 'Usage is above threshold', {
      fields: [
        { name: 'Used', value: '86.2%' },
        { name: 'Free', value: '1.1 TB' },
      ],
    }),
    n(2 * 86400 + 600, 'torrents', 'ok', 'Download Complete', 'Torrent download has completed: **Sintel.2010.1080p.mkv**', { status: 'kept', channels: [] }),
    n(26 * 3600, 'uptime', 'critical', 'Service Down: nas-backup', 'dial tcp 10.0.0.30:873: connect: connection refused', {
      subject: 'check:nas-backup',
      fields: [
        { name: 'Target', value: '10.0.0.30:873' },
        { name: 'Uptime (24h)', value: '97.92%' },
      ],
    }),
    n(25.5 * 3600, 'uptime', 'ok', 'Service Recovered: nas-backup', 'connected in 3 ms', { subject: 'check:nas-backup' }),
    n(7.2 * 3600, 'services', 'warning', 'radarr keeps restarting', 'Radarr Daemon restarted 4 times in the last ten minutes.', { subject: 'unit:radarr.service', status: 'digest' }),
    n(6.8 * 3600, 'resources', 'warning', 'Memory Usage Alert', 'Memory usage has exceeded the threshold of 90.0%', { status: 'digest', fields: [{ name: 'Used', value: '14.6 GB of 15.6 GB' }] }),
    n(6.5 * 3600, 'system', 'info', 'Quiet hours summary: 2 alerts', 'Held from 23:00 to 07:00. Critical alerts were sent straight away.'),
    n(3.1 * 3600, 'storage', 'warning', 'Drive health warning: WDC PC SN530 SDBPNPZ-512G (/dev/nvme0)', '• 214% of its rated write endurance used: plan to replace it', {
      subject: 'drive:/dev/nvme0',
      fields: [
        { name: 'Temperature', value: '54°C' },
        { name: 'Powered on', value: '2y 87d' },
      ],
    }),
    n(2 * 3600, 'media', 'info', 'Now playing: Big Buck Bunny', '', {
      fields: [
        { name: 'User', value: 'sam' },
        { name: 'Quality', value: '4K · direct play' },
      ],
    }),
    n(55 * 60, 'updates', 'info', '3 updates available', "Install from the dashboard's Updates page, or turn on auto-update for them.", { fields: [{ name: 'Prowlarr', value: '1.30 → 1.31' }] }),
    n(21 * 60, 'services', 'critical', 'bazarr failed', "Bazarr for media stopped and did not come back. Restart it from the dashboard's Services page.", {
      subject: 'unit:bazarr.service',
      fields: [
        { name: 'State', value: 'failed (exit-code, exit status 1)' },
        { name: 'Last log lines', value: '```\nTraceback (most recent call last):\n  File "/opt/bazarr/bazarr.py", line 12\nModuleNotFoundError: No module named \'lxml\'\n```' },
      ],
    }),
    n(4 * 60, 'services', 'warning', 'tautulli keeps restarting', 'Tautulli - Stats for Plex Media Server usage restarted 5 times in the last ten minutes.', {
      subject: 'unit:tautulli.service',
      status: 'failed',
      channels: [],
      error: 'ntfy: HTTP 502: bad gateway; Discord: rate limited',
    }),
  ];
  return {
    items: items.reverse(),
    rules: notifyDefaults(),
    quiet: { enabled: true, start: '23:00', end: '07:00', critical: true },
    mutes: [{ subject: 'area:torrents', label: 'qBittorrent', until: now() + 3 * 3600 }],
  };
})();

function notifyCounts(): NotificationCounts {
  const unread = demoNotify.items.filter((n) => !n.read);
  const crit = unread.filter((n) => n.severity === 'critical').length;
  const warn = unread.filter((n) => n.severity === 'warning').length;
  return { unread: unread.length, alerts: crit + warn, worst: crit ? 'critical' : warn ? 'warning' : unread.length ? 'info' : undefined, latest: demoNotify.items[0]?.id ?? 0 };
}

const liveMutes = () => demoNotify.mutes.filter((m) => m.until > now());

function notifyRules(): NotificationRules {
  return { areas: NOTIFY_AREAS, channels: NOTIFY_CHANNELS, rules: demoNotify.rules, defaults: notifyDefaults(), quiet: demoNotify.quiet, quiet_active: false, held: 0, mutes: liveMutes() };
}

function notifications(url: URL): NotificationsResponse {
  const q = url.searchParams.get('q')?.toLowerCase() ?? '';
  const sev = url.searchParams.get('severity')?.split(',') ?? [];
  const area = url.searchParams.get('area') ?? '';
  const unread = url.searchParams.get('unread') === '1';
  const before = Number(url.searchParams.get('before') ?? 0);
  const all = demoNotify.items.filter(
    (n) => (!q || `${n.title} ${n.body}`.toLowerCase().includes(q)) && (!sev.length || sev.includes(n.severity)) && (!area || n.area === area) && (!unread || !n.read),
  );
  return { items: all.filter((n) => !before || n.id < before).slice(0, 50), total: all.length, counts: notifyCounts(), kept: true };
}

// ---------- the monitor's own log ----------

const demoLog = (() => {
  const t0 = Date.now() - 40 * 60 * 1000;
  const lines: [number, LogEntry['level'], string, string, [string, string][]][] = [
    [0, 'info', 'core', 'Starting monitors', []],
    [1, 'info', 'kernel', 'Kernel monitor: ignoring I/O errors on devices: nvme0n1, sda', []],
    [1, 'info', 'web', 'Web dashboard listening', [['addr', 'http://127.0.0.1:8080']]],
    [2, 'info', 'qbittorrent', 'qBittorrent connected successfully', [['version', 'v4.6.7']]],
    [
      4,
      'warn',
      'disk',
      'Drive health warning',
      [
        ['drive', 'WDC PC SN530 SDBPNPZ-512G (/dev/nvme0)'],
        ['news', '[214% of its rated write endurance used: plan to replace it]'],
      ],
    ],
    [
      60,
      'info',
      'updates',
      'Update check finished',
      [
        ['available', '4'],
        ['took', '6.2s'],
      ],
    ],
    [
      300,
      'warn',
      'systemd',
      'Service keeps restarting',
      [
        ['unit', 'tautulli.service'],
        ['restarts', '5'],
      ],
    ],
    [
      320,
      'error',
      'systemd',
      'Service failed',
      [
        ['unit', 'bazarr.service'],
        ['result', 'exit-code, exit status 1'],
      ],
    ],
    [
      900,
      'info',
      'core',
      'Readings',
      [
        ['cpu', '13.3%'],
        ['memory', '50.2%'],
        ['disk.root', '19.8%'],
        ['disk.media_hdd', '67.3%'],
        ['temp', '80°C'],
        ['network', '0.9MB/s'],
      ],
    ],
    [1500, 'warn', 'web', 'Failed sign-in', [['ip', '203.0.113.9']]],
    [
      1800,
      'info',
      'core',
      'Readings',
      [
        ['cpu', '12.9%'],
        ['memory', '50.6%'],
        ['disk.root', '19.8%'],
        ['disk.media_hdd', '67.4%'],
        ['temp', '79°C'],
        ['network', '1.4MB/s'],
      ],
    ],
    [2100, 'info', 'qbittorrent', 'Torrents added from the dashboard', [['count', '2']]],
    [
      2280,
      'info',
      'notify',
      'Quiet hours summary sent',
      [
        ['alerts', '2'],
        ['channels', 'Discord'],
      ],
    ],
  ];
  return lines.map(([s, level, component, message, attrs], i): LogEntry => ({ seq: i + 1, time: t0 + s * 1000, level, component, message, attrs: attrs.map(([key, value]) => ({ key, value })) }));
})();

function logs(url: URL): LogsResponse {
  const after = Number(url.searchParams.get('after') ?? 0);
  const rank = { debug: 0, info: 1, warn: 2, error: 3 };
  const min = rank[(url.searchParams.get('level') || 'debug') as LogEntry['level']];
  const component = url.searchParams.get('component') ?? '';
  const q = url.searchParams.get('q')?.toLowerCase() ?? '';
  const entries = demoLog.filter(
    (e) =>
      e.seq > after && rank[e.level] >= min && (!component || e.component === component) && (!q || `${e.message} ${e.attrs?.map((a) => `${a.key}=${a.value}`).join(' ')}`.toLowerCase().includes(q)),
  );
  return { entries, latest: demoLog.length, components: [...new Set(demoLog.map((e) => e.component ?? ''))].sort(), level: 'INFO', file: 'server_monitor.log', kept: 3000 };
}

// ---------- app store ----------

const storeApp = (id: string, name: string, category: string, color: string, port: number, summary: string, over: Partial<StoreApp> = {}): StoreApp => ({
  id,
  name,
  category,
  color,
  port,
  summary,
  homepage: 'https://example.com',
  sign_in: port ? 'Choose a username and password on first visit.' : undefined,
  installed: false,
  running: false,
  available: true,
  ...over,
});

const demoApps: StoreApp[] = [
  storeApp('qbittorrent', 'qBittorrent', 'Download clients', '#2f67ba', 8086, 'The torrent client: fast, light, with a full web interface.', {
    installed: true,
    running: true,
    by: 'swizzin',
    unit: 'qbittorrent@media.service',
  }),
  storeApp('prowlarr', 'Prowlarr', 'Indexers', '#e66000', 9696, 'Manages your torrent trackers and indexers in one place and syncs them to Sonarr, Radarr and Lidarr.', {
    installed: true,
    running: true,
    by: 'swizzin',
  }),
  storeApp('jackett', 'Jackett', 'Indexers', '#2e3440', 9117, "Turns hundreds of tracker sites into one search API, for apps that don't use Prowlarr."),
  storeApp('sonarr', 'Sonarr', 'Automation', '#35c5f4', 8989, 'Watches for new episodes of your shows, grabs them through your torrent client and files them.', {
    installed: true,
    running: true,
    by: 'swizzin',
  }),
  storeApp('radarr', 'Radarr', 'Automation', '#ffc230', 7878, 'Sonarr for films: finds the releases you want, at the quality you want, and keeps them organised.', {
    installed: true,
    running: true,
    by: 'swizzin',
  }),
  storeApp('lidarr', 'Lidarr', 'Automation', '#00a65b', 8686, 'Sonarr for music: follows artists and fetches new albums as they come out.'),
  storeApp('autobrr', 'autobrr', 'Automation', '#3d6bf5', 7474, 'Catches new releases from tracker IRC announce channels within seconds and sends them to your client.', {
    installed: true,
    running: true,
    by: 'swizzin',
  }),
  storeApp('unpackerr', 'Unpackerr', 'Automation', '#7a5af8', 0, 'Extracts downloads that arrive as RAR archives so Sonarr and Radarr can import them, then cleans up.', {
    installed: true,
    running: true,
    by: 'system',
    unit: 'unpackerr.service',
  }),
  storeApp('ntfy', 'ntfy', 'Notifications', '#338574', 2586, "Your own push notification server: the monitor's alerts reach your phone without a third-party service.", {
    sign_in: "In the ntfy phone app, add this server's address, then subscribe to the monitor's topic.",
  }),
];

let demoJob: AppJob | null = null;

// startDemoJob plays an install or removal over a few seconds.
function startDemoJob(id: string, action: AppJob['action']): AppJob {
  const app = demoApps.find((a) => a.id === id)!;
  const script =
    action === 'install'
      ? [
          '::step:: Finding the latest release',
          `::step:: Downloading ${app.name} 2.36.0`,
          '######################################################################## 100.0%',
          '::step:: Configuring',
          `::step:: Creating the ${id}.service service`,
          `Created symlink /etc/systemd/system/multi-user.target.wants/${id}.service → /etc/systemd/system/${id}.service.`,
          `::step:: Starting ${app.name}`,
          '::step:: Installed',
        ]
      : [`::step:: Stopping ${app.name}`, `Removed "/etc/systemd/system/multi-user.target.wants/${id}.service".`, '::step:: Removing files', '::step:: Removed'];
  const job: AppJob = { id: (demoJob?.id ?? 0) + 1, app: id, action, step: '', started: now(), ok: false, lines: [] };
  demoJob = job;
  script.forEach((line, i) =>
    setTimeout(
      () => {
        job.lines.push(line);
        if (line.startsWith('::step:: ')) job.step = line.slice(9);
        if (i === script.length - 1) {
          job.finished = now();
          job.ok = true;
          Object.assign(app, action === 'install' ? { installed: true, running: true, by: 'store', version: '2.36.0' } : { installed: false, running: false, by: undefined, version: undefined });
        }
      },
      500 * (i + 1),
    ),
  );
  return job;
}

function route(method: string, url: URL, body: unknown): Response {
  const path = url.pathname;
  const checkName = path.startsWith('/api/checks/') ? decodeURIComponent(path.slice('/api/checks/'.length)) : null;
  const findCheck = (name: string) => state.checks.findIndex((c) => c.name === name);

  if (method === 'GET') {
    switch (path) {
      case '/api/config':
        return json({
          auth_required: true,
          authorized: true,
          account: { configured: true, username: 'demo', service: 'server-monitor' },
          hidden: (state.settings.DASHBOARD_HIDDEN ?? '').split(',').filter(Boolean),
          status_page_enabled: true,
          status_page_title: 'Demo status',
          plex_enabled: true,
          qbittorrent_enabled: true,
          version: 'demo',
        } satisfies AppConfig);
      case '/api/stats':
        return json(stats());
      case '/api/services':
        return json({ services: state.checks.map(summary) } satisfies ServicesResponse);
      case '/api/services/history': {
        const spec = state.checks.find((c) => c.name === url.searchParams.get('name'));
        if (!spec) return error(404, 'Unknown service');
        const hours = Number(url.searchParams.get('hours') ?? 24);
        return json({ name: spec.name, hours, points: spec.paused ? [] : history(spec, hours) } satisfies HistoryResponse);
      }
      case '/api/checks':
        return json({ checks: state.checks, types: ['http', 'tcp', 'ping', 'tls'] } satisfies ChecksResponse);
      case '/api/settings':
        return json(settings());
      case '/api/plex':
        return json(plex(url.searchParams.get('view') === 'summary'));
      case '/api/updates':
        return json(updates());
      case '/api/qbittorrent':
        return json(qbittorrent());
      case '/api/units':
        return json({ enabled: true, self: 'server-monitor.service', units: demoUnits });
      case '/api/network':
        return json(network(url.searchParams.has('since') ? Number(url.searchParams.get('since')) : undefined));
      case '/api/drives':
        return json({ enabled: true, drives: demoDrives(), limits: { hdd: 55, ssd: 70 } });
      case '/api/status':
        return json(publicStatus());
      case '/api/apps':
        return json({ apps: demoApps, job: demoJob, user: 'media' });
      case '/api/apps/job': {
        const after = Number(url.searchParams.get('after') ?? 0);
        return json({ job: demoJob && { ...demoJob, lines: demoJob.lines.slice(after) } });
      }
      case '/api/logs':
        return json(logs(url));
      case '/api/notifications':
        return json(notifications(url));
      case '/api/notifications/summary':
        return json({ counts: notifyCounts(), mutes: liveMutes(), quiet: false, held: 0 });
      case '/api/notifications/rules':
        return json(notifyRules());
      case '/api/statuspage':
        return json({ enabled: true, designed: !!state.statusDesign, design: designOrDefault(), checks: state.checks.map((c) => c.name) });
    }
  }
  if (method === 'POST' && path === '/api/checks/test') {
    const spec = normalise(body as Partial<CheckSpec>);
    if (typeof spec === 'string') return error(400, spec);
    // Targets on .invalid or .test hosts never answer, so the demo can show a failed test.
    const offline = /\.(invalid|test)\b|offline/.test(spec.target);
    const ms = 20 + Math.round(noise(spec.target.length, 7) * 180);
    const detail = { http: 'HTTP 200', tcp: 'connected', ping: 'reply', tls: 'certificate valid for 61 days' }[spec.type];
    return json({ result: offline ? { up: false, latency_ms: null, detail: spec.type === 'ping' ? 'no reply' : 'connection timed out' } : { up: true, latency_ms: ms, detail } });
  }
  if (method === 'POST' && path === '/api/checks') {
    const spec = normalise(body as Partial<CheckSpec>);
    if (typeof spec === 'string') return error(400, spec);
    if (findCheck(spec.name) >= 0) return error(409, 'a check with that name already exists');
    state.checks.push(spec);
    save();
    return json({ check: spec }, 201);
  }
  if (method === 'PUT' && checkName !== null) {
    const i = findCheck(checkName);
    if (i < 0) return error(404, 'no check with that name');
    const spec = normalise(body as Partial<CheckSpec>);
    if (typeof spec === 'string') return error(400, spec);
    if (spec.name !== checkName && findCheck(spec.name) >= 0) return error(409, 'a check with that name already exists');
    state.checks[i] = spec;
    save();
    return json({ check: spec });
  }
  if (method === 'DELETE' && checkName !== null) {
    const i = findCheck(checkName);
    if (i < 0) return error(404, 'no check with that name');
    state.checks.splice(i, 1);
    save();
    return new Response(null, { status: 204 });
  }
  if (path.startsWith('/api/units/')) {
    const [, , , name, what] = path.split('/').map(decodeURIComponent);
    if (method === 'GET' && what === 'logs') return demoUnits.some((u) => u.name === name) ? json({ lines: demoLogs[name] ?? [] }) : error(404, 'no such service');
    if (method === 'POST') {
      const u = demoUnitAction(name, what);
      return u ? json({ unit: u }) : error(404, 'no such service');
    }
  }
  if (method === 'PUT' && path === '/api/statuspage') {
    state.statusDesign = body as StatusDesign;
    save();
    return json({ design: state.statusDesign });
  }
  if (path === '/api/setup')
    return json({
      alerts: { configured: true, channels: ['Discord'], webhook_set: true },
      plex: { enabled: true, url: 'http://localhost:32400', token_set: true, detected: { running: true, url: 'http://127.0.0.1:32400', version: '1.43.5', token_found: true } },
      qbittorrent: {
        enabled: false,
        host: 'localhost',
        port: 8080,
        username: 'admin',
        password_set: false,
        detected: { found: true, host: 'localhost', port: 10914, username: 'media', local_bypass: false, answering: true },
      },
      checks: {
        count: state.checks.length,
        suggestions: [
          { name: 'Sonarr', type: 'http' as const, target: 'http://127.0.0.1:8989/ping' },
          { name: 'Radarr', type: 'http' as const, target: 'http://127.0.0.1:7878/ping' },
          { name: 'Tautulli', type: 'http' as const, target: 'http://127.0.0.1:8181/status' },
        ].filter((c) => !state.checks.some((x) => x.target === c.target)),
      },
      updates: { enabled: (state.settings.SYSTEM_UPDATE_ENABLED ?? ENVIRONMENT.SYSTEM_UPDATE_ENABLED) === 'true', targets: updatesList().length, window: '04:00–06:00' },
    } satisfies SetupStatus);
  if (method === 'POST' && path === '/api/settings/test') {
    const { target, values } = body as { target: string; values: Record<string, string> };
    if (target === 'alerts') return json({ ok: true, results: [{ channel: 'Discord', ok: true }] });
    if (target === 'qbittorrent')
      return values.QBITTORRENT_PASSWORD ? json({ ok: true, detail: 'Connected to qBittorrent v5.1.4 · 7 torrent(s)' }) : json({ ok: false, detail: 'login failed (HTTP 200)' });
    return json({ ok: true, detail: 'Connected to demo-server (Plex 1.43.5)' });
  }
  if (path === '/api/qbittorrent/categories') return json({ categories: ['linux', 'movies', 'tv'] });
  if (method === 'POST' && path === '/api/qbittorrent/action') {
    const { action, hashes } = body as { action: string; hashes: string[] };
    if (!hashes?.length) return error(502, 'no torrents given');
    qbAct(action, hashes);
    return json(qbittorrent());
  }
  if (method === 'POST' && path === '/api/qbittorrent/add') {
    const { urls, category, stopped } = body as { urls: string; category: string; stopped: boolean };
    const lines = urls
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean);
    if (!lines.length || lines.some((l) => !/^(magnet:\?|https?:\/\/)/.test(l))) return error(400, 'give magnet links or http(s) URLs');
    for (const l of lines) {
      const name = decodeURIComponent(/[?&]dn=([^&]+)/.exec(l)?.[1] ?? l.split('/').pop() ?? 'new torrent').replace(/\+/g, ' ');
      const hash = Array.from({ length: 40 }, (_, i) => '0123456789abcdef'[(name.charCodeAt(i % name.length) + i) % 16]).join('');
      qbChanges.added.push(torrent({ hash, name, state: stopped ? 'stoppeddl' : 'metadl', size: 0, progress: 0, category, added_on: now() }));
    }
    return json({ added: lines.length }, 201);
  }
  if (path.startsWith('/api/updates')) {
    const parts = path.split('/').slice(3).map(decodeURIComponent); // after /api/updates
    const list = updatesList();
    const found = parts[0] ? list.find((t) => t.id === parts[0]) : undefined;
    if (method === 'POST' && parts[0] === 'check') {
      list.forEach(checkDemo);
      return json({ queued: true }, 202);
    }
    if (method === 'POST' && parts[0] === 'detect') return json({ added: [] });
    if (method === 'POST' && parts.length === 0) {
      const t = saveDemoTarget(body as UpdateSpec);
      if (typeof t === 'string') return error(400, t);
      if (list.some((u) => u.id === t.id)) return error(409, 'an update target with that id already exists');
      list.push(t);
      checkDemo(t);
      save();
      return json({ target: t }, 201);
    }
    if (!found) return error(404, 'no update target with that id');
    if (method === 'PUT') {
      const t = saveDemoTarget(body as UpdateSpec, found);
      if (typeof t === 'string') return error(400, t);
      Object.assign(found, t);
      save();
      return json({ target: found });
    }
    if (method === 'DELETE') {
      list.splice(list.indexOf(found), 1);
      save();
      return new Response(null, { status: 204 });
    }
    if (method === 'POST' && parts[1] === 'check') {
      checkDemo(found);
      return json({ queued: true }, 202);
    }
    if (method === 'POST' && parts[1] === 'update') {
      if (!found.can_update) return error(400, 'this target has no update command; it is only checked');
      applyDemoUpdate(found);
      save();
      return json({ queued: true }, 202);
    }
  }
  const appAction = path.match(/^\/api\/apps\/([a-z]+)\/(install|remove)$/);
  if (method === 'POST' && appAction) {
    if (demoJob && !demoJob.finished) return error(409, 'another install is running; wait for it to finish');
    return json({ job: startDemoJob(appAction[1], appAction[2] as AppJob['action']) }, 202);
  }
  if (method === 'POST' && path === '/api/auth/password') return json({ changed: true });
  if (method === 'POST' && path === '/api/auth/logout') return new Response(null, { status: 204 });
  if (method === 'POST' && path === '/api/notifications/read') {
    const b = body as { ids?: number[]; all?: boolean; unread?: boolean };
    for (const n of demoNotify.items) if (b.all || b.ids?.includes(n.id)) n.read = !b.unread;
    return json({ counts: notifyCounts() });
  }
  if (method === 'PUT' && path === '/api/notifications/rules') {
    const b = body as { rules: AlertRules; quiet: QuietHours };
    demoNotify.rules = { ...notifyDefaults(), ...b.rules };
    demoNotify.quiet = b.quiet;
    return json(notifyRules());
  }
  if (method === 'POST' && path === '/api/notifications/mute') {
    const b = body as { subject: string; label: string; minutes: number };
    demoNotify.mutes = demoNotify.mutes.filter((m) => m.subject !== b.subject);
    if (b.minutes > 0) demoNotify.mutes.push({ subject: b.subject, label: b.label || b.subject, until: now() + b.minutes * 60 });
    return json({ mutes: liveMutes() });
  }
  if (method === 'POST' && path === '/api/alerts/test') {
    const set = (key: string) => (state.settings[key] ?? ENVIRONMENT[key] ?? '') !== '';
    const channels: [string, string][] = [
      ['Discord', 'WEBHOOK'],
      ['ntfy', 'NTFY_URL'],
      ['Telegram', 'TELEGRAM_BOT_TOKEN'],
      ['Slack', 'SLACK_WEBHOOK'],
      ['Webhook', 'ALERT_WEBHOOK_URL'],
      ['Email', 'SMTP_HOST'],
    ];
    return json({ results: channels.filter(([, key]) => set(key)).map(([channel]) => ({ channel, ok: true })) });
  }
  if (method === 'PUT' && path === '/api/settings') {
    const values = (body as { values?: Record<string, string> }).values ?? {};
    const problems = validate(values);
    if (Object.keys(problems).length) return error(400, 'Some settings are invalid', problems);
    for (const [key, value] of Object.entries(values)) {
      if (value === '') delete state.settings[key];
      else state.settings[key] = value;
    }
    save();
    const restartFor = Object.keys(values).filter((k) => !DEMO_LIVE.has(k));
    return restartFor.length ? json({ restarting: true, restart_for: restartFor }, 202) : json({ restarting: false, applied: true });
  }
  return error(404, 'Not Found');
}

// demoFetch answers API requests from the demo data, after a short, realistic delay.
export async function demoFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.origin);
  const body = typeof init.body === 'string' ? JSON.parse(init.body) : undefined;
  await new Promise((r) => setTimeout(r, 60 + Math.random() * 120));
  return route((init.method ?? 'GET').toUpperCase(), url, body);
}
