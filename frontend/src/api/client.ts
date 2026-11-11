import type {
  Finding,
  StatusProxyResponse,
  StatusProxyResult,
  AlertTestResult,
  AnalyticsRangeName,
  AgentFirewall,
  AttackView,
  FirewallRule,
  FirewallStatus,
  AnalyticsResponse,
  HostAnalytics,
  AppConfig,
  AppJob,
  AppsResponse,
  CheckResult,
  ConnectionTest,
  DrivesResponse,
  CheckSpec,
  ChecksResponse,
  HistoryResponse,
  HostBody,
  HostsResponse,
  HostSSH,
  LoginTest,
  SSHAction,
  SSHKey,
  SSHAccess,
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
  RemoteHost,
  PlexView,
  PublicStatus,
  QBAction,
  QBResponse,
  SaveResult,
  ServicesResponse,
  SetupStatus,
  Plugin,
  PluginCard,
  PluginChange,
  PluginsResponse,
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
  Appearance,
  IncidentsResponse,
  RepairAttempt,
  RepairsResponse,
  ContainersResponse,
  IncidentFeedback,
  IncidentNotice,
  MaintenanceResponse,
  MaintenanceWindow,
  ReportResponse,
  PlexStats,
  CleanupView,
  ActivityEntry,
  Viewer,
} from './types';

export class UnauthorizedError extends Error {
  constructor() {
    super('Unauthorized');
  }
}

// ApiError carries the server's message and, for rejected settings, the problem with each field.
// hostKey is the server's SSH host key when adding a key with a password waits for someone to confirm it.
export class ApiError extends Error {
  constructor(
    message: string,
    readonly fields: Record<string, string> = {},
    readonly hostKey?: string,
  ) {
    super(message);
  }
}

// transport sends API requests: the network normally, the in-browser demo data otherwise.
let transport: typeof fetch = (input, init) => fetch(input, init);
let demo = false;

// isDemo reports whether the dashboard is showing demo data instead of a real backend.
export const isDemo = () => demo;

// Demo data stands in for the backend with `npm run dev` (and its alias `npm run demo`), always,
// and with `npm run dev:live` when no backend answers. Both flags are build-time constants, so
// production builds drop the demo code entirely.
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

// Changes still waiting for their answer; a reload now would cut them off part-way.
let writing = 0;

// writesPending reports whether a change (any request but a GET) is still on its way.
export const writesPending = () => writing > 0;

// The only calls made without a session, where a 401 means a wrong password or code (the
// authenticator code is checked by login itself). Every other /api/auth/ call needs a session,
// so a 401 there means it ended.
const signInPaths = new Set(['/api/auth/login', '/api/auth/setup']);

async function request<T>(path: string, init: { method?: string; body?: unknown } = {}, timeoutMs = 15000): Promise<T> {
  const method = init.method ?? 'GET';
  // The server takes changes only as JSON, so one with nothing to say still sends an empty object.
  const body = init.body ?? (method === 'GET' || method === 'DELETE' ? undefined : {});
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  // The session is an HttpOnly cookie the browser sends itself.
  const headers: Record<string, string> = {};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const write = method !== 'GET';
  if (write) writing++;
  try {
    const res = await transport(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal, cache: 'no-store' });
    // A refused sign-in is an answer to show, not a sign that the session ended.
    if (res.status === 401 && !signInPaths.has(path)) {
      unauthorizedHandler();
      throw new UnauthorizedError();
    }
    if (!res.ok) {
      const err = (await res.json().catch(() => ({}))) as { error?: string; fields?: Record<string, string>; host_key?: string };
      throw new ApiError(err.error ?? `${path}: HTTP ${res.status}`, err.fields, err.host_key);
    }
    return (res.status === 204 ? undefined : await res.json()) as T;
  } catch (e) {
    // Our own time limit ran out: say so, rather than the browser's bare "signal is aborted".
    if (controller.signal.aborted) throw new ApiError(`No answer from Nightjar within ${Math.round(timeoutMs / 1000)} seconds`);
    throw e;
  } finally {
    clearTimeout(timer);
    if (write) writing--;
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
  login: (username: string, password: string, code = '') => request<{ username: string }>('/api/auth/login', { method: 'POST', body: { username, password, code } }),
  twoFactorBegin: () => request<{ secret: string; link: string }>('/api/auth/2fa/begin', { method: 'POST', body: {} }),
  // Turning it on asks for the password too, so an open session alone cannot.
  twoFactorEnable: (password: string, code: string) => request<{ recovery: string[] }>('/api/auth/2fa/enable', { method: 'POST', body: { password, code } }),
  twoFactorDisable: (password: string, code: string) => request<void>('/api/auth/2fa/disable', { method: 'POST', body: { password, code } }),
  activity: (before = 0) => request<{ activity: ActivityEntry[] }>(`/api/activity?limit=100${before ? `&before=${before}` : ''}`),
  backup: (secrets: boolean) => request<Record<string, unknown>>(`/api/backup${secrets ? '?secrets=1' : ''}`),
  restoreBackup: (backup: unknown) => request<{ restored: Record<string, number>; restarting: boolean }>('/api/backup/restore', { method: 'POST', body: backup }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST', body: {} }),
  viewers: () => request<{ viewers: Viewer[] }>('/api/auth/viewers'),
  addViewer: (username: string, password: string) => request<{ viewers: Viewer[] }>('/api/auth/viewers', { method: 'POST', body: { username, password } }),
  setViewerPassword: (username: string, password: string) => request<{ viewers: Viewer[] }>(`/api/auth/viewers/${encodeURIComponent(username)}`, { method: 'PUT', body: { password } }),
  removeViewer: (username: string) => request<{ viewers: Viewer[] }>(`/api/auth/viewers/${encodeURIComponent(username)}`, { method: 'DELETE' }),
  // changePassword signs every other browser out.
  changePassword: (current: string, next: string) => request<{ changed: boolean }>('/api/auth/password', { method: 'POST', body: { current, new: next } }),
  // stats with `since` returns only history samples newer than it.
  stats: (since?: number) => request<Stats>(since === undefined ? '/api/stats' : `/api/stats?since=${since}`),
  services: () => request<ServicesResponse>('/api/services'),
  history: (name: string, hours: number) => request<HistoryResponse>(`/api/services/history?name=${encodeURIComponent(name)}&hours=${hours}`),
  status: () => request<PublicStatus>('/api/status'),
  // plugins are the owner's own checks, cards and alert channels.
  plugins: () => request<PluginsResponse>('/api/plugins'),
  rescanPlugins: () => request<PluginsResponse>('/api/plugins/rescan', { method: 'POST', body: {} }),
  configurePlugin: (id: string, c: PluginChange) => request<{ plugin: Plugin }>(`/api/plugins/${encodeURIComponent(id)}`, { method: 'PUT', body: c }),
  // tryPlugin runs a plugin once; the server gives it up to 2 minutes.
  tryPlugin: (id: string, mode: 'check' | 'card', argument = '') =>
    request<{ check?: CheckResult; card?: PluginCard }>(`/api/plugins/${encodeURIComponent(id)}/try`, { method: 'POST', body: { mode, argument } }, 130000),
  pluginCards: () => request<{ cards: PluginCard[] }>('/api/plugins/cards'),
  incidents: (days = 14) => request<IncidentsResponse>(`/api/incidents?days=${days}`),
  feedback: (id: number, f: Pick<IncidentFeedback, 'verdict' | 'actual' | 'note'> | null) =>
    f ? request<{ feedback: IncidentFeedback }>(`/api/incidents/${id}/feedback`, { method: 'PUT', body: f }) : request<void>(`/api/incidents/${id}/feedback`, { method: 'DELETE' }),
  notice: (id: number, n: Pick<IncidentNotice, 'title' | 'text' | 'published'> | null) =>
    n ? request<{ notice: IncidentNotice }>(`/api/incidents/${id}/notice`, { method: 'PUT', body: n }) : request<void>(`/api/incidents/${id}/notice`, { method: 'DELETE' }),
  maintenance: () => request<MaintenanceResponse>('/api/maintenance'),
  saveMaintenance: (w: MaintenanceWindow) => request<{ window: MaintenanceWindow }>(w.id ? `/api/maintenance/${w.id}` : '/api/maintenance', { method: w.id ? 'PUT' : 'POST', body: w }),
  deleteMaintenance: (id: string) => request<void>(`/api/maintenance/${id}`, { method: 'DELETE' }),
  containers: () => request<ContainersResponse>('/api/docker/containers'),
  report: () => request<ReportResponse>('/api/report'),
  sendReport: () => request<{ sent: boolean }>('/api/report/send', { method: 'POST', body: {} }, 60000),
  repairs: (check = '') => request<RepairsResponse>(`/api/repairs${check ? `?check=${encodeURIComponent(check)}` : ''}`),
  // repairCheck runs a check's repair now; the server gives it up to 3 minutes.
  repairCheck: (name: string) => request<{ repair: RepairAttempt }>(`/api/checks/${encodeURIComponent(name)}/repair`, { method: 'POST', body: {} }, 190000),
  appearance: () => request<{ appearance: Appearance }>('/api/appearance'),
  saveAppearance: (a: Appearance) => request<{ appearance: Appearance }>('/api/appearance', { method: 'PUT', body: a }),
  statusDesign: () => request<StatusDesignResponse>('/api/statuspage'),
  saveStatusDesign: (d: StatusDesign) => request<{ design: StatusDesign }>('/api/statuspage', { method: 'PUT', body: d }),
  // The status page's domain in nginx: what is set up, setting it up (nginx and certbot take a while),
  // and taking it out.
  statusProxy: () => request<StatusProxyResponse>('/api/statuspage/proxy'),
  setUpStatusProxy: (email: string) => request<StatusProxyResult>('/api/statuspage/proxy', { method: 'POST', body: { email } }, 5.5 * 60 * 1000),
  removeStatusProxy: () => request<StatusProxyResult>('/api/statuspage/proxy', { method: 'DELETE' }, 90 * 1000),
  checks: () => request<ChecksResponse>('/api/checks'),
  // saveCheck creates a check, or replaces (and may rename) the one called `original`.
  saveCheck: (spec: CheckSpec, original?: string) =>
    original ? request<{ check: CheckSpec }>(checkPath(original), { method: 'PUT', body: spec }) : request<{ check: CheckSpec }>('/api/checks', { method: 'POST', body: spec }),
  deleteCheck: (name: string) => request<void>(checkPath(name), { method: 'DELETE' }),
  // testCheck runs a check once without saving it (its timeout is capped at 20s).
  // original names the saved check being edited, so a test without the password retyped uses its saved one.
  testCheck: (spec: CheckSpec, original?: string) => request<{ result: CheckResult }>('/api/checks/test', { method: 'POST', body: { ...spec, original } }, 30000),
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
  unitLogs: (name: string, lines = 80) => request<{ lines: string[]; findings?: Finding[] }>(`/api/units/${encodeURIComponent(name)}/logs?lines=${lines}`),
  // controlUnit starts, stops or restarts a systemd service and returns its new state.
  controlUnit: (name: string, action: UnitAction) => request<{ unit: Unit }>(`/api/units/${encodeURIComponent(name)}/${action}`, { method: 'POST', body: {} }, 95000),
  drives: () => request<DrivesResponse>('/api/drives'),
  // network keeps the live meter running; with `since` it returns only newer samples.
  // network is the live meter, asked every second: a reply that takes more than a few seconds is
  // given up on so the next poll can go out, rather than holding the meter still.
  // attacks is the watch for denial-of-service attacks on this machine.
  attacks: () => request<{ enabled: boolean; attacks?: AttackView }>('/api/attacks'),
  // setDefense switches defending against attacks on or off; unblock lifts a block and leaves the address alone for an hour.
  setDefense: (on: boolean) => request<{ enabled: boolean; attacks?: AttackView }>('/api/attacks/defense', { method: 'PUT', body: { on } }),
  unblock: (address: string) => request<{ enabled: boolean; attacks?: AttackView }>(`/api/attacks/blocked/${encodeURIComponent(address)}`, { method: 'DELETE' }),
  // This machine's firewall rules, and each agent's through it.
  firewall: () => request<{ enabled: boolean; firewall?: FirewallStatus }>('/api/firewall'),
  addFirewallRule: (rule: Partial<FirewallRule>) => request<{ enabled: boolean; firewall?: FirewallStatus }>('/api/firewall/rules', { method: 'POST', body: rule }),
  removeFirewallRule: (id: string) => request<{ enabled: boolean; firewall?: FirewallStatus }>(`/api/firewall/rules/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  updateFirewallRule: (id: string, rule: Partial<FirewallRule>) =>
    request<{ enabled: boolean; firewall?: FirewallStatus }>(`/api/firewall/rules/${encodeURIComponent(id)}`, { method: 'PUT', body: rule }),
  orderFirewall: (ids: string[]) => request<{ enabled: boolean; firewall?: FirewallStatus }>('/api/firewall/order', { method: 'PUT', body: { ids } }),
  setFirewallDefault: (def: '' | 'drop') => request<{ enabled: boolean; firewall?: FirewallStatus }>('/api/firewall/default', { method: 'PUT', body: { default: def } }),
  keepFirewallDefault: (keep: boolean) => request<{ enabled: boolean; firewall?: FirewallStatus }>('/api/firewall/default/keep', { method: 'POST', body: { keep } }),
  hostFirewall: (id: string) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall`, undefined, 40000),
  addHostRule: (id: string, rule: Partial<FirewallRule>) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/rules`, { method: 'POST', body: rule }, 40000),
  removeHostRule: (id: string, rule: string) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/rules/${encodeURIComponent(rule)}`, { method: 'DELETE' }, 40000),
  updateHostRule: (id: string, rule: string, r: Partial<FirewallRule>) =>
    request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/rules/${encodeURIComponent(rule)}`, { method: 'PUT', body: r }, 40000),
  orderHostRules: (id: string, ids: string[]) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/order`, { method: 'PUT', body: { ids } }, 40000),
  setHostDefault: (id: string, def: '' | 'drop') => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/default`, { method: 'PUT', body: { default: def } }, 40000),
  keepHostDefault: (id: string, keep: boolean) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/firewall/default/keep`, { method: 'POST', body: { keep } }, 40000),
  setHostDefense: (id: string, on: boolean) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/defense`, { method: 'PUT', body: { on } }, 40000),
  unblockHost: (id: string, address: string) => request<AgentFirewall>(`/api/hosts/${encodeURIComponent(id)}/blocked/${encodeURIComponent(address)}`, { method: 'DELETE' }, 40000),
  // How this machine's SSH server lets people in, and each agent server's through it: add a key to an
  // account, turn passwords off (they come back by themselves unless kept) or on again.
  sshd: () => request<SSHAccess>('/api/sshd'),
  addSSHDKey: (user: string, key: string) => request<SSHAccess>('/api/sshd/keys', { method: 'POST', body: { user, key } }),
  setKeysOnly: (on: boolean, user?: string) => request<SSHAccess>('/api/sshd/keys-only', { method: 'PUT', body: { on, user } }),
  keepKeysOnly: (keep: boolean) => request<SSHAccess>('/api/sshd/keys-only/keep', { method: 'POST', body: { keep } }),
  hostSSHD: (id: string) => request<SSHAccess>(`/api/hosts/${encodeURIComponent(id)}/sshd`, undefined, 40000),
  addHostSSHDKey: (id: string, user: string, key: string) => request<SSHAccess>(`/api/hosts/${encodeURIComponent(id)}/sshd/keys`, { method: 'POST', body: { user, key } }, 40000),
  setHostKeysOnly: (id: string, on: boolean, user?: string) => request<SSHAccess>(`/api/hosts/${encodeURIComponent(id)}/sshd/keys-only`, { method: 'PUT', body: { on, user } }, 40000),
  keepHostKeysOnly: (id: string, keep: boolean) => request<SSHAccess>(`/api/hosts/${encodeURIComponent(id)}/sshd/keys-only/keep`, { method: 'POST', body: { keep } }, 40000),
  network: (since?: number) => request<NetworkResponse>(since === undefined ? '/api/network' : `/api/network?since=${since}`, {}, 5000),
  // logs is the monitor's own recent log; with `after` only newer entries, for following it.
  logs: (q: { after?: number; level?: string; component?: string; q?: string; limit?: number }) => {
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
  // mute silences a check:, unit:, drive:, host: or area: subject for some minutes; 0 unmutes it.
  mute: (subject: string, label: string, minutes: number) => request<{ mutes: Mute[] }>('/api/notifications/mute', { method: 'POST', body: { subject, label, minutes } }),
  // hosts are the other servers watched through their agents; tokens never come back.
  hosts: () => request<HostsResponse>('/api/hosts'),
  host: (id: string) => request<{ host: RemoteHost }>(`/api/hosts/${encodeURIComponent(id)}`),
  hostToken: () => request<{ token: string; port: number }>('/api/hosts/token', { method: 'POST', body: {} }),
  // addHost checks that the agent (or the SSH port) answers before saving it; a server added with just an
  // SSH login comes back with its first login tried.
  addHost: (body: HostBody) => request<{ host: RemoteHost; test?: LoginTest }>('/api/hosts', { method: 'POST', body }, 60000),
  // updateHost keeps an agent's saved token when token is empty.
  updateHost: (id: string, body: HostBody) => request<{ host: RemoteHost }>(`/api/hosts/${encodeURIComponent(id)}`, { method: 'PUT', body }),
  // analytics is every server's history over a range; hostAnalytics one server's ("local" for this one).
  analytics: (range: AnalyticsRangeName) => request<AnalyticsResponse>(`/api/analytics?range=${range}`),
  hostAnalytics: (id: string, range: AnalyticsRangeName) => request<{ host: HostAnalytics; step: number }>(`/api/analytics/${encodeURIComponent(id)}?range=${range}`),
  deleteHost: (id: string) => request<void>(`/api/hosts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // SSH: the keys on this machine (public halves only), a server's login, and the fixed actions.
  sshKeys: () => request<{ keys: SSHKey[] }>('/api/ssh/keys'),
  newSSHKey: (name: string) => request<{ key: SSHKey }>('/api/ssh/keys', { method: 'POST', body: { name } }),
  // importSSHKey keeps a private key you already have on this machine; it never comes back out.
  importSSHKey: (name: string, privateKey: string) => request<{ key: SSHKey }>('/api/ssh/keys', { method: 'POST', body: { name, private: privateKey } }),
  // A login saved comes back already tried.
  setHostSSH: (id: string, ssh: HostSSH | null) =>
    request<{ host: RemoteHost; test?: LoginTest }>(`/api/hosts/${encodeURIComponent(id)}/ssh`, ssh ? { method: 'PUT', body: ssh } : { method: 'DELETE' }, 60000),
  // authorizeSSHKey puts a login's key on its server, logging in once with the password, which is kept nowhere.
  // The server gives that a minute and a half (checking the host key, then the login). The password only goes
  // to a server whose host key someone confirmed: otherwise it fails with the key in ApiError.hostKey.
  authorizeSSHKey: (body: { id?: string; login: HostSSH; password: string }) => request<{ ok: boolean; output?: string }>('/api/ssh/authorize', { method: 'POST', body }, 100000),
  // trustSSHHostKey keeps a server's host key once someone has checked it on the server.
  trustSSHHostKey: (body: { id?: string; login: HostSSH; host_key: string }) => request<{ ok: boolean }>('/api/ssh/trust', { method: 'POST', body }, 75000),
  // runHostSSH waits as long as the server lets the action run (5 minutes, for a reinstall over a slow
  // link): the action goes on to its end on the server either way, but giving up sooner would show it failed.
  runHostSSH: (id: string, action: SSHAction) => request<{ output: string }>(`/api/hosts/${encodeURIComponent(id)}/ssh/${action}`, { method: 'POST', body: {} }, 340000),
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
  cleanup: () => request<CleanupView>('/api/cleanup'),
  // cleanupScan checks every library now; the server gives that up to 5 minutes.
  cleanupScan: () => request<CleanupView>('/api/cleanup/scan', { method: 'POST', body: {} }, 310000),
  cleanupKeep: (key: string, keep: boolean) => request<CleanupView>(`/api/cleanup/${encodeURIComponent(key)}/keep`, keep ? { method: 'PUT', body: {} } : { method: 'DELETE' }),
  plexStats: (days = 7) => request<{ enabled: boolean; stats?: PlexStats }>(`/api/plex/stats?days=${days}`),
  plex: (view: PlexView = 'full') => request<PlexResponse>(view === 'summary' ? '/api/plex?view=summary' : '/api/plex'),
  stopPlexStream: (id: string, message: string) => request<PlexResponse>(`/api/plex/streams/${encodeURIComponent(id)}/stop`, { method: 'POST', body: { message } }),
};
