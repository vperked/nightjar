import type {
  ActivityEntry,
  CheckSpec,
  CleanupItem,
  CleanupView,
  Container,
  Incident,
  IncidentCause,
  IncidentEvent,
  IncidentFeedback,
  IncidentNotice,
  MaintenanceResponse,
  MaintenanceWindow,
  Pings,
  PlexStats,
  RepairAttempt,
  RepairsResponse,
  WeeklyReport,
} from './types';

// The demo's incidents, repairs and heartbeat: one open incident, and four over the last ten days
// that show each kind of cause Nightjar finds. Times are relative to now.

const now = () => Date.now() / 1000;
const MIN = 60;
const HOUR = 3600;
const DAY = 86400;

let nextID = 9000;

type Ev = [offset: number, role: IncidentEvent['role'], severity: IncidentEvent['severity'], area: string, title: string, body?: string, subject?: string];

// readings draws the machine's minutes from half an hour before to ten minutes after, shaped by
// what happened: memory filling, the processor busy, or a disk near full.
function readings(start: number, end: number, shape: 'memory' | 'cpu' | 'disk' | 'calm') {
  const out: Incident['series'] = [];
  for (let t = Math.floor((start - 30 * MIN) / 60) * 60; t <= end + 10 * MIN; t += 60) {
    const x = (t - start) / 60;
    const wob = Math.sin(t / 97) * 3 + Math.sin(t / 31) * 2;
    let mem = 52 + wob;
    let cpu = 18 + Math.abs(Math.sin(t / 53)) * 14;
    let disk = 61;
    if (shape === 'memory') mem = x < -12 ? 58 + wob : x <= 1 ? Math.min(99.4, 70 + (x + 12) * 2.3) : 48 + wob;
    if (shape === 'cpu') cpu = x > -8 && x < (end - start) / 60 ? 88 + wob * 2 : cpu;
    if (shape === 'disk') disk = x < -20 ? 93 : x <= (end - start) / 60 ? 99.2 : 71;
    out.push({ t, mem: Math.round(mem * 10) / 10, cpu: Math.round(cpu * 10) / 10, disk });
  }
  return out;
}

function incident(
  started: number,
  length: number | null,
  title: string,
  cause: IncidentCause,
  also: IncidentCause[],
  affected: Incident['affected'],
  events: Ev[],
  shape: Parameters<typeof readings>[2],
): Incident {
  const evs = events.map(([off, role, severity, area, t, body, subject]) => ({ id: nextID++, ts: started + off, role, severity, area, title: t, body, subject }));
  const causeEvent = evs.find((e) => e.title === cause.text.split('|')[1]);
  const ended = length === null ? null : started + length;
  const series = readings(started, ended ?? now(), shape);
  const peak = (k: 'mem' | 'cpu' | 'disk') => Math.max(...series.filter((p) => p.t >= started - 600 && p.t <= (ended ?? now())).map((p) => p[k] ?? 0));
  return {
    id: evs.find((e) => e.role === 'problem')!.id,
    started,
    ended,
    severity: evs.some((e) => e.severity === 'critical') ? 'critical' : 'warning',
    title,
    cause: { ...cause, text: cause.text.split('|')[0], event: causeEvent?.id },
    also,
    affected,
    events: evs,
    series,
    peaks: { mem: peak('mem'), cpu: peak('cpu'), disk: peak('disk') },
    feedback: null,
    notice: null,
    draft: null,
  };
}

// Feedback, notices and maintenance windows last as long as the tab, as the demo's checks do.
function kept<T>(key: string): Map<string, T> {
  try {
    return new Map(Object.entries(JSON.parse(sessionStorage.getItem(key) ?? '{}')));
  } catch {
    return new Map();
  }
}
function keep<T>(key: string, m: Map<string, T>) {
  try {
    sessionStorage.setItem(key, JSON.stringify(Object.fromEntries(m)));
  } catch {
    // Storage unavailable: it lasts until the page reloads.
  }
}
const feedback = kept<IncidentFeedback>('nightjar.demo.feedback');
const notices = kept<IncidentNotice>('nightjar.demo.notices');
// Incidents are rebuilt on every request, so their feedback and notices are kept by start.
const keyOf = (i: Incident) => String(Math.round(i.started / 60));

// draft words an incident for visitors, as the backend does.
function draft(i: Incident): IncidentNotice | null {
  const names = i.affected.filter((a) => a.kind === 'check').map((a) => (a.label === 'plex' ? 'Movies & TV' : a.label === 'nas-backup' ? 'Backups' : a.label));
  if (!names.length) return null;
  const who = names.length === 1 ? names[0] : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`;
  const mins = Math.round(((i.ended ?? now()) - i.started) / 60);
  const day = new Date(i.started * 1000).toLocaleDateString('en', { weekday: 'long' });
  const plain: Record<string, string> = { memory: 'the server ran out of memory', disk: 'a disk on the server filled up', network: 'a network problem', crash: 'a service on the server crashed' };
  let text = `${who} ${names.length > 1 ? 'were' : 'was'} unavailable for ${mins} minutes on ${day}.`;
  if (i.cause && plain[i.cause.kind]) text += ` The cause was ${plain[i.cause.kind]}.`;
  text += i.events.some((e) => e.role === 'action' && e.severity !== 'warning') ? ' It was fixed automatically.' : ' It is resolved.';
  return { id: i.id, title: `${who} outage`, text, started: i.started, ended: i.ended, published: false };
}

export function demoFeedback(id: number, f: Partial<IncidentFeedback> | null) {
  const inc = demoIncidents(90).find((i) => i.id === id);
  if (!inc) return undefined;
  if (!f) feedback.delete(keyOf(inc));
  else feedback.set(keyOf(inc), { verdict: f.verdict ?? 'right', kind: inc.cause?.kind ?? '', actual: f.actual, note: f.note, subjects: inc.affected.map((a) => a.subject || a.label), at: now() });
  keep('nightjar.demo.feedback', feedback);
  return feedback.get(keyOf(inc)) ?? null;
}

export function demoNotice(id: number, n: Partial<IncidentNotice> | null) {
  const inc = demoIncidents(90).find((i) => i.id === id);
  if (!inc) return undefined;
  if (!n) notices.delete(keyOf(inc));
  else notices.set(keyOf(inc), { id, title: n.title ?? '', text: n.text ?? '', started: inc.started, ended: inc.ended, published: !!n.published, updated: now() });
  keep('nightjar.demo.notices', notices);
  return notices.get(keyOf(inc)) ?? null;
}

export const demoPublicNotices = () => [...notices.values()].filter((n) => n.published).sort((a, b) => b.started - a.started);

export function demoIncidents(days: number): Incident[] {
  const t = now();
  nextID = 9000;
  const list = [
    incident(
      t - 21 * MIN,
      null,
      'bazarr and tautulli',
      { kind: 'change', confidence: 'possibly', text: 'It began 5 minutes after: Bazarr updated|Bazarr updated' },
      [{ kind: 'crash', confidence: 'possibly', text: 'The bazarr service failed first' }],
      [
        { subject: 'unit:bazarr.service', label: 'bazarr', kind: 'service', down: t - 21 * MIN },
        { subject: 'unit:tautulli.service', label: 'tautulli', kind: 'service', down: t - 4 * MIN },
      ],
      [
        [-5 * MIN, 'before', 'ok', 'updates', 'Bazarr updated', 'Installed and running. 1.4.3 → 1.5.0'],
        [0, 'problem', 'critical', 'services', 'bazarr failed', "ModuleNotFoundError: No module named 'lxml'", 'unit:bazarr.service'],
        [17 * MIN, 'problem', 'warning', 'services', 'tautulli keeps restarting', 'Restarted 5 times in the last ten minutes.', 'unit:tautulli.service'],
      ],
      'calm',
    ),
    incident(
      t - 26 * HOUR,
      30 * MIN,
      'Out of memory',
      { kind: 'memory', confidence: 'likely', text: 'The machine ran out of memory and the kernel stopped rsync to free some, and nas-backup went down|Out of memory' },
      [{ kind: 'check', confidence: 'possibly', text: 'nas-backup: Connection refused. Start the service, or check its port' }],
      [
        { subject: '', label: 'Out of memory', kind: 'other', down: t - 26 * HOUR },
        { subject: 'check:nas-backup', label: 'nas-backup', kind: 'check', down: t - 26 * HOUR + 40, up: t - 26 * HOUR + 30 * MIN },
      ],
      [
        [-9 * MIN, 'before', 'warning', 'resources', 'Memory at 93%', 'Above the 90% limit, with 1.1 GB of 15.6 GB free.'],
        [0, 'problem', 'critical', 'system', 'Out of memory', 'Killed process 4182 (rsync) total-vm:9214332kB, anon-rss:8101204kB'],
        [40, 'problem', 'critical', 'uptime', 'nas-backup is down', 'Dial tcp 10.0.0.30:873: connect: connection refused', 'check:nas-backup'],
        [2 * MIN + 40, 'action', 'warning', 'uptime', 'Repair of nas-backup failed', 'Nightjar tried to run restart-rsync.sh: restart-rsync.sh exited with code 1.', 'check:nas-backup'],
        [
          4 * MIN + 40,
          'action',
          'info',
          'uptime',
          'Nightjar repaired nas-backup',
          'It was down, so Nightjar tried to run restart-rsync.sh: ran restart-rsync.sh. Checking it again now.',
          'check:nas-backup',
        ],
        [30 * MIN, 'recovery', 'ok', 'uptime', 'nas-backup is back up', 'Connected in 3 ms', 'check:nas-backup'],
      ],
      'memory',
    ),
    incident(
      t - 4 * DAY - 2 * HOUR,
      7 * MIN,
      'plex is down',
      { kind: 'disk', confidence: 'likely', text: '/ is 99% full, so programs could not write, and plex went down|/ is 99% full' },
      [],
      [{ subject: 'check:plex', label: 'plex', kind: 'check', down: t - 4 * DAY - 2 * HOUR, up: t - 4 * DAY - 2 * HOUR + 7 * MIN }],
      [
        [-6 * MIN, 'before', 'critical', 'storage', '/ is 99% full', 'Only 4.1 GB of 465.9 GB free. Nightjar is clearing caches and old logs.'],
        [0, 'problem', 'critical', 'uptime', 'plex is down', 'timed out after 10s', 'check:plex'],
        [
          40,
          'action',
          'info',
          'uptime',
          'Nightjar repaired plex',
          'It was down, so Nightjar tried to restart plexmediaserver.service: restarted plexmediaserver.service. Checking it again now.',
          'check:plex',
        ],
        [3 * MIN, 'note', 'ok', 'storage', 'Freed 38.2 GB on /', 'Cleared the Plex transcode cache and logs older than 7 days.'],
        [7 * MIN, 'recovery', 'ok', 'uptime', 'plex is back up', 'HTTP 200 in 4 ms', 'check:plex'],
      ],
      'disk',
    ),
    incident(
      t - 9 * DAY - 5 * HOUR,
      6 * MIN,
      'home-assistant, website and 1 more',
      { kind: 'network', confidence: 'likely', text: '3 checks failed within two minutes, so the network or this machine’s connection was likely down' },
      [],
      [
        { subject: 'check:home-assistant', label: 'home-assistant', kind: 'check', down: t - 9 * DAY - 5 * HOUR, up: t - 9 * DAY - 5 * HOUR + 6 * MIN },
        { subject: 'check:website', label: 'website', kind: 'check', down: t - 9 * DAY - 5 * HOUR + 20, up: t - 9 * DAY - 5 * HOUR + 5 * MIN },
        { subject: 'check:router', label: 'router', kind: 'check', down: t - 9 * DAY - 5 * HOUR + 55, up: t - 9 * DAY - 5 * HOUR + 5 * MIN },
      ],
      [
        [0, 'problem', 'critical', 'uptime', 'home-assistant is down', 'dial tcp 10.0.0.20:8123: i/o timeout', 'check:home-assistant'],
        [20, 'problem', 'critical', 'uptime', 'website is down', 'timed out after 10s', 'check:website'],
        [55, 'problem', 'critical', 'uptime', 'router is down', 'no reply', 'check:router'],
        [5 * MIN, 'recovery', 'ok', 'uptime', 'router is back up', 'reply in 2 ms', 'check:router'],
        [5 * MIN, 'recovery', 'ok', 'uptime', 'website is back up', 'HTTP 200 in 212 ms', 'check:website'],
        [6 * MIN, 'recovery', 'ok', 'uptime', 'home-assistant is back up', 'HTTP 200 in 191 ms', 'check:home-assistant'],
      ],
      'calm',
    ),
  ];
  return list.filter((i) => i.started >= t - days * DAY).map((i) => ({ ...i, feedback: feedback.get(keyOf(i)) ?? null, notice: notices.get(keyOf(i)) ?? null, draft: draft(i) }));
}

// ---------- repairs ----------

const repairs: RepairAttempt[] = [];

function seedRepairs() {
  if (repairs.length) return;
  const t = now();
  repairs.push(
    {
      id: 3,
      ts: t - 26 * HOUR + 4 * MIN + 40,
      check: 'nas-backup',
      action: 'script',
      target: 'restart-rsync.sh',
      ok: true,
      detail: 'ran restart-rsync.sh',
      output: 'rsyncd restarted\nlistening on 873',
      fixed: true,
    },
    {
      id: 2,
      ts: t - 26 * HOUR + 2 * MIN + 40,
      check: 'nas-backup',
      action: 'script',
      target: 'restart-rsync.sh',
      ok: false,
      detail: 'restart-rsync.sh exited with code 1',
      output: 'rsync: failed to bind: address in use',
      fixed: false,
    },
    { id: 1, ts: t - 4 * DAY - 2 * HOUR + 40, check: 'plex', action: 'restart', target: 'plexmediaserver.service', ok: true, detail: 'restarted plexmediaserver.service', output: '', fixed: true },
  );
}

export function demoRepairs(check: string): RepairsResponse {
  seedRepairs();
  return {
    repairs: repairs.filter((r) => !check || r.check === check),
    dir: '/opt/nightjar/repairs',
    scripts: [{ name: 'clear-cache.sh' }, { name: 'notes.txt', problem: 'notes.txt is not executable; run chmod +x on it' }, { name: 'restart-rsync.sh' }],
  };
}

export function demoRepairNow(spec: CheckSpec): RepairAttempt {
  seedRepairs();
  const r = spec.repair!;
  const rec: RepairAttempt = {
    id: repairs.length + 1,
    ts: now(),
    check: spec.name,
    action: r.action,
    target: (r.action === 'restart' ? r.unit : r.script) ?? '',
    ok: true,
    detail: r.action === 'restart' ? `restarted ${r.unit}` : `ran ${r.script}`,
    output: r.action === 'script' ? 'done' : '',
    fixed: false,
  };
  repairs.unshift(rec);
  return rec;
}

// ---------- the heartbeat ----------

// demoPings: the nightly backup ran at 03:00 today and took 14 minutes.
export function demoPings(): Pings {
  const start = new Date();
  start.setHours(3, 0, 0, 0);
  if (start.getTime() > Date.now()) start.setDate(start.getDate() - 1);
  const s = start.getTime() / 1000;
  return { seen: now() - 30 * DAY, start: s, ok: s + 14 * MIN + 12, fail: 0, exit: 0, output: 'sent 3.1 GB, 18,204 files\nbackup finished' };
}

export function demoHeartbeatDetail(): string {
  const p = demoPings();
  const next = new Date((p.start + DAY) * 1000).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' });
  const h = Math.floor((now() - p.ok) / HOUR);
  return `last ran ${h ? `${h}h` : `${Math.floor((now() - p.ok) / 60)} min`} ago; next run ${next}`;
}

// ---------- maintenance ----------

let windows: MaintenanceWindow[] | null = null;

function seedWindows(): MaintenanceWindow[] {
  if (windows) return windows;
  const saved = kept<MaintenanceWindow>('nightjar.demo.windows');
  if (saved.size) return (windows = [...saved.values()]);
  const sat = new Date();
  sat.setDate(sat.getDate() + ((6 - sat.getDay() + 7) % 7 || 7));
  sat.setHours(2, 0, 0, 0);
  windows = [
    {
      id: 'a1b2c3',
      title: 'Updating the NAS',
      checks: ['nas-backup'],
      start: sat.getTime() / 1000,
      end: sat.getTime() / 1000 + 90 * MIN,
      public: true,
      note: 'Backups pause while the NAS is updated.',
    },
    { id: 'd4e5f6', title: 'Nightly backup window', checks: ['nightly-backup', 'nas-backup'], days: [0, 1, 2, 3, 4, 5, 6], from: '03:00', to: '03:30', public: false },
  ];
  return windows;
}

// occurrence is a window's current or next time, as the backend works it out.
function occurrence(w: MaintenanceWindow, t: number): { start: number; end: number } | null {
  if (!w.days?.length) return (w.end ?? 0) > t ? { start: w.start ?? 0, end: w.end ?? 0 } : null;
  const [fh, fm] = (w.from ?? '00:00').split(':').map(Number);
  const [th, tm] = (w.to ?? '00:00').split(':').map(Number);
  let len = th * 60 + tm - (fh * 60 + fm);
  if (len <= 0) len += 24 * 60;
  for (let d = -1; d <= 7; d++) {
    const day = new Date(t * 1000);
    day.setHours(0, 0, 0, 0);
    day.setDate(day.getDate() + d);
    if (!w.days.includes(day.getDay())) continue;
    const start = day.getTime() / 1000 + (fh * 60 + fm) * 60;
    if (start + len * 60 > t) return { start, end: start + len * 60 };
  }
  return null;
}

export function demoMaintenance(): MaintenanceResponse {
  const t = now();
  const list = seedWindows();
  const upcoming = list.flatMap((w) => {
    const o = occurrence(w, t);
    return o && o.start <= t + 14 * DAY ? [{ window: w, ...o, on: o.start <= t }] : [];
  });
  upcoming.sort((a, b) => a.start - b.start);
  return { windows: list, upcoming };
}

export function demoSaveWindow(w: MaintenanceWindow): MaintenanceWindow | string {
  if (!w.title?.trim()) return 'a window needs a title of up to 80 characters';
  if (!w.days?.length && (!w.start || !w.end || w.end <= w.start)) return 'a window needs a start, and an end after it';
  const list = seedWindows();
  const saved = { ...w, id: w.id || Math.random().toString(16).slice(2, 14) };
  const i = list.findIndex((x) => x.id === saved.id);
  if (i >= 0) list[i] = saved;
  else list.push(saved);
  keep('nightjar.demo.windows', new Map(list.map((w) => [w.id, w])));
  return saved;
}

export function demoDeleteWindow(id: string): boolean {
  const list = seedWindows();
  const i = list.findIndex((x) => x.id === id);
  if (i >= 0) list.splice(i, 1);
  keep('nightjar.demo.windows', new Map(list.map((w) => [w.id, w])));
  return i >= 0;
}

export function demoResting(check: string): boolean {
  const t = now();
  return seedWindows().some((w) => (w.checks.length === 0 || w.checks.includes(check)) && (occurrence(w, t)?.start ?? Infinity) <= t);
}

export function demoPublicMaintenance() {
  return demoMaintenance()
    .upcoming.filter((o) => o.window.public && o.start <= now() + 7 * DAY)
    .map((o) => ({
      title: o.window.title,
      note: o.window.note,
      start: o.start,
      end: o.end,
      on: o.on,
      covers: o.window.checks.map((c) => (c === 'nas-backup' ? 'Backups' : c)),
      all: o.window.checks.length === 0,
    }));
}

// ---------- Docker ----------

export const demoContainers: Container[] = [
  { name: 'bazarr', image: 'lscr.io/linuxserver/bazarr', state: 'running', status: 'Up 21 minutes (unhealthy)', health: 'unhealthy' },
  { name: 'prowlarr', image: 'lscr.io/linuxserver/prowlarr', state: 'running', status: 'Up 3 days', health: undefined },
  { name: 'radarr', image: 'lscr.io/linuxserver/radarr', state: 'running', status: 'Up 3 days (healthy)', health: 'healthy' },
  { name: 'sonarr', image: 'lscr.io/linuxserver/sonarr', state: 'running', status: 'Up 3 days (healthy)', health: 'healthy' },
  { name: 'unpackerr', image: 'golift/unpackerr', state: 'exited', status: 'Exited (0) 2 days ago' },
];

// ---------- the weekly report ----------

export function demoReport(checks: { name: string; uptime_7d: number | null }[]): WeeklyReport {
  const t = now();
  const week = demoIncidents(7);
  const causes: Record<string, number> = {};
  const words: Record<string, string> = { memory: 'out of memory', disk: 'disk full', network: 'network', change: 'after a change', crash: 'crashed service' };
  for (const i of week) if (i.cause && i.cause.kind !== 'first') causes[words[i.cause.kind] ?? i.cause.kind] = (causes[words[i.cause.kind] ?? i.cause.kind] ?? 0) + 1;
  const list = checks.filter((c) => c.uptime_7d !== null).map((c) => ({ name: c.name, uptime: c.uptime_7d }));
  list.sort((a, b) => (a.uptime ?? 100) - (b.uptime ?? 100));
  return {
    from: t - 7 * DAY,
    to: t,
    checks: list,
    overall: list.length ? Math.round((list.reduce((n, c) => n + (c.uptime ?? 100), 0) / list.length) * 100) / 100 : null,
    incidents: week.length,
    open: week.filter((i) => i.ended === null).length,
    down: week.reduce((n, i) => n + ((i.ended ?? t) - i.started), 0),
    causes: Object.entries(causes).map(([name, n]) => ({ name, n })),
    top: week
      .map((i) => ({ id: i.id, title: i.title, cause: i.cause?.kind === 'first' ? '' : (i.cause?.text ?? ''), started: i.started, length: (i.ended ?? t) - i.started, open: i.ended === null }))
      .sort((a, b) => b.length - a.length)
      .slice(0, 3),
    repairs: 3,
    fixed: 2,
    alerts: 14,
    noisiest: [{ name: 'tautulli', n: 6 }],
    forecasts: ['The disk is 61% full and growing 0.4% a day: full in about 97 days', 'The certificate for example.com cert expires in 24 days'].slice(1),
    plex: demoPlexStats(7),
  };
}

// ---------- Plex ----------

// demoPlexStats is a household's week or month on Plex.
export function demoPlexStats(days: number): PlexStats {
  const t = now();
  const k = days / 7;
  const sat = new Date();
  sat.setDate(sat.getDate() - ((sat.getDay() + 1) % 7));
  sat.setHours(21, 10, 0, 0);
  return {
    days,
    from: t - days * DAY,
    to: t,
    plays: Math.round(47 * k),
    hours: Math.round(38.4 * k * 10) / 10,
    users: [
      { name: 'sam', n: Math.round(19 * k), hours: Math.round(16.2 * k * 10) / 10 },
      { name: 'alex', n: Math.round(14 * k), hours: Math.round(11.5 * k * 10) / 10 },
      { name: 'kim', n: Math.round(11 * k), hours: Math.round(7.9 * k * 10) / 10 },
      { name: 'guest', n: Math.round(3 * k), hours: Math.round(2.8 * k * 10) / 10 },
    ],
    titles: [
      { name: 'Severance', n: Math.round(9 * k), hours: Math.round(7.6 * k * 10) / 10 },
      { name: 'Heat', n: 2, hours: 5.6 },
      { name: 'The Bear', n: Math.round(6 * k), hours: Math.round(3.4 * k * 10) / 10 },
      { name: 'Massive Attack', n: Math.round(8 * k), hours: Math.round(2.9 * k * 10) / 10 },
      { name: 'Planet Earth II', n: Math.round(3 * k), hours: Math.round(2.6 * k * 10) / 10 },
    ],
    players: [
      { name: 'Living Room', n: Math.round(22 * k), hours: Math.round(21 * k) },
      { name: 'iPhone', n: Math.round(15 * k), hours: Math.round(9 * k) },
    ],
    peak: 4,
    peak_at: sat.getTime() / 1000,
    transcoded: 17,
    remote: 28,
    reasons: [
      { name: 'Lowering the quality to 720p', n: Math.round(5 * k), hours: 4.1 },
      { name: 'Burning in subtitles', n: Math.round(2 * k), hours: 2.2 },
      { name: 'The player cannot play Dolby TrueHD audio', n: 1, hours: 1.4 },
    ],
    weekdays: [7.2, 3.1, 4.4, 2.6, 5.0, 6.9, 9.2].map((h) => Math.round(h * k * 10) / 10),
    watched: Math.round(31 * k),
    viewers: [
      { name: 'sam', n: Math.round(13 * k) },
      { name: 'alex', n: Math.round(10 * k) },
    ],
    finished: [
      { name: 'Severance', n: Math.round(7 * k) },
      { name: 'The Bear', n: Math.round(5 * k) },
      { name: 'Heat', n: 1 },
      { name: 'Planet Earth II', n: Math.round(2 * k) },
    ],
    added: [
      { library: 'TV Shows', items: Math.round(14 * k), label: 'episodes' },
      { library: 'Movies', items: Math.round(2 * k), label: 'movies' },
    ],
  };
}

// ---------- the media cleanup ----------

const GB = 1024 ** 3;
let cleanup: CleanupView | null = null;

function seedCleanup(): CleanupView {
  if (cleanup) return cleanup;
  const saved = kept<CleanupView>('nightjar.demo.cleanup').get('view');
  if (saved) return (cleanup = saved);
  const t = now();
  const item = (over: Partial<CleanupItem> & Pick<CleanupItem, 'key' | 'title'>): CleanupItem => ({
    kind: 'movie',
    library: 'Movies',
    added: t - 74 * DAY,
    size: 8 * GB,
    status: 'leaving',
    listed: t - 2 * DAY,
    leaving_at: t + 5 * DAY,
    manager: 'radarr',
    torrents: [],
    ...over,
  });
  cleanup = {
    enabled: true,
    days: 60,
    notice_days: 7,
    ratio: 1,
    ratio_days: 30,
    managers: ['Sonarr', 'Radarr'],
    found: ['Sonarr at http://127.0.0.1:8989 (from /home/media/.config/Sonarr/config.xml)'],
    scanned: t - 2 * 3600,
    freed: 61.4 * GB,
    kept: [{ key: 'k9', title: 'The Godfather', at: t - 20 * DAY }],
    items: [
      item({
        key: 'k1',
        title: 'Waterworld',
        year: 1995,
        size: 14.2 * GB,
        status: 'waiting',
        leaving_at: t - 3 * DAY,
        listed: t - 10 * DAY,
        reason: 'seeding to ratio 1.00: at 0.64; removed anyway on Sat 29 Nov',
        torrents: [{ hash: 'a1', name: 'Waterworld.1995.1080p.BluRay', ratio: 0.64, size: 14.2 * GB }],
      }),
      item({ key: 'k2', title: 'Cats', year: 2019, size: 6.1 * GB, torrents: [{ hash: 'a2', name: 'Cats.2019.1080p.WEB', ratio: 1.42, size: 6.1 * GB }] }),
      item({
        key: 'k3',
        kind: 'show',
        title: 'The Big Leap',
        library: 'TV Shows',
        size: 22.8 * GB,
        manager: 'sonarr',
        added: t - 140 * DAY,
        leaving_at: t + 6 * DAY,
        torrents: [{ hash: 'a3', name: 'The.Big.Leap.S01.1080p', ratio: 2.1, size: 22.8 * GB }],
      }),
      item({ key: 'k4', title: 'Summer 2009', year: 2009, library: 'Home videos', size: 2.3 * GB, manager: 'plex', added: t - 300 * DAY }),
      item({ key: 'k5', title: 'Battlefield Earth', year: 2000, size: 9.4 * GB, status: 'removed', removed_at: t - 4 * DAY, leaving_at: t - 4 * DAY, torrents: [] }),
    ],
  };
  return cleanup;
}

function keepCleanup() {
  keep('nightjar.demo.cleanup', new Map([['view', cleanup!]]));
}

export const demoCleanup = (): CleanupView => seedCleanup();

export function demoCleanupKeep(key: string, on: boolean): CleanupView | undefined {
  const v = seedCleanup();
  if (on) {
    const i = v.items.findIndex((x) => x.key === key && x.status !== 'removed');
    if (i < 0) return undefined;
    v.kept.push({ key, title: v.items[i].title, at: now() });
    v.items.splice(i, 1);
  } else {
    const i = v.kept.findIndex((x) => x.key === key);
    if (i < 0) return undefined;
    v.kept.splice(i, 1);
  }
  keepCleanup();
  return v;
}

export function demoCleanupScan(): CleanupView {
  const v = seedCleanup();
  v.scanned = now();
  keepCleanup();
  return v;
}

// ---------- the activity log and two-factor sign-in ----------

export function demoActivity(): ActivityEntry[] {
  const t = now();
  let id = 100;
  const a = (ago: number, who: string, kind: string, action: string, detail = ''): ActivityEntry => ({ id: id--, ts: t - ago, who, kind, action, detail });
  return [
    a(4 * MIN, 'Discord user 1029384756', 'discord', 'Muted tautulli for an hour'),
    a(21 * MIN, 'Nightjar', 'repair', 'Repaired nas-backup: ran restart-rsync.sh'),
    a(2 * HOUR + 900, 'demo', 'account', 'Signed in', 'from 192.168.1.20'),
    a(2 * HOUR + 300, 'demo', 'maintenance', 'Planned maintenance'),
    a(26 * HOUR, 'Nightjar', 'cleanup', 'Removed Battlefield Earth (2000) · 9.4 GB (never watched), through radarr'),
    a(3 * DAY, 'demo', 'check', 'Changed the check plex'),
    a(3 * DAY + 600, 'API token', 'check', 'Added a check'),
    a(5 * DAY, 'demo', 'settings', 'Changed the appearance'),
  ];
}

const twoFactor = { on: false, left: 0 };
export const demoTwoFactor = () => twoFactor;

export function demoTwoFactorRoute(path: string, body: Record<string, string> | undefined): unknown | string | undefined {
  switch (path) {
    case '/api/auth/2fa/begin':
      return { secret: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP', link: 'otpauth://totp/Nightjar:demo?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Nightjar' };
    case '/api/auth/2fa/enable':
      if (!/^\d{6}$/.test(body?.code ?? '')) return 'that code is not right; use the current one from your authenticator app';
      Object.assign(twoFactor, { on: true, left: 10 });
      return { recovery: ['3f9a1-7c2e0', 'b81d4-0a9f3', '5e7c2-d41b8', '0c6af-93e15', 'a2d90-6b7f4', '7e13c-58da0', 'd94b2-1fe6c', '64f0a-c3b97', '1b8e5-a07d2', 'c5f37-2e9b1'] };
    case '/api/auth/2fa/disable':
      if (!body?.password || !body?.code) return 'the current password is not right';
      Object.assign(twoFactor, { on: false, left: 0 });
      return null;
  }
  return undefined;
}
