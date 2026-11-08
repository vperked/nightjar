// Demo data for the dashboard when no backend is running (see transport in client.ts). Readings
// are generated from the current time so charts move; checks and settings can be edited and are kept
// for the browser tab. Production builds never include this file.
import type {
  Finding,
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
  PublicDay,
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
  AnalyticsPoint,
  AnalyticsRangeName,
  HostAnalytics,
  RemoteHost,
  Attack,
  AttackDefense,
  AttackView,
  FirewallPort,
  FirewallRule,
  FirewallStatus,
  HostSSH,
  SSHCheck,
  SSHKey,
  Appearance,
  MaintenanceWindow,
} from './types';
import { demoPluginCheck, pluginRoute } from './demoPlugins';
import {
  demoActivity,
  demoCleanup,
  demoTwoFactor,
  demoTwoFactorRoute,
  demoCleanupKeep,
  demoCleanupScan,
  demoContainers,
  demoDeleteWindow,
  demoFeedback,
  demoHeartbeatDetail,
  demoIncidents,
  demoMaintenance,
  demoNotice,
  demoPlexStats,
  demoPings,
  demoPublicMaintenance,
  demoPublicNotices,
  demoRepairNow,
  demoRepairs,
  demoReport,
  demoResting,
  demoSaveWindow,
} from './demoIncidents';

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
  appearance?: Appearance;
}

const STORE_KEY = 'nightjar.demo';

const check = (over: Partial<CheckSpec> & Pick<CheckSpec, 'name' | 'type' | 'target'>): CheckSpec => ({
  interval: 60,
  timeout: 10,
  retries: 2,
  expected_status: null,
  keyword: '',
  verify_tls: true,
  tls_warn_days: 14,
  paused: false,
  retry_interval: 20,
  ping_count: 3,
  ping_interval: 0.2,
  ...over,
});

const INITIAL: DemoState = {
  checks: [
    check({ name: 'website', type: 'http', target: 'https://example.com' }),
    check({ name: 'plex', type: 'http', target: 'http://127.0.0.1:32400/identity', interval: 30, repair: { action: 'restart', unit: 'plexmediaserver.service', max: 2, wait: 120 } }),
    check({ name: 'home-assistant', type: 'http', target: 'http://192.168.0.31:8123', interval: 30, server: 'b2c3d4e5f6a1' }),
    check({ name: 'nas-backup', type: 'tcp', target: '192.168.0.20:873', retries: 1, repair: { action: 'script', script: 'restart-rsync.sh', max: 2, wait: 120 }, server: 'a1b2c3d4e5f6' }),
    check({ name: 'sonarr', type: 'docker', target: 'sonarr', repair: { action: 'container', container: 'sonarr', max: 2, wait: 120 } }),
    check({ name: 'nightly-backup', type: 'heartbeat', target: 'q7Lr2xVb9KpTn4WcYs3mEa', interval: 30, retries: 0, schedule: '0 3 * * *', grace: 600, max_runtime: 3600 }),
    check({ name: 'router', type: 'ping', target: '10.0.0.1', interval: 30 }),
    check({ name: 'vps web', type: 'http', target: 'https://vps.example.net', server: 'f6a1b2c3d4e5' }),
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

// nextMonday is when the demo's weekly report would next go out.
function nextMonday(): number {
  const d = new Date();
  d.setDate(d.getDate() + ((1 - d.getDay() + 7) % 7 || 7));
  d.setHours(9, 0, 0, 0);
  return d.getTime() / 1000;
}

const demoAppearance = (): Appearance => state.appearance ?? { style: 'matrix', charts: '', density: 'comfortable', css: '' };

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
  unit('nightjar', 'Nightjar', { since: now() - 3600 }),
  unit('apt-daily', 'Daily apt download activities', { active: 'failed', sub: 'failed', result: 'exit-code', exit_status: 100, ignored: true }),
  unit('apparmor', 'Load AppArmor profiles', { sub: 'exited' }),
];

// What the backend's log diagnosis says about the demo's failing services and checks.
const demoFindings: Record<string, Finding[]> = {
  'bazarr.service': [
    {
      id: 'db_corrupt',
      title: 'Its database is damaged',
      explain: 'The database file is corrupt, often after a crash, a full disk or a power cut.',
      fix: 'Stop the app and restore its last backup (most *arr apps keep them under Backups). Check the drive’s health too.',
      severity: 'critical',
      line: 'ERROR: database disk image is malformed',
      count: 1,
      known: true,
    },
  ],
  'tautulli.service': [
    {
      id: 'py_module',
      title: 'Python module cherrypy is missing',
      explain: 'A Python package it needs is not installed where it looks for it.',
      fix: 'Install it into the app’s own environment (`pip install cherrypy` inside its venv), or reinstall the app.',
      severity: 'critical',
      line: "ModuleNotFoundError: No module named 'cherrypy'",
      count: 5,
      known: true,
    },
  ],
  'check:refused': [
    {
      id: 'refused',
      title: 'Nothing answers at the address it uses',
      explain: 'It tried to connect, but nothing is listening there: that program is down, or on another port.',
      fix: 'Start the program it depends on, or correct the address and port in its settings.',
      severity: 'warning',
      line: 'connect failed: connection refused',
      count: 1,
      known: true,
    },
  ],
  log: [
    {
      id: 'db_corrupt',
      title: 'Its database is damaged',
      explain: 'The database file is corrupt, often after a crash, a full disk or a power cut.',
      fix: 'Stop the app and restore its last backup (most *arr apps keep them under Backups). Check the drive’s health too.',
      severity: 'critical',
      line: 'Likely cause unit=bazarr.service cause=Its database is damaged',
      count: 1,
      known: true,
    },
    {
      id: 'repeated',
      title: 'Failed sign-in ip=203.0.113.9',
      explain: 'An error the logs repeat, seen 3 times in these lines. The latest is below.',
      severity: 'warning',
      line: 'Failed sign-in ip=203.0.113.9',
      count: 3,
      known: false,
    },
  ],
};

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

// The failing services carry the cause their journal shows, until they run again.
for (const u of demoUnits) u.cause = demoFindings[u.name]?.[0];

function demoUnitAction(name: string, action: string): Unit | undefined {
  const u = demoUnits.find((x) => x.name === name);
  if (!u) return undefined;
  if (action !== 'stop') u.cause = undefined;
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
    runs: [
      {
        at: hoursAgo(26),
        ok: true,
        auto: true,
        detail: '7 package(s) upgraded with apt',
        log: [
          'Updating OS packages (apt): 7 packages',
          '$ apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade',
          '7 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.',
          'Setting up openssl (3.0.13-0ubuntu3.6) ...',
          'Checking the version now installed',
          'Done',
        ],
      },
    ],
  }),
  target({ id: 'snaps', name: 'Snaps', kind: 'snap' }),
  target({
    id: 'nightjar',
    name: 'Nightjar',
    kind: 'git',
    dir: '/opt/nightjar',
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

// demoUpdateLog is what a demo update prints, step by step, as the backend's live log shows it.
function demoUpdateLog(t: UpdateTarget): string[] {
  const change = `${t.installed ?? ''} → ${t.latest ?? ''}`;
  if (t.kind === 'arr')
    return [
      `Updating ${t.name}: ${change}`,
      `Asking ${t.name} to update itself to ${t.latest}`,
      'Waiting for it to download the update and restart',
      'It stopped answering: restarting',
      `It answers, on ${t.latest}`,
      'Checking it is running',
      '$ systemctl is-active ' + (t.unit ?? t.id),
      'active',
      'Checking the version now installed',
      'Done',
    ];
  if (t.kind === 'system')
    return [
      `Updating ${t.name}: ${t.pending?.length ?? 0} packages`,
      '$ apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade',
      'Reading package lists...',
      'Building dependency tree...',
      `${t.pending?.length ?? 0} upgraded, 0 newly installed, 0 to remove and 0 not upgraded.`,
      'Get:1 http://archive.ubuntu.com/ubuntu noble-updates/main amd64 openssl amd64 3.0.13-0ubuntu3.7 [1,005 kB]',
      'Fetched 1,005 kB in 1s (1,370 kB/s)',
      'Unpacking openssl (3.0.13-0ubuntu3.7) over (3.0.13-0ubuntu3.6) ...',
      'Setting up openssl (3.0.13-0ubuntu3.7) ...',
      'Checking the version now installed',
      '$ apt-get -s upgrade',
      'Done',
    ];
  return [
    `Updating ${t.name}: ${change}`,
    `$ sh -c ${t.update_command ?? 'update'}`,
    `Downloading ${t.name} ${t.latest}`,
    'Installed',
    'Checking it is running',
    'Checking the version now installed',
    'Done',
  ];
}

// The demo updates one at a time, as the server does: the rest wait in a queue.
const demoQueue: string[] = [];

function applyDemoUpdate(t: UpdateTarget) {
  if (updatesList().some((u) => u.busy === 'updating')) {
    t.busy = 'queued';
    demoQueue.push(t.id);
    return;
  }
  runDemoUpdate(t);
}

function nextDemoUpdate() {
  const id = demoQueue.shift();
  const t = id ? updatesList().find((u) => u.id === id) : undefined;
  if (t) runDemoUpdate(t);
  else if (id) nextDemoUpdate();
}

function runDemoUpdate(t: UpdateTarget) {
  const lines = demoUpdateLog(t);
  const step = 600;
  t.busy = 'updating';
  t.live = { started: now(), lines: [] };
  // The log fills a line at a time, as a real update's does.
  lines.forEach((_, i) =>
    later(t.id, step * (i + 1), (u) => {
      if (u.live) u.live = { ...u.live, lines: lines.slice(0, i + 1) };
    }),
  );
  later(t.id, step * (lines.length + 1), (u) => {
    const from = u.installed;
    u.runs = [
      { at: now(), from, to: u.latest ?? from, ok: true, auto: false, detail: u.kind === 'system' ? `${u.pending?.length ?? 0} package(s) upgraded with apt` : undefined, log: lines },
      ...(u.runs ?? []),
    ].slice(0, 5);
    if (u.kind === 'release') u.installed = u.latest?.replace(/^v/, '');
    else u.installed = u.latest;
    u.available = false;
    u.pending = null;
    u.busy = undefined;
    u.live = undefined;
    u.checked = now();
    setTimeout(nextDemoUpdate, 0);
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
    flaky: name === 'home-assistant' ? 0.012 : name === 'nightly-backup' ? 0 : 0.003,
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
  const status: Service['status'] = spec.paused ? 'paused' : demoResting(spec.name) ? 'maintenance' : last?.up ? 'up' : 'down';
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
  const heartbeat = spec.type === 'heartbeat';
  const pings = heartbeat ? demoPings() : undefined;
  return {
    name: spec.name,
    type: spec.type,
    target: spec.target,
    status,
    latency_ms: heartbeat && pings ? (pings.ok - pings.start) * 1000 : (last?.latency_ms ?? null),
    detail: heartbeat && status === 'up' ? demoHeartbeatDetail() : spec.type === 'docker' && status === 'up' ? 'container healthy, started 3d ago' : detail,
    schedule: spec.schedule,
    pings,
    repair: spec.repair,
    server: spec.server,
    last_check: last?.ts ?? null,
    last_change: status === 'down' ? now() - 25 * 60 : now() - 6 * 86400,
    cause: status === 'down' && spec.type === 'tcp' ? demoFindings['check:refused'][0] : undefined,
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

// Streams stopped from the dashboard, with when, until the page reloads.
const demoStopped = new Map<string, number>();

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
  const all = [
    stream('1', {
      address: '192.168.1.42',
      title: 'Severance',
      kind: 'episode',
      subtitle: 'S02E03 · Who Is Alive?',
      duration_ms: 3_300_000,
      offset_ms: 1_900_000,
      progress: 58,
      started_at: t - 1900,
      source: { video: 'H.264', audio: 'Dolby Digital Plus 5.1', container: 'MKV', resolution: '1080p' },
      audio_track: 'English (EAC3 5.1)',
      peak_kbps: 14800,
    }),
    stream('2', {
      user: 'alex',
      title: 'Heat',
      subtitle: '1995',
      player: 'iPhone',
      product: 'Plex for iOS',
      platform: 'iOS',
      local: false,
      address: '203.0.113.58',
      decision: 'transcode',
      video_decision: 'transcode',
      reason: 'Lowering the quality to 720p',
      fix: "The player's remote quality is set below the file's, or the server's upload limit is too low: raise either (Plex Settings → Remote Access).",
      audio_decision: 'copy',
      hw_transcode: true,
      hw_decode: 'Intel (VA-API)',
      hw_encode: 'Intel (VA-API)',
      throttled: true,
      speed: 2.4,
      source: { video: 'HEVC', audio: 'Dolby TrueHD 7.1', container: 'MKV', resolution: '4K' },
      output: { video: 'H.264', audio: 'Dolby TrueHD 7.1', container: 'MPEGTS', resolution: '720p' },
      audio_track: 'English (TRUEHD 7.1)',
      subtitle_track: 'English (SRT)',
      subtitle_decision: 'copy',
      peak_kbps: 6100,
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
      address: '192.168.1.17',
      state: 'paused',
      quality: '',
      bitrate_kbps: 320,
      bandwidth_kbps: 330,
      source: { audio: 'FLAC stereo', container: 'FLAC' },
      duration_ms: 330_000,
      offset_ms: 120_000,
      progress: 36,
      started_at: t - 400,
    }),
  ];
  const streams = all.filter((s) => !demoStopped.has(s.id));
  const samples: PlexSample[] = Array.from({ length: 360 }, (_, i) => {
    const ts = t - (359 - i) * 15;
    const evening = Math.max(0, Math.sin((ts / 5400) * Math.PI));
    const direct = Math.round(1 + evening * 2 + noise(Math.floor(ts / 600), 3));
    const transcode = noise(Math.floor(ts / 900), 4) > 0.55 ? 1 : 0;
    return { ts, direct, transcode, bandwidth_kbps: Math.round(direct * 8200 + transcode * 4300 + noise(ts, 5) * 900) };
  });
  // Finished streams, kept as they were last seen.
  const andor = stream('p1', {
    title: 'Andor',
    kind: 'episode',
    subtitle: 'S02E01 · One Year Later',
    address: '192.168.1.42',
    source: { video: 'HEVC', audio: 'Dolby Digital Plus 5.1', container: 'MKV', resolution: '4K' },
    audio_track: 'English (EAC3 5.1)',
    quality: '4K',
    bitrate_kbps: 18400,
    bandwidth_kbps: 19100,
    peak_kbps: 24300,
    duration_ms: 2_880_000,
    offset_ms: 2_822_000,
    progress: 98,
  });
  const dune = stream('p2', {
    user: 'alex',
    title: 'Dune: Part Two',
    subtitle: '2024',
    player: 'iPhone',
    product: 'Plex for iOS',
    platform: 'iOS',
    local: false,
    address: '203.0.113.58',
    decision: 'transcode',
    video_decision: 'transcode',
    audio_decision: 'transcode',
    subtitle_decision: 'burn',
    reason: 'Burning in subtitles',
    fix: 'Pick a text subtitle (SRT), or turn subtitles off: picture subtitles (PGS, VobSub) have to be drawn into the video.',
    hw_transcode: true,
    hw_decode: 'Intel (VA-API)',
    hw_encode: 'Intel (VA-API)',
    source: { video: 'HEVC', audio: 'Dolby TrueHD 7.1', container: 'MKV', resolution: '4K' },
    output: { video: 'H.264', audio: 'AAC stereo', container: 'MPEGTS', resolution: '1080p' },
    audio_track: 'English (TRUEHD 7.1)',
    subtitle_track: 'English (PGS)',
    quality: '4K',
    bitrate_kbps: 8000,
    bandwidth_kbps: 7600,
    peak_kbps: 11200,
    duration_ms: 9_960_000,
    offset_ms: 9_960_000,
    progress: 100,
  });
  const spirited = stream('p3', {
    user: 'kim',
    title: 'Spirited Away',
    subtitle: '2001',
    player: 'Bedroom',
    product: 'Plex for LG',
    platform: 'webOS',
    address: '192.168.1.31',
    decision: 'direct stream',
    video_decision: 'copy',
    audio_decision: 'copy',
    source: { video: 'H.264', audio: 'DTS 5.1', container: 'MKV', resolution: '1080p' },
    output: { video: 'H.264', audio: 'DTS 5.1', container: 'MPEGTS', resolution: '1080p' },
    audio_track: 'Japanese (DTS 5.1)',
    subtitle_track: 'English (SRT)',
    subtitle_decision: 'copy',
    bandwidth_kbps: 11200,
    peak_kbps: 12900,
    duration_ms: 7_500_000,
    offset_ms: 5_550_000,
    progress: 74,
  });
  const ended = (s: PlexStream, started: number, at: number): PlexPlay => ({
    user: s.user,
    kind: s.kind,
    title: s.title,
    subtitle: s.subtitle,
    player: s.player,
    decision: s.decision,
    started,
    ended: at,
    progress: s.progress,
    local: s.local,
    duration_ms: s.duration_ms,
    reason: s.reason,
    last: { ...s, started_at: started },
  });
  const plays: PlexPlay[] = [
    ...all.filter((s) => demoStopped.has(s.id)).map((s) => ended(s, s.started_at, demoStopped.get(s.id)!)),
    ended(andor, t - 9000, t - 6300),
    ended(dune, t - 20000, t - 11000),
    ended(spirited, t - 40000, t - 33000),
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
      direct: streams.filter((s) => s.decision !== 'transcode').length,
      transcode: streams.filter((s) => s.decision === 'transcode').length,
      bandwidth_kbps: streams.reduce((a, s) => a + s.bandwidth_kbps, 0),
      wan_kbps: streams.filter((s) => !s.local).reduce((a, s) => a + s.bandwidth_kbps, 0),
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

const f = (group: string, key: string, label: string, kind: SettingField['kind'], def: string, extra: Partial<FieldDef> = {}): FieldDef => ({ key, group, label, kind, default: def, ...extra });

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
  f('Dashboard', 'STATUS_PAGE_DOMAIN', 'Status page domain', 'text', '', { help: 'A domain of its own, such as status.example.com.' }),
  f('Dashboard', 'STATUS_PAGE_PATH', 'Status page path', 'text', '/status', { help: "Where it is on the dashboard's own address." }),
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
  // A new heartbeat gets its token, as the backend gives it one.
  const target = spec.target?.trim() || (spec.type === 'heartbeat' ? Math.random().toString(36).slice(2, 13) + Math.random().toString(36).slice(2, 13) : '');
  if (spec.type === 'heartbeat' && !spec.schedule?.trim()) return `${name}: a heartbeat needs a schedule: cron ("0 3 * * *"), @daily, or "every 1h"`;
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

// demoDays is a check's last 90 days for the status page: mostly clean, with the odd bad day, and
// nas-backup down since this morning. home-assistant was added three weeks ago.
function demoDays(name: string): PublicDay[] {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const seed = [...name].reduce((n, c) => n + c.charCodeAt(0), 0);
  return Array.from({ length: 90 }, (_, i) => {
    const t = today.getTime() / 1000 - (89 - i) * 86400;
    if (name === 'home-assistant' && i < 69) return { t, uptime: null, down_s: 0 };
    if (name === 'nas-backup' && i === 89) return { t, uptime: 61.2, down_s: 6 * 3600 + 1200 };
    const r = noise(i, seed);
    const uptime = r > 0.995 ? 91 + 6 * noise(i, seed + 1) : r > 0.965 ? 99 + 0.85 * noise(i, seed + 2) : 100;
    return { t, uptime: round(uptime, 2), down_s: Math.round(((100 - uptime) / 100) * 86400) };
  });
}

const daysUptime = (days: PublicDay[]) => {
  const known = days.filter((d) => d.uptime !== null);
  return known.length ? known.reduce((n, d) => n + d.uptime!, 0) / known.length : null;
};

function publicStatus(): PublicStatus {
  const d = designOrDefault();
  const byName = new Map(state.checks.map((c) => [c.name, summary(c)]));
  const groups = d.groups.map((g) => ({
    name: g.name,
    services: g.items.flatMap((it) => {
      const r = byName.get(it.check);
      return r
        ? [
            {
              name: it.label || r.name,
              status: r.status,
              uptime_24h: d.show_uptime ? r.uptime_24h : null,
              uptime_30d: d.show_uptime ? r.uptime_30d : null,
              uptime_90d: d.show_uptime ? daysUptime(demoDays(r.name)) : null,
              recent: d.show_history ? r.recent : [],
              days: d.show_history ? demoDays(r.name) : [],
            },
          ]
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
    css: d.css,
    header_html: d.header_html,
    footer_html: d.footer_html,
    show_uptime: d.show_uptime,
    show_history: d.show_history,
    announcement: d.announcement ? { text: d.announcement, tone: d.tone } : undefined,
    maintenance: demoPublicMaintenance(),
    incidents: demoPublicNotices(),
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
    n(4 * 86400 + 3000, 'storage', 'warning', '/mnt/media is 86% full', 'Above the 85% limit.', {
      fields: [
        { name: 'Used', value: '6.2 TB' },
        { name: 'Free', value: '1.1 TB' },
        { name: 'Size', value: '7.3 TB' },
      ],
    }),
    n(2 * 86400 + 600, 'torrents', 'ok', 'Downloaded: Sintel.2010.1080p.mkv', '', { status: 'kept', channels: [] }),
    n(26 * 3600, 'uptime', 'critical', 'nas-backup is down', 'Dial tcp 10.0.0.30:873: connect: connection refused', {
      subject: 'check:nas-backup',
      fields: [
        { name: 'Target', value: '`10.0.0.30:873`' },
        { name: 'Uptime today', value: '97.92%' },
      ],
    }),
    n(25.5 * 3600, 'uptime', 'ok', 'nas-backup is back up', 'Connected in 3 ms', { subject: 'check:nas-backup' }),
    n(7.2 * 3600, 'services', 'warning', 'radarr keeps restarting', 'Radarr Daemon restarted 4 times in the last ten minutes.', { subject: 'unit:radarr.service', status: 'digest' }),
    n(6.8 * 3600, 'resources', 'warning', 'Memory at 94%', 'Above the 90% limit, with 1.0 GB of 15.6 GB free.', { status: 'digest', fields: [{ name: 'Used', value: '14.6 GB of 15.6 GB' }] }),
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
  return { entries, latest: demoLog.length, components: [...new Set(demoLog.map((e) => e.component ?? ''))].sort(), level: 'INFO', file: 'nightjar.log', kept: 3000 };
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
  storeApp('navidrome', 'Navidrome', 'Media', '#0d6efd', 4533, 'Your own music streaming server: plays your music library in the browser and in Subsonic apps on your phone.', {
    sign_in: 'Create the admin account on first visit. Point MusicFolder in /var/lib/navidrome/navidrome.toml at your music, then restart it. Install ffmpeg for transcoding.',
  }),
  storeApp('ombi', 'Ombi', 'Media', '#e0752d', 5000, 'Lets the people you share Plex with request shows and films; requests go to Sonarr and Radarr.', {
    sign_in: 'Run the setup wizard on first visit: create the admin account, then connect Plex, Sonarr and Radarr.',
  }),
  storeApp('syncthing', 'Syncthing', 'Files and sync', '#0891d1', 8384, 'Keeps folders in sync between this server, your computers and your phone, directly and privately.', {
    installed: true,
    running: true,
    by: 'store',
    version: '2.1.7',
    sign_in: 'Open it and set a username and password under Actions → Settings → GUI; it warns until you do.',
  }),
  storeApp('filebrowser', 'File Browser', 'Files and sync', '#40c4ff', 8090, "Browse, upload, download and share the server's files from any browser.", {
    sign_in: 'Username admin. A generated password is printed at the end of the install log; change it under Settings.',
  }),
  storeApp('adguardhome', 'AdGuard Home', 'Network', '#68bc71', 3080, 'Blocks ads and trackers for every device on your network, as its DNS server.', {
    sign_in:
      "Run the setup wizard on first visit. On Ubuntu, systemd-resolved already uses port 53 on 127.0.0.53: listen for DNS on this server's network address, not on all interfaces. Then point your router's DNS at this server.",
  }),
  storeApp('gitea', 'Gitea', 'Self-hosting', '#609926', 3000, 'Your own GitHub: git repositories, issues, pull requests and a web editor, light enough for a Raspberry Pi.', {
    sign_in: 'Finish the install page on first visit (SQLite is fine for a home server); the first account made there is the admin.',
  }),
  storeApp('memos', 'Memos', 'Self-hosting', '#4b5563', 5230, 'Quick notes, like a private Twitter for your thoughts, links and to-dos, kept on your server.', {
    sign_in: 'The first account you create is the admin.',
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

// ---------- other servers ----------

// Each demo server is a seed and a character: a cool NAS, a Raspberry Pi running hot, and a backup
// box that went offline twenty minutes ago.
const demoHosts: { id: string; name: string; address: string; seed: number; temp: number; cpu: number; mem: number; arch: string; os: string; cores: number; offlineSince?: number }[] = [
  { id: 'a1b2c3d4e5f6', name: 'nas', address: 'https://192.168.0.20:9101', seed: 3, temp: 47, cpu: 9, mem: 38, arch: 'amd64', os: 'debian 12.8', cores: 4 },
  { id: 'b2c3d4e5f6a1', name: 'pi-dns', address: 'https://192.168.0.31:9101', seed: 7, temp: 79, cpu: 41, mem: 62, arch: 'arm64', os: 'debian 12.8', cores: 4 },
  { id: 'c3d4e5f6a1b2', name: 'backup-box', address: 'https://192.168.0.44:9101', seed: 11, temp: 39, cpu: 3, mem: 21, arch: 'amd64', os: 'ubuntu 24.04', cores: 2, offlineSince: now() - 1200 },
];

function demoHost(i: number, points: number): RemoteHost {
  const d = demoHosts[i];
  const end = d.offlineSince ?? now();
  const at = (t: number) => ({
    cpu: clamp(wave(t, d.cpu, d.cpu * 0.4, 600, d.seed, 0.6), 1, 100),
    mem: clamp(wave(t, d.mem, 2, 1800, d.seed + 1, 0.02), 1, 100),
    temp: clamp(wave(t, d.temp, 3, 900, d.seed + 2, 0.03), 20, 110),
  });
  const history = Array.from({ length: points }, (_, k) => {
    const t = Math.floor(end / 10) * 10 - (points - 1 - k) * 10;
    const v = at(t);
    return { t, cpu: round(v.cpu), mem: round(v.mem), temp: round(v.temp) };
  });
  const v = at(end);
  const disks = [
    { device: '/dev/sda2', mountpoint: '/', fstype: 'ext4', total: 64 * GB, used: 0, free: 0, percent: 31 + d.seed, is_nvme: false },
    ...(d.name === 'nas'
      ? [{ device: '/dev/md0', mountpoint: '/srv/storage', fstype: 'ext4', total: 16000 * GB, used: 0, free: 0, percent: 93, is_nvme: false }]
      : d.name === 'backup-box'
        ? [{ device: '/dev/sdb1', mountpoint: '/backup', fstype: 'xfs', total: 4000 * GB, used: 0, free: 0, percent: 71, is_nvme: false }]
        : []),
  ].map((p) => ({ ...p, used: (p.total * p.percent) / 100, free: (p.total * (100 - p.percent)) / 100 }));
  const problems = [...(v.temp >= 80 ? [`CPU at ${Math.round(v.temp)}°C`] : []), ...disks.filter((p) => p.percent >= 90).map((p) => `${p.mountpoint} ${p.percent}% full`)];
  if (d.offlineSince) problems.unshift('Offline: no answer from ' + d.address + ': timed out (a firewall is blocking the port: on the server, or on a cloud server in the provider’s firewall too)');
  return {
    id: d.id,
    name: d.name,
    address: d.address,
    kind: 'agent',
    added: now() - 86400 * (5 + d.seed),
    status: d.offlineSince ? 'offline' : 'online',
    last_seen: end,
    error: d.offlineSince ? `no answer from ${d.address}: timed out (a firewall is blocking the port: on the server, or on a cloud server in the provider's firewall too)` : undefined,
    history,
    problems,
    readings: {
      version: 1,
      agent: 'v1.4.0',
      time: end,
      system: {
        hostname: d.name,
        platform: 'Linux',
        platform_release: d.arch === 'arm64' ? '6.6.51+rpt-rpi-2712' : '6.1.0-28-amd64',
        platform_version: d.os,
        architecture: d.arch,
        boot_time: end - 86400 * (12 + d.seed),
        uptime_seconds: 86400 * (12 + d.seed),
        uptime_formatted: '',
        users: 0,
      },
      cpu: {
        percent: round(v.cpu),
        cores: Array.from({ length: d.cores }, (_, c) => round(clamp(v.cpu * (0.5 + noise(c + Math.floor(end / 5), d.seed)), 0, 100))),
        count: d.cores,
        load: [round((v.cpu / 100) * d.cores, 2), round((v.cpu / 110) * d.cores, 2), round((v.cpu / 120) * d.cores, 2)],
        freq_mhz: d.arch === 'arm64' ? 2400 : 3400,
      },
      memory: { total: 8 * GB, used: (8 * GB * v.mem) / 100, free: (8 * GB * (100 - v.mem)) / 100, available: (8 * GB * (100 - v.mem)) / 100, percent: round(v.mem) },
      swap: { total: 2 * GB, used: 0.1 * GB, free: 1.9 * GB, available: 1.9 * GB, percent: 5 },
      temperature: round(v.temp),
      fans: d.arch === 'arm64' ? [{ name: 'fan1', label: 'pwmfan', speed_rpm: 4120 }] : [],
      gpu: null,
      disks,
      network: {
        bytes_recv_per_sec: Math.max(0, wave(end, d.name === 'nas' ? 4.2e6 : 9e4, 6e4, 300, d.seed, 0.5)),
        bytes_sent_per_sec: Math.max(0, wave(end, d.name === 'nas' ? 1.1e6 : 4e4, 3e4, 300, d.seed + 3, 0.5)),
      },
      disk_io: { read_per_sec: d.name === 'nas' ? 2.4e6 : 1.2e4, write_per_sec: d.name === 'nas' ? 8e5 : 3e4 },
      drives:
        d.name === 'nas'
          ? [
              {
                device: '/dev/sda',
                type: 'sat',
                model: 'WDC WD80EFAX-68KNBN0',
                capacity: 8001563222016,
                rotational: true,
                passed: true,
                temperature: 36,
                power_on_hours: 31022,
                asleep: false,
                checked: end - 200,
                health: 'good' as const,
              },
              {
                device: '/dev/sdb',
                type: 'sat',
                model: 'WDC WD80EFAX-68KNBN0',
                capacity: 8001563222016,
                rotational: true,
                passed: true,
                temperature: 38,
                power_on_hours: 31020,
                asleep: false,
                checked: end - 200,
                health: 'good' as const,
              },
            ]
          : [],
      drives_error: d.name === 'nas' ? undefined : 'smartctl is not installed (apt install smartmontools)',
      attacks: d.offlineSince ? undefined : demoAttacks(d.name === 'pi-dns'),
    },
  };
}

// demoFirewalls are each machine's firewall, made on first look: ufw on this machine with rules set
// from here, what they caught and dropped; plain iptables on the agents.
const demoFirewalls: Record<string, FirewallStatus> = {};
function demoFirewall(id: string): FirewallStatus {
  const ports: FirewallPort[] = [
    { port: 22, proto: 'tcp', program: 'sshd' },
    { port: 53, proto: 'udp', program: 'systemd-resolve', local: true },
    { port: 9101, proto: 'tcp', program: 'nightjar-agent' },
  ];
  const ssh = { port: 22, what: 'SSH' };
  if (id === 'local')
    return {
      full: true,
      tool: 'ufw',
      policy: 'DROP',
      default: '',
      open: [ssh, { port: 80, what: 'web' }, { port: 443, what: 'web' }, { port: 8080, what: 'this dashboard' }],
      rules: [
        { id: 'r1', action: 'block', source: '45.142.120.0/24', note: 'kept scanning SSH', added: now() - 4 * 86400 },
        { id: 'r2', action: 'limit', port: '22', proto: 'tcp', rate: '6/min', note: 'no password guessing', added: now() - 2 * 86400 },
        { id: 'r3', action: 'allow', port: '32400', proto: 'tcp', note: 'Plex', added: now() - 86400 },
        { id: 'r4', action: 'block', direction: 'out', port: '25', proto: 'tcp', note: 'no mail from here', added: now() - 3 * 86400 },
      ],
      listening: [
        { port: 22, proto: 'tcp', program: 'sshd' },
        { port: 80, proto: 'tcp', program: 'nginx' },
        { port: 443, proto: 'tcp', program: 'nginx' },
        { port: 8080, proto: 'tcp', program: 'nightjar', local: true },
        { port: 32400, proto: 'tcp', program: 'Plex Media Server' },
        { port: 51413, proto: 'udp', program: 'qbittorrent-nox' },
      ],
      hits: { r1: { packets: 1204, bytes: 72240 }, r2: { packets: 38, bytes: 2280 }, r3: { packets: 91822, bytes: 7_340_000 }, r4: { packets: 3, bytes: 180 } },
      since: now() - 6 * 3600,
      drops: [
        // A few hours of one network scanning SSH: enough drops to fill several pages.
        ...Array.from({ length: 34 }, (_, i) => ({ at: now() - 60 - i * 340, rule: 'r1', source: `45.142.120.${[11, 37, 11, 52, 37, 11, 90][i % 7]}`, dest: '192.168.1.10', proto: 'tcp', port: 22 })),
        { at: now() - 900, rule: 'r4', direction: 'out' as const, source: '192.168.1.10', dest: '192.0.2.25', proto: 'tcp', port: 25 },
        { at: now() - 1300, rule: 'r2', source: '203.0.113.50', dest: '192.168.1.10', proto: 'tcp', port: 22 },
      ].sort((a, b) => b.at - a.at),
      native: `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), deny (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
80,443/tcp                 ALLOW IN    Anywhere
32400/tcp                  ALLOW IN    Anywhere
51413/udp                  ALLOW IN    Anywhere`,
    };
  return {
    full: true,
    tool: 'iptables',
    policy: 'ACCEPT',
    default: '',
    open: [ssh, { port: 9101, what: 'this agent' }],
    rules: [],
    hits: {},
    drops: [],
    native: '-P INPUT ACCEPT\n-P FORWARD ACCEPT\n-P OUTPUT ACCEPT',
    listening:
      id === 'b2c3d4e5f6a1'
        ? [ports[0], { port: 53, proto: 'tcp', program: 'pihole-FTL' }, { port: 53, proto: 'udp', program: 'pihole-FTL' }, { port: 80, proto: 'tcp', program: 'lighttpd' }, ports[2]]
        : ports,
  };
}

// demoAllowed marks the ports anyone may reach whatever the default, as the firewall does.
function demoAllowed(fw: FirewallStatus) {
  const covers = (r: FirewallRule, port: number) =>
    !r.port ||
    r.port.split(',').some((p) => {
      const [a, b = a] = p.split(':').map(Number);
      return port >= a && port <= b;
    });
  for (const p of fw.listening) {
    p.allowed = (p.proto === 'tcp' && !!fw.open?.some((o) => o.port === p.port)) || undefined;
    if (p.allowed) continue;
    const first = fw.rules.find((r) => !r.paused && r.direction !== 'out' && !r.source && (!r.proto || r.proto === p.proto) && covers(r, p.port) && r.action !== 'limit');
    p.allowed = first?.action === 'allow' || undefined;
  }
}

// demoDefense is this machine's defense: on, and ready. The switch and unblocking change it.
const demoDefense: AttackDefense = { on: true, active: false, measures: [], blocked: [], web: true };

// demoAttacks is the attack watch: this machine calm, with torrents explaining their UDP and a SYN
// flood from yesterday; pi-dns under a SYN and UDP flood right now, and defending.
function demoAttacks(under: boolean): AttackView {
  const past: Attack = {
    id: 'demo1',
    start: now() - 26 * 3600,
    end: now() - 26 * 3600 + 252,
    methods: [
      { method: 'syn_flood', value: 9200, threshold: 100, normal: 0.4 },
      { method: 'udp_flood', value: 480000, threshold: 20000, normal: 1800 },
    ],
    kinds: [
      {
        id: 'udp_reflect_53',
        name: 'DNS amplification',
        detail: 'from port 53',
        share: 0.71,
        how: 'Open DNS resolvers answering spoofed queries, often for large records, in this server\u2019s name.',
      },
      { id: 'tcp_SYN', name: 'SYN flood', share: 0.24, how: 'Connection requests that are never completed, to fill the queue of half-open connections.' },
    ],
    peak_rx_bits: 812e6,
    peak_rx_packets: 640000,
    sources: [
      { address: '45.142.120.11', established: 0, syn_recv: 940, requests: 0 },
      { address: '45.142.120.37', established: 0, syn_recv: 610, requests: 0 },
      { address: '172.68.10.21', established: 38, syn_recv: 0, requests: 0, network: 'Cloudflare' },
    ],
  };
  if (under) {
    const current: Attack = {
      id: 'demo2',
      start: now() - 95,
      methods: [
        { method: 'syn_flood', value: 6100, threshold: 100, normal: 0.2 },
        { method: 'udp_flood', value: 210000, threshold: 20000, normal: 600 },
      ],
      kinds: [
        { id: 'udp_reflect_123', name: 'NTP amplification', detail: 'from port 123', share: 0.64, how: 'NTP servers answering spoofed monlist requests, each reply hundreds of times the request.' },
        { id: 'tcp_ACK', name: 'ACK flood', share: 0.29, how: 'Packets acknowledging connections that do not exist, to tire out firewalls and the kernel looking them up.' },
      ],
      peak_rx_bits: 610e6,
      peak_rx_packets: 410000,
      sources: [
        { address: '185.220.101.4', established: 0, syn_recv: 720, requests: 0 },
        { address: '185.220.101.9', established: 0, syn_recv: 540, requests: 0 },
        { address: '91.92.240.17', established: 210, syn_recv: 0, requests: 0 },
      ],
    };
    const defense: AttackDefense = {
      on: true,
      active: true,
      web: false,
      measures: [
        { id: 'syn_limit', name: 'SYN limit per address', how: 'New connections from one address beyond 50 a second are dropped. SYN cookies answer the rest without setting memory aside for each.' },
        {
          id: 'stray_tcp',
          name: 'Drop TCP outside connections',
          how: 'TCP packets that neither start a connection nor belong to one, such as a flood of ACKs or of replies to connections never made, are dropped before anything handles them.',
        },
        { id: 'reflection', name: 'Drop unasked-for replies', how: 'Replies this machine never asked for (UDP from port 123) are dropped; answers to its own requests still get through.' },
        { id: 'udp_limit', name: 'UDP limit per address', how: 'UDP from one address beyond 10000 packets a second is dropped.' },
        { id: 'udp_closed', name: 'Drop UDP to closed ports', how: 'UDP sent to ports nothing listens on is dropped quietly, so the server stops answering each packet with an error.' },
      ],
      blocked: [
        { address: '185.220.101.4', until: now() + 14 * 60, why: '720 connections', where: 'firewall' },
        { address: '185.220.101.9', until: now() + 13 * 60, why: '540 connections', where: 'firewall' },
      ],
    };
    return { attacking: true, current, findings: current.methods, busy: {}, rx_bits: 600e6, rx_packets: 400000, learning: false, history: [], defense };
  }
  return {
    attacking: false,
    findings: [{ method: 'udp_flood', value: 42000, threshold: 20000, normal: 1800, expected: 'qbittorrent-nox is transferring (uTP is UDP)' }],
    busy: { torrents: 'qbittorrent-nox' },
    rx_bits: 96e6,
    rx_packets: 42000,
    learning: false,
    history: [past],
    defense: { ...demoDefense, blocked: [...demoDefense.blocked] },
  };
}

// SSH logins and keys: nas has one set up already; a new key made here is a made-up one.
const demoKeys: SSHKey[] = [
  {
    path: '/root/serverMonitor/ssh/nightjar',
    name: 'nightjar',
    type: 'ED25519',
    fingerprint: 'SHA256:q3Vb7m0yZr1kQe9tJ4cL2wXo8pN6aFhD5sGuIvTzB0E',
    comment: 'nightjar@demo-server',
    public: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk3m9Zq2T0bWc7yPj1sVxLr4dNf8aGuEo6iKt5hBzQe nightjar@demo-server',
    own: true,
  },
  {
    path: '/root/.ssh/id_ed25519',
    name: 'id_ed25519',
    type: 'ED25519',
    fingerprint: 'SHA256:Hd8kP2xVt6Lq0nZr4bYc9mWs1eJg7uAo3iFz5RNvKQw',
    comment: 'root@demo-server',
    public: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGv2Lp8sXn1Qd4cWm7zRb0kTy6hJf3aEu9oNi5BtKrVx root@demo-server',
    own: false,
  },
];
const demoSSH: Record<string, HostSSH> = { a1b2c3d4e5f6: { port: 22, user: 'root', key: '/root/serverMonitor/ssh/nightjar' } };

// demoChecks are how each saved login last went.
const demoChecks: Record<string, SSHCheck> = {
  a1b2c3d4e5f6: { ok: true, at: now() - 3 * 3600, as: 'root@nas', host_key: 'ED25519 SHA256:q7Lw2vN8fKc3RtY1mZpX4hJ9sB6dE0aGuTi5oVnWkQs' },
  f6a1b2c3d4e5: { ok: true, at: now() - 20 * 60, as: 'root@vps', host_key: 'ED25519 SHA256:Hn3pR8sW1vKx6cYq0zT4mB9fL2jD7gEaUo5iNrXtZe' },
};

function withSSH(h: RemoteHost): RemoteHost {
  const ssh = demoSSH[h.id];
  if (!ssh) return h;
  const target = ssh.host || (h.kind === 'ssh' ? h.address : new URL(h.address).hostname);
  return { ...h, ssh, ssh_command: `ssh -i ${ssh.key}${ssh.port !== 22 ? ` -p ${ssh.port}` : ''} ${ssh.user}@${target}`, ssh_check: demoChecks[h.id] };
}

// noteDemoLogin keeps how a demo login went, as the monitor does.
function noteDemoLogin(id: string, t: { ok: boolean; output?: string; error?: string }) {
  const before = demoChecks[id];
  const key = before?.host_key ?? `ED25519 SHA256:demo${id}hostkey0000000000000000000000`;
  demoChecks[id] = t.ok ? { ok: true, at: now(), as: t.output?.split('\n')[0] || before?.as, host_key: key } : { ok: false, at: now(), error: t.error, host_key: before?.host_key };
  return t;
}

// demoLoginTest is the demo's first login with a saved login: backup-box is offline, so it fails.
function demoLoginTest(id: string) {
  if (demoHosts.find((h) => h.id === id)?.offlineSince) return { ok: false, error: 'the server did not answer SSH in time' };
  const name = demoHosts.find((h) => h.id === id)?.name ?? demoSSHServers.find((h) => h.id === id)?.name ?? 'server';
  return { ok: true, output: `${demoSSH[id]?.user ?? 'root'}@${name}\n up 15 days,  2:41,  0 users,  load average: 0.21, 0.18, 0.12` };
}

// A VPS watched through its SSH login alone.
const demoSSHServers: { id: string; name: string; address: string }[] = [{ id: 'f6a1b2c3d4e5', name: 'vps', address: 'vps.example.net' }];
demoSSH.f6a1b2c3d4e5 = { host: 'vps.example.net', port: 22, user: 'root', key: '/root/serverMonitor/ssh/nightjar' };

function demoSSHServer(i: number): RemoteHost {
  const d = demoSSHServers[i];
  return { id: d.id, name: d.name, address: d.address, added: now() - 86400 * 2, kind: 'ssh', status: 'online', last_seen: now(), readings: null, history: [], problems: [] };
}

const allDemoHosts = (points: number) => [...demoHosts.map((_, i) => withSSH(demoHost(i, points))), ...demoSSHServers.map((_, i) => withSSH(demoSSHServer(i)))];

// ---------- analytics ----------

const RANGE_SPECS: Record<AnalyticsRangeName, [number, number, string]> = {
  '1h': [3600, 60, '1 hour'],
  '24h': [86400, 300, '24 hours'],
  '7d': [7 * 86400, 1800, '7 days'],
  '30d': [30 * 86400, 7200, '30 days'],
};

// Each demo server's character over time: a daily rhythm, a disk that fills or not, and outages.
const demoCharacters: Record<string, { cpu: number; mem: number; temp?: number; disk?: [number, number]; rx: number; tx: number; latency?: number; down?: [number, number][]; age?: number }> = {
  local: { cpu: 22, mem: 51, temp: 60, disk: [66.8, 0.08], rx: 1.4e6, tx: 2.6e6 },
  a1b2c3d4e5f6: { cpu: 9, mem: 38, temp: 47, disk: [91.2, 0.2], rx: 4e6, tx: 1.1e6, down: [[3 * 86400 + 4000, 3 * 86400 + 4600]] },
  b2c3d4e5f6a1: { cpu: 38, mem: 62, temp: 77, disk: [38, 0], rx: 9e4, tx: 4e4 },
  c3d4e5f6a1b2: {
    cpu: 3,
    mem: 21,
    temp: 39,
    disk: [70.5, 0.03],
    rx: 8e4,
    tx: 6e4,
    down: [
      [0, 1200],
      [9 * 86400, 9 * 86400 + 5400],
    ],
  },
};

function demoHostAnalytics(id: string, name: string, kind: HostAnalytics['kind'], status: string, range: AnalyticsRangeName): HostAnalytics {
  const [span, step] = RANGE_SPECS[range];
  const c = demoCharacters[id] ?? { cpu: 10, mem: 30, rx: 1e4, tx: 1e4 };
  const end = Math.floor(now() / step) * step;
  const seed = id.charCodeAt(0);
  const daily = (t: number) => Math.sin(((t % 86400) / 86400) * 2 * Math.PI - 1.2);
  // Seconds ago that were down: down windows are [from, to] seconds before now.
  const downAt = (ago: number) => (c.down ?? []).some(([a, b]) => ago >= a && ago < b);
  const points: AnalyticsPoint[] = [];
  for (let t = end - span + step; t <= end; t += step) {
    const ago = now() - t;
    if (c.age !== undefined && ago > c.age) continue;
    const down = downAt(ago);
    const cpu = c.latency !== undefined || down ? null : round(clamp(c.cpu * (1 + 0.45 * daily(t)) + c.cpu * 0.3 * (noise(Math.floor(t / step), seed) - 0.5), 0.5, 100));
    const temp = c.temp === undefined || down || cpu === null ? null : round(c.temp + (cpu - c.cpu) * 0.25 + 2 * (noise(Math.floor(t / step), seed + 1) - 0.5));
    const mem = c.latency !== undefined || down ? null : round(c.mem + 3 * daily(t + 7200));
    const latency = c.latency === undefined || down ? null : round(Math.max(0.4, c.latency * (1 + 0.25 * daily(t) + 0.3 * (noise(t, seed) - 0.5))), 1);
    points.push({
      t,
      up: down ? 0 : 1,
      cpu,
      cpu_max: cpu === null ? null : round(Math.min(100, cpu * (1.6 + noise(t, seed)))),
      mem,
      mem_max: mem === null ? null : round(Math.min(100, mem + 2 + 3 * noise(t, seed + 3))),
      temp,
      temp_max: temp === null ? null : round(temp + 3 + 4 * noise(t, seed + 2)),
      disk: c.disk && !down ? round(c.disk[0] - (c.disk[1] * ago) / 86400, 2) : null,
      rx: c.latency !== undefined || down ? null : Math.max(0, c.rx * (1 + 0.6 * daily(t + 3600))),
      tx: c.latency !== undefined || down ? null : Math.max(0, c.tx * (1 + 0.6 * daily(t + 5400))),
      latency,
      latency_max: latency === null ? null : round(latency * (1.5 + noise(t, seed + 4)), 1),
      minutes: step / 60,
    });
  }
  const minutes = points.reduce((n, p) => n + p.minutes, 0);
  const avg = (pick: (p: AnalyticsPoint) => number | null) => {
    const v = points.map(pick).filter((x): x is number => x !== null);
    return v.length ? v.reduce((a, b) => a + b, 0) / v.length : null;
  };
  const peak = (pick: (p: AnalyticsPoint) => number | null) => {
    const v = points.map(pick).filter((x): x is number => x !== null);
    return v.length ? Math.max(...v) : null;
  };
  const incidents = (c.down ?? []).filter(([a]) => a < span).map(([a, b]) => ({ start: now() - b, end: now() - a, ongoing: a === 0 && status === 'offline' }));
  const downtime = incidents.reduce((n, i) => n + (i.end - i.start), 0);
  const disk = c.disk ? c.disk[0] : null;
  const fullDays = c.disk && c.disk[1] > 0.01 && span >= 86400 ? (100 - c.disk[0]) / c.disk[1] : null;
  return {
    id,
    name,
    kind,
    status,
    points,
    incidents,
    summary: {
      coverage: Math.min(1, (minutes * 60) / span),
      uptime: kind === 'local' ? null : 100 * (1 - downtime / Math.min(span, c.age ?? span)),
      downtime_s: kind === 'local' ? 0 : downtime,
      incidents: incidents.length,
      cpu_avg: avg((p) => p.cpu),
      cpu_peak: peak((p) => p.cpu_max),
      mem_avg: avg((p) => p.mem),
      mem_peak: peak((p) => p.mem),
      temp_avg: avg((p) => p.temp),
      temp_peak: peak((p) => p.temp_max),
      disk_now: disk,
      disk_change: c.disk ? (c.disk[1] * span) / 86400 : null,
      disk_full_days: fullDays !== null && fullDays < 3650 ? fullDays : null,
      rx_bytes: points.reduce((n, p) => n + (p.rx ?? 0) * p.minutes * 60, 0),
      tx_bytes: points.reduce((n, p) => n + (p.tx ?? 0) * p.minutes * 60, 0),
      latency_avg: avg((p) => p.latency),
      latency_peak: peak((p) => p.latency),
    },
  };
}

function demoAnalytics(range: AnalyticsRangeName): HostAnalytics[] {
  return [
    demoHostAnalytics('local', 'demo-server', 'local', 'online', range),
    ...allDemoHosts(1)
      .filter((h) => h.kind === 'agent')
      .map((h) => demoHostAnalytics(h.id, h.name, 'agent', h.status, range)),
  ];
}

function route(method: string, url: URL, body: unknown): Response {
  const path = url.pathname;
  const plugin = pluginRoute(method, path, body, json, error);
  if (plugin) return plugin;
  const checkName = path.startsWith('/api/checks/') ? decodeURIComponent(path.slice('/api/checks/'.length)) : null;
  const findCheck = (name: string) => state.checks.findIndex((c) => c.name === name);

  const hostID = path.match(/^\/api\/hosts\/([0-9a-f]+)$/)?.[1];
  const hostIndex = hostID ? demoHosts.findIndex((h) => h.id === hostID) : -1;
  const sshIndex = hostID ? demoSSHServers.findIndex((h) => h.id === hostID) : -1;
  const analyticsRange = (url.searchParams.get('range') ?? '24h') as AnalyticsRangeName;
  if (method === 'GET' && path === '/api/analytics') {
    const [, step, label] = RANGE_SPECS[analyticsRange] ?? RANGE_SPECS['24h'];
    return json({
      range: { name: analyticsRange, label },
      ranges: Object.entries(RANGE_SPECS).map(([name, [, , label]]) => ({ name, label })),
      step,
      hosts: demoAnalytics(analyticsRange),
      kept: true,
    });
  }
  const analyticsID = path.match(/^\/api\/analytics\/([0-9a-z]+)$/)?.[1];
  if (method === 'GET' && analyticsID) {
    const host = demoAnalytics(analyticsRange).find((h) => h.id === analyticsID);
    return host ? json({ host, step: RANGE_SPECS[analyticsRange][1] }) : error(404, 'No such server');
  }
  if (method === 'GET' && path === '/api/hosts')
    return json({
      hosts: allDemoHosts(60),
      port: 9101,
      agents: [
        { arch: 'amd64', file: 'nightjar-agent-linux-amd64', size: 7_759_032 },
        { arch: 'arm64', file: 'nightjar-agent-linux-arm64', size: 7_209_112 },
      ],
    });
  if (method === 'GET' && hostID && sshIndex >= 0) return json({ host: withSSH(demoSSHServer(sshIndex)) });
  if (method === 'GET' && hostID) return hostIndex < 0 ? error(404, 'no such server') : json({ host: withSSH(demoHost(hostIndex, 360)) });
  if (method === 'GET' && path === '/api/ssh/keys') return json({ keys: demoKeys });
  if (method === 'POST' && path === '/api/ssh/keys') {
    const { name: given, private: privateKey } = body as { name?: string; private?: string };
    const name = (given || 'nightjar').trim();
    if (privateKey !== undefined && !/^-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(privateKey.trim()))
      return error(400, 'paste the whole private key, from -----BEGIN to -----END; it is the file without .pub, such as ~/.ssh/id_ed25519');
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/.test(name)) return error(400, 'name the key with letters, numbers, dots, dashes or underscores');
    if (demoKeys.some((k) => k.own && k.name === name)) return error(400, `there is already a key called ${name}`);
    const key: SSHKey = { ...demoKeys[0], path: `/root/serverMonitor/ssh/${name}`, name, fingerprint: `SHA256:demo${name}${demoKeys.length}` };
    demoKeys.unshift(key);
    return json({ key });
  }
  // Firewalls: this machine's, and each agent server's through its agent.
  const fwMatch =
    path === '/api/firewall' || path.startsWith('/api/firewall/') ? ['', 'local', path.slice('/api'.length)] : path.match(/^\/api\/hosts\/([0-9a-f]+)(\/(?:firewall.*|defense|blocked\/.+))$/);
  if (fwMatch) {
    const [, id, rest] = fwMatch;
    const local = id === 'local';
    const host = demoHosts.find((h) => h.id === id);
    if (!local && !host) return error(404, 'no such server');
    if (host?.offlineSince) return error(422, `no answer from ${host.name}: timed out`);
    const fw = (demoFirewalls[id] ??= demoFirewall(id));
    // A change of the default not kept in time goes back.
    if (fw.pending && fw.pending.until < now()) {
      fw.default = fw.default === 'drop' ? '' : 'drop';
      fw.pending = undefined;
    }
    const answer = () => {
      demoAllowed(fw);
      return local ? json({ enabled: true, firewall: fw }) : json({ firewall: fw, defense: demoAttacks(host?.name === 'pi-dns').defense });
    };
    if (method === 'GET' && rest === '/firewall') return answer();
    // check refuses a rule the way the firewall does, else puts it in one form.
    const check = (b: Partial<FirewallRule>): FirewallRule | Response => {
      const out = b.direction === 'out';
      if (b.action === 'limit' && out) return error(422, 'limits are for incoming traffic; block or allow outgoing traffic instead');
      if (b.action === 'limit' && !b.port) return error(422, 'give the port of the service to limit');
      if (!b.source && !b.port)
        return error(
          422,
          out
            ? 'a rule for all outgoing traffic would cut this machine off, or change nothing; give an address or a port'
            : b.action === 'allow'
              ? 'allowing everyone to every port would open the whole machine; give an address or a port'
              : 'blocking everyone from every port would cut this machine off; give an address or a port',
        );
      if (b.source && !/^[0-9a-f.:/]+$/i.test(b.source)) return error(422, `"${b.source}" is not an address or a network like 203.0.113.0/24`);
      if (b.port && !/^[0-9 ,:-]+$/.test(b.port)) return error(422, `"${b.port}" is not a port from 1 to 65535, a range like 8000-8100, or a list like 80,443`);
      return {
        id: '',
        action: b.action === 'allow' || b.action === 'limit' ? b.action : 'block',
        direction: out ? 'out' : undefined,
        source: b.source || undefined,
        port: b.port?.replaceAll(' ', '').replaceAll('-', ':') || undefined,
        proto: b.proto,
        rate: b.action === 'limit' ? b.rate : undefined,
        note: b.note || undefined,
        paused: b.paused || undefined,
        added: now(),
      };
    };
    if (method === 'POST' && rest === '/firewall/rules') {
      const r = check(body as Partial<FirewallRule>);
      if (r instanceof Response) return r;
      r.id = `r${fw.rules.length}${Math.round(now())}`;
      // A block or limit goes before the allows; an allow last.
      const at = r.action === 'allow' ? -1 : fw.rules.findIndex((o) => o.action === 'allow' && (o.direction === 'out') === (r.direction === 'out'));
      if (at < 0) fw.rules.push(r);
      else fw.rules.splice(at, 0, r);
      return answer();
    }
    const rm = rest.match(/^\/firewall\/rules\/(.+)$/);
    if (method === 'PUT' && rm) {
      const i = fw.rules.findIndex((r) => r.id === decodeURIComponent(rm[1]));
      if (i < 0) return error(404, 'no such rule');
      const r = check(body as Partial<FirewallRule>);
      if (r instanceof Response) return r;
      fw.rules[i] = { ...r, id: fw.rules[i].id, added: fw.rules[i].added };
      return answer();
    }
    if (method === 'DELETE' && rm) {
      if (!fw.rules.some((r) => r.id === decodeURIComponent(rm[1]))) return error(404, 'no such rule');
      fw.rules = fw.rules.filter((r) => r.id !== decodeURIComponent(rm[1]));
      return answer();
    }
    if (method === 'PUT' && rest === '/firewall/order') {
      const ids = (body as { ids?: string[] }).ids ?? [];
      if (ids.length !== fw.rules.length || !fw.rules.every((r) => ids.includes(r.id))) return error(422, 'the order must name every rule once; reload and try again');
      fw.rules = ids.map((i) => fw.rules.find((r) => r.id === i)!);
      return answer();
    }
    if (method === 'PUT' && rest === '/firewall/default') {
      const def = (body as { default?: string }).default === 'drop' ? 'drop' : '';
      if (def !== (fw.default ?? '')) {
        fw.default = def;
        fw.pending = { default: def, until: now() + 60 };
      }
      return answer();
    }
    if (method === 'POST' && rest === '/firewall/default/keep') {
      if (!fw.pending) return error(422, 'there is no change waiting; it may have gone back already');
      if (!(body as { keep?: boolean }).keep) fw.default = fw.default === 'drop' ? '' : 'drop';
      fw.pending = undefined;
      return answer();
    }
    if (method === 'PUT' && rest === '/defense') return answer();
    if (method === 'DELETE' && rest.startsWith('/blocked/')) return answer();
  }
  if (method === 'PUT' && path === '/api/attacks/defense') {
    demoDefense.on = !!(body as { on?: boolean }).on;
    return json({ enabled: true, attacks: demoAttacks(false) });
  }
  const unblockMatch = path.match(/^\/api\/attacks\/blocked\/(.+)$/);
  if (method === 'DELETE' && unblockMatch) {
    const address = decodeURIComponent(unblockMatch[1]);
    if (!demoDefense.blocked.some((b) => b.address === address)) return error(404, 'That address is not blocked');
    demoDefense.blocked = demoDefense.blocked.filter((b) => b.address !== address);
    return json({ enabled: true, attacks: demoAttacks(false) });
  }
  if (method === 'POST' && path === '/api/ssh/authorize') {
    const b = body as { password?: string };
    if (!b.password) return error(400, 'enter the password for that user on the server');
    // The demo has no network; a password of "wrong" shows how a refusal reads.
    if (b.password === 'wrong') return error(422, 'the server turned the password down, or does not take passwords; add the key’s public line yourself instead');
    return json({ ok: true, output: '' });
  }
  if (method === 'POST' && path === '/api/ssh/trust') return json({ ok: true });
  const sshMatch = path.match(/^\/api\/hosts\/([0-9a-f]+)\/ssh(?:\/([a-z-]+))?$/);
  if (sshMatch) {
    const [, id, action] = sshMatch;
    const i = demoHosts.findIndex((h) => h.id === id);
    const v = demoSSHServers.findIndex((h) => h.id === id);
    if (i < 0 && v < 0) return error(404, 'no such server');
    const host = () => withSSH(i >= 0 ? demoHost(i, 360) : demoSSHServer(v));
    if (!action && method === 'PUT') {
      const b = body as HostSSH;
      if (!demoKeys.some((k) => k.path === b.key)) return error(400, 'choose one of the keys Nightjar found, or make a new one');
      if (v >= 0 && !b.host?.trim()) return error(400, "enter the server's address");
      demoSSH[id] = { host: b.host?.trim() || undefined, port: b.port || 22, user: b.user?.trim() || 'root', key: b.key };
      if (v >= 0) demoSSHServers[v].address = b.host!.trim();
      delete demoChecks[id];
      const test = noteDemoLogin(id, demoLoginTest(id));
      return json({ host: host(), test });
    }
    if (!action && method === 'DELETE') {
      if (v >= 0) return error(400, 'this server is watched through its SSH login; remove the server instead');
      delete demoSSH[id];
      delete demoChecks[id];
      return json({ host: host() });
    }
    if (action && method === 'POST') {
      if (!demoSSH[id]) return error(400, 'set up an SSH login for this server first');
      // The demo has no network; backup-box is the one that is offline, so it does not answer.
      if (i >= 0 && demoHosts[i].offlineSince) {
        if (action === 'test') noteDemoLogin(id, { ok: false, error: 'the server did not answer SSH in time' });
        return error(422, 'the server did not answer SSH in time');
      }
      const who = `${demoSSH[id].user}@${i >= 0 ? demoHosts[i].name : demoSSHServers[v].name}`;
      if (['test', 'restart-agent', 'reinstall-agent', 'reboot'].includes(action) && (action === 'test' || i >= 0 || action === 'reboot'))
        noteDemoLogin(id, { ok: true, output: action === 'test' ? who : undefined });
      if (action === 'test') return json({ output: `${who}\n ${new Date(now() * 1000).toTimeString().slice(0, 8)} up 15 days,  2:41,  0 users,  load average: 0.21, 0.18, 0.12` });
      if (action === 'restart-agent') return i >= 0 ? json({ output: 'nightjar-agent restarted' }) : error(400, 'this server has no agent');
      if (action === 'reinstall-agent')
        return i >= 0
          ? json({ output: "Reinstalling with this machine's agent settings.\nThe agent is running as nightjar-agent.service, serving HTTPS on :9101.\nIt updates itself to the dashboard's version." })
          : error(400, 'this server has no agent');
      if (action === 'reboot') return json({ output: 'rebooting' });
      return error(400, `unknown SSH action "${action}"`);
    }
  }
  if (method === 'POST' && path === '/api/hosts/token') return json({ token: Array.from({ length: 48 }, (_, i) => '0123456789abcdef'[Math.floor(noise(i, now()) * 16)]).join(''), port: 9101 });
  if (method === 'POST' && path === '/api/hosts') {
    const b = body as { kind?: string; name?: string; ssh?: HostSSH };
    if (b.kind !== 'ssh' || !b.ssh) return error(400, 'no answer from the agent: the demo has no network; on a real dashboard this connects to it');
    if (!b.ssh.host?.trim()) return error(400, "enter the server's address");
    if (!demoKeys.some((k) => k.path === b.ssh!.key)) return error(400, 'choose one of the keys Nightjar found, or make a new one');
    const id = Array.from({ length: 12 }, (_, i) => '0123456789abcdef'[Math.floor(noise(i, now()) * 16)]).join('');
    const address = b.ssh.host.trim();
    demoSSHServers.push({ id, name: b.name?.trim() || address, address });
    demoSSH[id] = { host: address, port: b.ssh.port || 22, user: b.ssh.user?.trim() || 'root', key: b.ssh.key };
    const test = noteDemoLogin(id, demoLoginTest(id));
    return json({ host: withSSH(demoSSHServer(demoSSHServers.length - 1)), test });
  }
  if (method === 'PUT' && hostID && sshIndex >= 0) {
    const b = body as { name?: string };
    if (b.name?.trim()) demoSSHServers[sshIndex].name = b.name.trim();
    return json({ host: withSSH(demoSSHServer(sshIndex)) });
  }
  if (method === 'PUT' && hostID) {
    if (hostIndex < 0) return error(404, 'no such server');
    const b = body as { name?: string };
    if (b.name?.trim()) demoHosts[hostIndex].name = b.name.trim();
    return json({ host: demoHost(hostIndex, 360) });
  }
  if (method === 'DELETE' && hostID) {
    if (hostIndex >= 0) demoHosts.splice(hostIndex, 1);
    if (sshIndex >= 0) demoSSHServers.splice(sshIndex, 1);
    // Checks that belonged to it stay, belonging to none.
    for (const c of state.checks) if (c.server === hostID) delete c.server;
    return new Response(null, { status: 204 });
  }

  if (method === 'GET') {
    switch (path) {
      case '/api/config':
        return json({
          auth_required: true,
          authorized: !demoSignedOut,
          account: { configured: true, username: 'demo', service: 'nightjar', two_factor: demoTwoFactor().on, recovery_left: demoTwoFactor().left },
          hidden: (state.settings.DASHBOARD_HIDDEN ?? '').split(',').filter(Boolean),
          appearance: demoAppearance(),
          status_page_enabled: true,
          status_page_title: 'Demo status',
          status_page_domain: state.settings.STATUS_PAGE_DOMAIN ?? '',
          status_page_path: state.settings.STATUS_PAGE_PATH || '/status',
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
        return json({ enabled: true, self: 'nightjar.service', units: demoUnits });
      case '/api/attacks':
        return json({ enabled: true, attacks: demoAttacks(false) });
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
        return json({ ...logs(url), findings: demoFindings.log });
      case '/api/notifications':
        return json(notifications(url));
      case '/api/notifications/summary':
        return json({ counts: notifyCounts(), mutes: liveMutes(), quiet: false, held: 0 });
      case '/api/notifications/rules':
        return json(notifyRules());
      case '/api/appearance':
        return json({ appearance: demoAppearance() });
      case '/api/incidents': {
        const days = Math.min(90, Math.max(1, Number(url.searchParams.get('days') ?? 14)));
        return json({ incidents: demoIncidents(days), days });
      }
      case '/api/repairs':
        return json(demoRepairs(url.searchParams.get('check') ?? ''));
      case '/api/maintenance':
        return json(demoMaintenance());
      case '/api/cleanup':
        return json(demoCleanup());
      case '/api/activity':
        return json({ activity: url.searchParams.get('before') ? [] : demoActivity() });
      case '/api/backup':
        return json({
          nightjar: 'demo',
          made: Date.now() / 1000,
          secrets: url.searchParams.get('secrets') === '1',
          checks: state.checks,
          settings: state.settings,
          meta: { 'dashboard.appearance': demoAppearance(), 'statuspage.design': designOrDefault() },
        });
      case '/api/plex/stats':
        return json({ enabled: true, stats: demoPlexStats(Math.min(90, Math.max(1, Number(url.searchParams.get('days') ?? 7)))) });
      case '/api/docker/containers':
        return json({ available: true, containers: demoContainers });
      case '/api/report':
        return json({ report: demoReport(state.checks.map(summary)), enabled: true, at: 'Mon 09:00', next: nextMonday() });
      case '/api/statuspage/proxy':
        return json(demoProxy());
      case '/api/statuspage':
        return json({
          enabled: true,
          designed: !!state.statusDesign,
          design: designOrDefault(),
          checks: state.checks.map((c) => c.name),
          days: Object.fromEntries(state.checks.map((c) => [c.name, demoDays(c.name)])),
        });
    }
  }
  const incidentPart = path.match(/^\/api\/incidents\/(\d+)\/(feedback|notice)$/);
  if (incidentPart && (method === 'PUT' || method === 'DELETE')) {
    const id = Number(incidentPart[1]);
    const out = incidentPart[2] === 'feedback' ? demoFeedback(id, method === 'PUT' ? (body as object) : null) : demoNotice(id, method === 'PUT' ? (body as object) : null);
    if (out === undefined) return error(404, 'no incident with that id in the last 90 days');
    return method === 'DELETE' ? new Response(null, { status: 204 }) : json({ [incidentPart[2]]: out });
  }
  const windowID = path.match(/^\/api\/maintenance(?:\/([0-9a-z]+))?$/);
  if (windowID && (method === 'POST' || method === 'PUT')) {
    const saved = demoSaveWindow({ ...(body as MaintenanceWindow), id: windowID[1] ?? '' });
    return typeof saved === 'string' ? error(400, saved) : json({ window: saved }, method === 'POST' ? 201 : 200);
  }
  if (windowID?.[1] && method === 'DELETE') return demoDeleteWindow(windowID[1]) ? new Response(null, { status: 204 }) : error(404, 'no maintenance window with that id');
  if (method === 'POST' && path === '/api/report/send') return json({ sent: true });
  if (method === 'POST' && path === '/api/cleanup/scan') return json(demoCleanupScan());
  if (method === 'POST' && path.startsWith('/api/auth/2fa/')) {
    const out = demoTwoFactorRoute(path, body as Record<string, string>);
    if (typeof out === 'string') return error(400, out);
    if (out === null) return new Response(null, { status: 204 });
    if (out !== undefined) return json(out);
  }
  if (method === 'POST' && path === '/api/backup/restore') {
    const b = body as { nightjar?: string; checks?: unknown[]; settings?: Record<string, string> };
    if (!b?.nightjar) return error(400, 'That is not a Nightjar backup');
    return json({ restored: { checks: b.checks?.length ?? 0, settings: Object.keys(b.settings ?? {}).length, parts: 2 }, restarting: false });
  }
  const keepFor = path.match(/^\/api\/cleanup\/([^/]+)\/keep$/);
  if (keepFor && (method === 'PUT' || method === 'DELETE')) {
    const v = demoCleanupKeep(decodeURIComponent(keepFor[1]), method === 'PUT');
    return v ? json(v) : error(404, 'that is not on the cleanup list');
  }
  const repairFor = path.match(/^\/api\/checks\/([^/]+)\/repair$/);
  if (method === 'POST' && repairFor) {
    const spec = state.checks.find((c) => c.name === decodeURIComponent(repairFor[1]));
    if (!spec) return error(404, 'no check with that name');
    if (!spec.repair) return error(400, 'The check has no repair');
    return json({ repair: demoRepairNow(spec) });
  }
  if (method === 'POST' && path === '/api/checks/test') {
    const spec = normalise(body as Partial<CheckSpec>);
    if (typeof spec === 'string') return error(400, spec);
    // Targets on .invalid or .test hosts never answer, so the demo can show a failed test.
    const offline = /\.(invalid|test)\b|offline/.test(spec.target);
    const ms = 20 + Math.round(noise(spec.target.length, 7) * 180);
    if (spec.type === 'plugin') return json({ result: demoPluginCheck(spec.target) });
    if (spec.type === 'heartbeat') return json({ result: { up: false, latency_ms: null, detail: 'a heartbeat has nothing to try: save it, then ping its URL from the job' } });
    const detail = { http: 'HTTP 200', tcp: 'connected', ping: 'reply', tls: 'certificate valid for 61 days', docker: 'container healthy, started 3d ago' }[spec.type];
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
    if (method === 'GET' && what === 'logs') return demoUnits.some((u) => u.name === name) ? json({ lines: demoLogs[name] ?? [], findings: demoFindings[name] ?? [] }) : error(404, 'no such service');
    if (method === 'POST') {
      const u = demoUnitAction(name, what);
      return u ? json({ unit: u }) : error(404, 'no such service');
    }
  }
  if (method === 'PUT' && path === '/api/appearance') {
    const a = body as Appearance;
    if (a.css.toLowerCase().includes('</style')) return error(400, 'the CSS cannot contain </style');
    state.appearance = { ...a, css: a.css.trim() };
    save();
    return json({ appearance: state.appearance });
  }
  if ((method === 'POST' || method === 'DELETE') && path === '/api/statuspage/proxy') {
    const domain = state.settings.STATUS_PAGE_DOMAIN ?? '';
    if (!domain) return error(400, "set the status page's domain first");
    demoProxyOn = method === 'POST';
    const steps =
      method === 'POST'
        ? [
            { name: 'Write the site', ok: true, detail: `/etc/nginx/sites-available/nightjar-status-${domain}.conf` },
            { name: 'Enable the site', ok: true, detail: `/etc/nginx/sites-enabled/nightjar-status-${domain}.conf` },
            { name: "Check nginx's configuration", ok: true },
            { name: 'Reload nginx', ok: true },
            { name: 'Get an HTTPS certificate', ok: true, detail: 'certbot renews it on its own' },
          ]
        : [
            { name: 'Remove the site', ok: true, detail: `/etc/nginx/sites-available/nightjar-status-${domain}.conf` },
            { name: 'Reload nginx', ok: true },
          ];
    return json({ ok: true, steps, state: demoProxy().state });
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
  const stopStream = method === 'POST' && path.match(/^\/api\/plex\/streams\/([^/]+)\/stop$/);
  if (stopStream) {
    const id = decodeURIComponent(stopStream[1]);
    if (demoStopped.has(id) || !plex(true).plex?.streams.some((s) => s.id === id)) return error(404, 'that stream has already ended');
    demoStopped.set(id, now());
    return json(plex(false));
  }
  if (method === 'POST' && path === '/api/qbittorrent/action') {
    const { action, hashes } = body as { action: string; hashes: string[] };
    if (!hashes?.length) return error(422, 'no torrents given');
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

// The demo's admin account: sign in as demo, password demo. The sign-in screen says so in the demo.
export const DEMO_LOGIN = { username: 'demo', password: 'demo' };

// Signed in or out, as the demo has it: kept for the tab, so a reload stays the way it was. A
// signed-out demo answers only what the real monitor answers without a session.
const SIGNED_OUT_KEY = 'nightjar.demo.signedOut';
let demoSignedOut = (() => {
  try {
    return sessionStorage.getItem(SIGNED_OUT_KEY) === '1';
  } catch {
    return false;
  }
})();
function setDemoSignedOut(out: boolean) {
  demoSignedOut = out;
  try {
    if (out) sessionStorage.setItem(SIGNED_OUT_KEY, '1');
    else sessionStorage.removeItem(SIGNED_OUT_KEY);
  } catch {
    /* storage unavailable: it lasts until a reload */
  }
}
const PUBLIC = new Set(['/api/config', '/api/status', '/health']);

// demoSignIn checks the demo account, and with two-factor sign-in on, asks for a code as the
// monitor does (any six digits, or a recovery code, will do).
function demoSignIn(body: Record<string, string> | undefined): Response {
  if ((body?.username ?? '').trim() !== DEMO_LOGIN.username || body?.password !== DEMO_LOGIN.password) return error(401, 'wrong username or password');
  if (demoTwoFactor().on) {
    const code = (body?.code ?? '').trim();
    if (!code) return error(401, 'enter the code from your authenticator app', { code: 'needed' });
    if (!/^\d{6}$/.test(code) && code.length < 8) return error(401, 'that code is not right');
  }
  setDemoSignedOut(false);
  return json({ username: DEMO_LOGIN.username });
}

// demoFetch answers API requests from the demo data, after a short, realistic delay.
export async function demoFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, window.location.origin);
  const body = typeof init.body === 'string' ? JSON.parse(init.body) : undefined;
  const method = (init.method ?? 'GET').toUpperCase();
  await new Promise((r) => setTimeout(r, 60 + Math.random() * 120));
  if (method === 'POST' && url.pathname === '/api/auth/login') return demoSignIn(body);
  if (method === 'POST' && url.pathname === '/api/auth/logout') {
    setDemoSignedOut(true);
    return new Response(null, { status: 204 });
  }
  if (demoSignedOut && !PUBLIC.has(url.pathname)) return error(401, 'sign in first');
  return route(method, url, body);
}

// The status page's domain in nginx, in the demo: set up or not, in this tab only.
let demoProxyOn = false;
function demoProxy() {
  const domain = state.settings.STATUS_PAGE_DOMAIN ?? '';
  const site = `# Written by Nightjar for its public status page.\nserver {\n    listen 80;\n    server_name ${domain};\n\n    location / {\n        proxy_pass http://127.0.0.1:8080;\n        proxy_set_header Host $host;\n    }\n}\n`;
  return { domain, state: domain ? { nginx: true, certbot: true, installed: demoProxyOn, https: demoProxyOn, site } : undefined };
}
