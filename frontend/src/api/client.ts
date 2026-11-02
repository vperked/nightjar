import type {
  AlertTestResult,
  AppConfig,
  AppJob,
  AppsResponse,
  CheckResult,
  ConnectionTest,
  DrivesResponse,
  CheckSpec,
  ChecksResponse,
  HistoryResponse,
  LogsResponse,
  NetworkResponse,
  Mute,
  NotificationCounts,
  NotificationQuery,
  NotificationRules,
  NotificationsResponse,
  NotificationSummary,
  AlertRules,
  QuietHours,
  PlexResponse,
  PlexView,
  PublicStatus,
  QBAction,
  QBResponse,
  SaveResult,
  ServicesResponse,
  SetupStatus,
  StatusDesign,
  StatusDesignResponse,
  SettingsResponse,
  Stats,
  TestTarget,
  Unit,
  UnitAction,
  UnitsResponse,
  UpdateSpec,
  UpdateTarget,
  UpdatesResponse,
} from './types';

export class UnauthorizedError extends Error {
  constructor() {
    super('Unauthorized');
  }
}

// ApiError carries the server's message and, for rejected settings, the problem with each field.
export class ApiError extends Error {
  constructor(
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
  }
}

// transport sends API requests: the network normally, the in-browser demo data otherwise.
let transport: typeof fetch = (input, init) => fetch(input, init);
let demo = false;

// isDemo reports whether the dashboard is showing demo data instead of a real backend.
export const isDemo = () => demo;

// Demo data stands in for the backend with `npm run demo`, and with `npm run dev` when no backend
// answers. Both flags are build-time constants, so production builds drop the demo code entirely.
const demoAllowed = import.meta.env.DEV || import.meta.env.VITE_DEMO === 'true';

// connect picks the transport before the app first renders.
export async function connect(): Promise<void> {
  if (!demoAllowed) return;
  if (import.meta.env.VITE_DEMO !== 'true' && (await backendAnswers())) return;
  const { demoFetch } = await import('./demo');
  transport = demoFetch;
  demo = true;
}

// backendAnswers asks the real API for its config; the dev proxy's error page does not count.
async function backendAnswers(): Promise<boolean> {
  try {
    const res = await fetch('/api/config', { cache: 'no-store', signal: AbortSignal.timeout(3000) });
    return res.ok && (res.headers.get('Content-Type') ?? '').includes('application/json');
  } catch {
    return false;
  }
}

let unauthorizedHandler = () => {};

// onUnauthorized registers the app's reaction to any 401 (asking to sign in again).
export function onUnauthorized(handler: () => void): void {
  unauthorizedHandler = handler;
}

async function request<T>(path: string, init: { method?: string; body?: unknown } = {}, timeoutMs = 15000): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  // The session is an HttpOnly cookie the browser sends itself.
  const headers: Record<string, string> = {};
  if (init.body !== undefined) headers['Content-Type'] = 'application/json';
  try {
    const res = await transport(path, {
      method: init.method ?? 'GET',
      headers,
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
      signal: controller.signal,
      cache: 'no-store',
    });
    // A refused sign-in is an answer to show, not a sign that the session ended.
    if (res.status === 401 && !path.startsWith('/api/auth/')) {
      unauthorizedHandler();
      throw new UnauthorizedError();
    }
    if (!res.ok) {
      const err = (await res.json().catch(() => ({}))) as { error?: string; fields?: Record<string, string> };
      throw new ApiError(err.error ?? `${path}: HTTP ${res.status}`, err.fields);
    }
    return (res.status === 204 ? undefined : await res.json()) as T;
  } finally {
    clearTimeout(timer);
  }
}

// waitForRestart polls until a restarted monitor answers again, then reloads the page.
export async function waitForRestart(): Promise<void> {
  await new Promise((r) => setTimeout(r, 1500));
  for (let i = 0; i < 60; i++) {
    try {
      await api.config();
      window.location.reload();
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
  window.location.reload();
}

const checkPath = (name: string) => `/api/checks/${encodeURIComponent(name)}`;
const updatePath = (id: string) => `/api/updates/${encodeURIComponent(id)}`;

export const api = {
  config: () => request<AppConfig>('/api/config'),
  // createAdmin makes the admin account on a fresh install (with the setup code from the log) and signs in.
  createAdmin: (username: string, password: string, setupCode: string) => request<{ username: string }>('/api/auth/setup', { method: 'POST', body: { username, password, setup_code: setupCode } }),
  login: (username: string, password: string) => request<{ username: string }>('/api/auth/login', { method: 'POST', body: { username, password } }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST', body: {} }),
  // changePassword signs every other browser out.
  changePassword: (current: string, next: string) => request<{ changed: boolean }>('/api/auth/password', { method: 'POST', body: { current, new: next } }),
  // stats with `since` returns only history samples newer than it.
  stats: (since?: number) => request<Stats>(since === undefined ? '/api/stats' : `/api/stats?since=${since}`),
  services: () => request<ServicesResponse>('/api/services'),
  history: (name: string, hours: number) => request<HistoryResponse>(`/api/services/history?name=${encodeURIComponent(name)}&hours=${hours}`),
  status: () => request<PublicStatus>('/api/status'),
  statusDesign: () => request<StatusDesignResponse>('/api/statuspage'),
  saveStatusDesign: (d: StatusDesign) => request<{ design: StatusDesign }>('/api/statuspage', { method: 'PUT', body: d }),
  checks: () => request<ChecksResponse>('/api/checks'),
  // saveCheck creates a check, or replaces (and may rename) the one called `original`.
  saveCheck: (spec: CheckSpec, original?: string) =>
    original ? request<{ check: CheckSpec }>(checkPath(original), { method: 'PUT', body: spec }) : request<{ check: CheckSpec }>('/api/checks', { method: 'POST', body: spec }),
  deleteCheck: (name: string) => request<void>(checkPath(name), { method: 'DELETE' }),
  // testCheck runs a check once without saving it (its timeout is capped at 20s).
  testCheck: (spec: CheckSpec) => request<{ result: CheckResult }>('/api/checks/test', { method: 'POST', body: spec }, 30000),
  settings: () => request<SettingsResponse>('/api/settings'),
  // testAlerts sends a test message to every configured channel (Discord, ntfy, email…).
  testAlerts: () => request<{ results: AlertTestResult[] }>('/api/alerts/test', { method: 'POST', body: {} }),
  // saveSettings stores changes ('' restores the default). Most apply at once; connections restart the monitor.
  saveSettings: (values: Record<string, string>) => request<SaveResult>('/api/settings', { method: 'PUT', body: { values } }),
  // testSettings tries a connection with unsaved values; "alerts" sends a real test alert to each channel they set up.
  testSettings: (target: TestTarget, values: Record<string, string>) => request<ConnectionTest>('/api/settings/test', { method: 'POST', body: { target, values } }, 30000),
  setup: () => request<SetupStatus>('/api/setup'),
  qbittorrent: () => request<QBResponse>('/api/qbittorrent'),
  qbCategories: () => request<{ categories: string[] }>('/api/qbittorrent/categories'),
  // qbAction starts, stops, reannounces, rechecks or deletes torrents (deleteFiles removes their data too).
  qbAction: (action: QBAction, hashes: string[], deleteFiles = false) => request<QBResponse>('/api/qbittorrent/action', { method: 'POST', body: { action, hashes, delete_files: deleteFiles } }),
  // qbAdd adds magnet links or URLs, one per line.
  qbAdd: (urls: string, category: string, stopped: boolean) => request<{ added: number }>('/api/qbittorrent/add', { method: 'POST', body: { urls, category, stopped } }),
  units: () => request<UnitsResponse>('/api/units'),
  unitLogs: (name: string, lines = 80) => request<{ lines: string[] }>(`/api/units/${encodeURIComponent(name)}/logs?lines=${lines}`),
  // controlUnit starts, stops or restarts a systemd service and returns its new state.
  controlUnit: (name: string, action: UnitAction) => request<{ unit: Unit }>(`/api/units/${encodeURIComponent(name)}/${action}`, { method: 'POST', body: {} }, 95000),
  drives: () => request<DrivesResponse>('/api/drives'),
  // network keeps the live meter running; with `since` it returns only newer samples.
  network: (since?: number) => request<NetworkResponse>(since === undefined ? '/api/network' : `/api/network?since=${since}`),
  // logs is the monitor's own recent log; with `after` only newer entries, for following it.
  logs: (q: { after?: number; level?: string; component?: string; q?: string }) => {
    const p = new URLSearchParams();
    for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v));
    return request<LogsResponse>(`/api/logs?${p}`);
  },
  notifications: (q: NotificationQuery = {}) => {
    const p = new URLSearchParams();
    if (q.q) p.set('q', q.q);
    if (q.severity?.length) p.set('severity', q.severity.join(','));
    if (q.area) p.set('area', q.area);
    if (q.unread) p.set('unread', '1');
    if (q.before) p.set('before', String(q.before));
    const qs = p.toString();
    return request<NotificationsResponse>(`/api/notifications${qs ? `?${qs}` : ''}`);
  },
  notificationSummary: () => request<NotificationSummary>('/api/notifications/summary'),
  // markRead marks notifications read (or, with unread, unread again); no ids marks them all.
  markRead: (ids?: number[], unread = false) => request<{ counts: NotificationCounts }>('/api/notifications/read', { method: 'POST', body: ids ? { ids, unread } : { all: true, unread } }),
  notificationRules: () => request<NotificationRules>('/api/notifications/rules'),
  saveNotificationRules: (rules: AlertRules, quiet: QuietHours) => request<NotificationRules>('/api/notifications/rules', { method: 'PUT', body: { rules, quiet } }),
  // mute silences a check:, unit:, drive: or area: subject for some minutes; 0 unmutes it.
  mute: (subject: string, label: string, minutes: number) => request<{ mutes: Mute[] }>('/api/notifications/mute', { method: 'POST', body: { subject, label, minutes } }),
  apps: () => request<AppsResponse>('/api/apps'),
  // appJob follows the running install; `after` skips log lines already shown.
  appJob: (after = 0) => request<{ job: AppJob | null }>(`/api/apps/job?after=${after}`),
  installApp: (id: string, port: number, check: boolean) => request<{ job: AppJob }>(`/api/apps/${encodeURIComponent(id)}/install`, { method: 'POST', body: { port, check } }),
  // removeApp uninstalls a store app; purge deletes its settings and data too.
  removeApp: (id: string, purge: boolean) => request<{ job: AppJob }>(`/api/apps/${encodeURIComponent(id)}/remove`, { method: 'POST', body: { purge } }),
  updates: () => request<UpdatesResponse>('/api/updates'),
  // saveUpdate adds a target, or replaces the one with this id (an empty api_key keeps the stored one).
  saveUpdate: (spec: UpdateSpec, isNew: boolean) =>
    isNew ? request<{ target: UpdateTarget }>('/api/updates', { method: 'POST', body: spec }) : request<{ target: UpdateTarget }>(updatePath(spec.id), { method: 'PUT', body: spec }),
  deleteUpdate: (id: string) => request<void>(updatePath(id), { method: 'DELETE' }),
  // checkUpdates queues a check of every target, or of one.
  checkUpdates: (id?: string) => request<{ queued: boolean }>(id ? `${updatePath(id)}/check` : '/api/updates/check', { method: 'POST', body: {} }),
  applyUpdate: (id: string) => request<{ queued: boolean }>(`${updatePath(id)}/update`, { method: 'POST', body: {} }),
  detectUpdates: () => request<{ added: UpdateTarget[] | null }>('/api/updates/detect', { method: 'POST', body: {} }),
  // The summary view omits charts, libraries and recent plays (used by the overview).
  plex: (view: PlexView = 'full') => request<PlexResponse>(view === 'summary' ? '/api/plex?view=summary' : '/api/plex'),
};
