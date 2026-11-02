// Mirrors the JSON returned by the Go backend (backend/internal/api).

export interface Memory {
  total: number;
  used: number;
  free: number;
  available: number;
  percent: number;
}

export interface Partition {
  device: string;
  mountpoint: string;
  fstype: string;
  total: number;
  used: number;
  free: number;
  percent: number;
  is_nvme: boolean;
}

export interface Process {
  pid: number;
  name: string;
  username?: string;
  cpu_percent: number;
  memory_percent: number;
}

// HostInfo identifies the machine (part of Stats).
export interface HostInfo {
  hostname: string;
  platform: string;
  platform_release: string;
  platform_version: string;
  architecture: string;
  uptime_seconds: number;
  uptime_formatted: string;
  users: number;
}

export interface Stats {
  timestamp: number;
  system: HostInfo;
  cpu: {
    percent: number;
    per_core: number[] | null;
    cores: number;
    freq: { current: number; min: number; max: number };
    load: number[] | null;
  };
  memory: Memory;
  swap: Memory;
  disk: {
    total: { total: number; used: number; free: number; percent: number; partitions: number };
    partitions: Partition[];
  };
  gpu: {
    name: string;
    utilization: number;
    memory_percent: number;
    memory_used_mb: number;
    memory_total_mb: number;
    temperature: number;
    power_draw: number;
    power_limit: number;
  } | null;
  temperature: number | null;
  fans: { label: string; speed_rpm: number }[];
  network: {
    bandwidth: { bytes_sent_per_sec: number; bytes_recv_per_sec: number } | null;
    interfaces: { name: string; addresses: string[] | null; is_up: boolean; speed: number }[] | null;
  };
  processes: {
    total: number;
    by_status: Record<string, number>;
    top_cpu: Process[] | null;
    top_memory: Process[] | null;
  };
  history: Sample[];
}

export interface Sample {
  ts: number;
  cpu: number;
  memory: number;
  disk: number;
  rx_bps: number | null;
  tx_bps: number | null;
}

export type ServiceStatus = 'up' | 'down' | 'unknown' | 'paused';

export interface Beat {
  ts: number;
  up: boolean;
  latency_ms?: number | null;
}

export interface Service {
  name: string;
  type: string;
  target: string;
  status: ServiceStatus;
  latency_ms: number | null;
  detail: string;
  last_check: number | null;
  last_change: number | null;
  uptime_24h: number | null;
  uptime_7d: number | null;
  uptime_30d: number | null;
  recent: Beat[];
}

export interface ServicesResponse {
  services: Service[];
}

export type CheckType = 'http' | 'tcp' | 'ping' | 'tls';

// CheckSpec is one uptime check's definition, as stored and edited in the dashboard.
export interface CheckSpec {
  name: string;
  type: CheckType;
  target: string;
  interval: number;
  timeout: number;
  retries: number;
  expected_status: number[] | null;
  keyword: string;
  verify_tls: boolean;
  tls_warn_days: number;
  paused: boolean;
}

// CheckResult is one run of a check (POST /api/checks/test).
export interface CheckResult {
  up: boolean;
  latency_ms: number | null;
  detail: string;
}

export interface ChecksResponse {
  checks: CheckSpec[];
  types: CheckType[];
}

export type SettingKind = 'bool' | 'int' | 'number' | 'text' | 'secret' | 'list';

// SettingField is one setting: secrets report only whether they are set; startup settings
// come from the environment and are read-only here.
export interface SettingField {
  key: string;
  group: string;
  label: string;
  help?: string;
  unit?: string;
  kind: SettingKind;
  default: string;
  min?: number;
  max?: number;
  options?: string[];
  startup?: boolean;
  live?: boolean; // applies on the next cycle, without a restart
  advanced?: boolean; // a tuning knob, folded away by default
  value: string;
  set: boolean;
  source: 'dashboard' | 'environment' | 'default';
}

// AlertTestResult is one channel's answer to a test alert.
export interface AlertTestResult {
  channel: string;
  ok: boolean;
  error?: string;
}

export interface SettingsResponse {
  groups: { name: string; fields: SettingField[] }[];
}

// HistoryPoint is one check, or a time bucket of checks on long ranges: latency_ms is then
// the mean of successful checks and fails counts the failures it stands for.
export interface HistoryPoint {
  ts: number;
  up: boolean;
  latency_ms: number | null;
  detail?: string;
  fails?: number;
}

export interface HistoryResponse {
  name: string;
  hours: number;
  points: HistoryPoint[];
}

export interface PublicService {
  name: string;
  status: ServiceStatus;
  uptime_24h: number | null;
  uptime_30d: number | null;
  recent: Beat[];
}

// PublicStatus is the public status page (GET /api/status): only what its design shows.
export interface PublicStatus {
  title: string;
  overall: 'operational' | 'degraded' | 'outage';
  updated: number;
  description?: string;
  footer?: string;
  show_uptime?: boolean;
  show_history?: boolean;
  announcement?: { text: string; tone: 'info' | 'warning' | 'crit' };
  groups?: { name: string; services: PublicService[] }[];
  services: PublicService[];
}

// StatusDesign is how the public status page looks, edited in the dashboard.
export interface StatusDesign {
  title: string;
  description: string;
  announcement: string;
  tone: 'info' | 'warning' | 'crit';
  show_uptime: boolean;
  show_history: boolean;
  footer: string;
  groups: { name: string; items: { check: string; label?: string }[] }[];
}

export interface StatusDesignResponse {
  enabled: boolean;
  designed: boolean;
  design: StatusDesign;
  checks: string[];
}

export interface AppConfig {
  auth_required: boolean;
  authorized: boolean; // signed in
  account?: { configured: boolean; username?: string; service?: string }; // the admin account; not configured on a fresh install
  status_page_enabled: boolean;
  status_page_title: string;
  plex_enabled?: boolean;
  qbittorrent_enabled?: boolean;
  needs_setup?: boolean; // no alert channel or no uptime check yet
  hidden?: string[]; // dashboard sections turned off (DASHBOARD_HIDDEN)
  version?: string;
}

export type PlexDecision = 'direct play' | 'direct stream' | 'transcode';

export interface PlexStream {
  id: string;
  user: string;
  kind: string;
  title: string;
  subtitle: string;
  player: string;
  product: string;
  platform: string;
  state: string;
  local: boolean;
  decision: PlexDecision;
  video_decision?: string;
  audio_decision?: string;
  hw_transcode: boolean;
  throttled: boolean;
  speed?: number;
  quality: string;
  bitrate_kbps: number;
  bandwidth_kbps: number;
  duration_ms: number;
  offset_ms: number;
  progress: number;
  started_at: number;
}

export interface PlexLibrary {
  key: string;
  title: string;
  type: string;
  count: number;
  child_count?: number;
  child_label?: string;
  refreshing: boolean;
}

export interface PlexSample {
  ts: number;
  direct: number;
  transcode: number;
  bandwidth_kbps: number;
}

export interface PlexPlay {
  user: string;
  title: string;
  subtitle: string;
  player: string;
  decision: PlexDecision;
  started: number;
  ended: number;
  progress: number;
}

export interface PlexState {
  url: string;
  status: 'up' | 'down' | 'pending';
  detail: string;
  since: number;
  checked_at: number;
  latency_ms: number | null;
  server: {
    name: string;
    version: string;
    platform: string;
    platform_version: string;
    update_available?: string;
  } | null;
  streams: PlexStream[];
  direct: number;
  transcode: number;
  bandwidth_kbps: number;
  wan_kbps: number;
  libraries: PlexLibrary[];
  libraries_at: number;
  samples: PlexSample[];
  recent: PlexPlay[];
}

export type PlexView = 'full' | 'summary';

export interface PlexResponse {
  enabled: boolean;
  plex?: PlexState;
}

export type UpdateKind = 'system' | 'snap' | 'apt' | 'arr' | 'git' | 'release';

// UpdateSpec is one thing to keep updated (backend/internal/updates.Spec); only its kind's fields matter.
export interface UpdateSpec {
  id: string;
  name: string;
  kind: UpdateKind;
  auto: boolean;
  paused: boolean;
  unit?: string;
  restart?: boolean;
  package?: string;
  url?: string;
  api_key?: string; // write-only: sent to set it, never returned
  config_file?: string;
  dir?: string;
  repo?: string;
  tag_prefix?: string;
  version_command?: string;
  update_command?: string;
  self?: boolean;
}

export interface UpdateRun {
  at: number;
  from?: string;
  to?: string;
  ok: boolean;
  auto: boolean;
  detail?: string;
}

export interface UpdateTarget extends UpdateSpec {
  installed?: string;
  latest?: string;
  available: boolean;
  pending?: string[] | null;
  checked?: number;
  error?: string;
  runs?: UpdateRun[] | null;
  api_key_set?: boolean;
  can_update: boolean;
  busy?: 'checking' | 'updating' | 'queued';
}

export interface UpdatesResponse {
  enabled: boolean;
  interval: number;
  window: { start: number; end: number; label: string };
  commands_editable: boolean;
  kinds: UpdateKind[];
  checking: boolean;
  last_check: number;
  targets: UpdateTarget[];
}

// qBittorrent, as the monitoring loop last saw it (backend/internal/qbittorrent.View).
export interface QBTorrent {
  hash: string;
  name: string;
  size: number;
  progress: number; // 0..1
  state: string; // lower-cased qBittorrent state, e.g. "stalleddl"
  dlspeed: number;
  upspeed: number;
  ratio: number;
  eta: number; // seconds; 8640000 means unknown
  num_seeds: number;
  num_leechs: number;
  added_on: number;
  completion_on: number;
  category: string;
  save_path: string;
  downloaded: number;
  uploaded: number;
  stalled_for?: number;
  recoveries?: number;
}

export interface QBTransfer {
  dl_info_speed: number;
  up_info_speed: number;
  dl_info_data: number;
  up_info_data: number;
  dl_rate_limit: number;
  up_rate_limit: number;
  connection_status: string;
  dht_nodes: number;
}

export interface QBState {
  connected: boolean;
  error?: string;
  version?: string;
  url: string;
  updated: number;
  transfer: QBTransfer | null;
  torrents: QBTorrent[];
  history: { ts: number; dl: number; up: number }[];
}

export type QBAction = 'start' | 'stop' | 'reannounce' | 'recheck' | 'delete';

export interface QBResponse {
  enabled: boolean;
  qbittorrent?: QBState;
}

// SaveResult says whether saved settings apply at once or restart the monitor (and for which keys).
export interface SaveResult {
  restarting: boolean;
  applied?: boolean;
  restart_for?: string[];
}

export type TestTarget = 'plex' | 'qbittorrent' | 'alerts';

// ConnectionTest is the result of trying settings before saving them.
export interface ConnectionTest {
  ok: boolean;
  detail?: string;
  results?: AlertTestResult[];
}

export interface CheckSuggestion {
  name: string;
  type: CheckType;
  target: string;
}

// SetupStatus is what is set up, and what the monitor found on its machine (GET /api/setup).
export interface SetupStatus {
  alerts: { configured: boolean; channels: string[] | null; webhook_set: boolean };
  plex: { enabled: boolean; url: string; token_set: boolean; detected: { running: boolean; url: string; version?: string; token_found: boolean } };
  qbittorrent: {
    enabled: boolean;
    host: string;
    port: number;
    username: string;
    password_set: boolean;
    detected: { found: boolean; host: string; port: number; username?: string; local_bypass: boolean; answering: boolean };
  };
  checks: { count: number; suggestions: CheckSuggestion[] };
  updates: { enabled: boolean; targets: number; window: string };
}

// Unit is one systemd service (GET /api/units).
export interface Unit {
  name: string;
  description: string;
  active: string; // active, failed, activating, deactivating, inactive
  sub: string; // running, exited, auto-restart, dead…
  enabled?: string;
  result?: string;
  exit_status?: number;
  restarts: number;
  since?: number;
  flapping?: number;
  ignored?: boolean;
}

export interface UnitsResponse {
  enabled: boolean;
  self?: string; // the monitor's own unit, which the dashboard will not stop
  units: Unit[];
}

export type UnitAction = 'start' | 'stop' | 'restart';

// Drive is one disk's SMART reading and verdict (GET /api/drives).
export interface Drive {
  device: string;
  type: string;
  model?: string;
  serial?: string;
  capacity?: number;
  rotational: boolean;
  passed?: boolean;
  temperature?: number;
  power_on_hours?: number;
  reallocated?: number;
  pending?: number;
  uncorrectable?: number;
  crc_errors?: number;
  critical_warning?: number;
  percent_used?: number;
  available_spare?: number;
  media_errors?: number;
  asleep: boolean;
  checked?: number;
  error?: string;
  health: 'good' | 'warning' | 'failing' | 'unknown';
  problems?: string[] | null;
}

export interface DrivesResponse {
  enabled: boolean;
  error?: string;
  drives: Drive[];
  limits?: { hdd: number; ssd: number };
}

// The live network meter (GET /api/network), sampled each second while the page is open.
export interface NetInterface {
  name: string;
  up: boolean;
  default: boolean;
  virtual: boolean;
  speed_mbps?: number;
  rx_bps: number;
  tx_bps: number;
  rx_pps: number;
  tx_pps: number;
  rx_peak: number;
  tx_peak: number;
  rx_avg: number;
  tx_avg: number;
  rx_total: number;
  tx_total: number;
  errors: number;
  drops: number;
  new_errors?: number;
  new_drops?: number;
}

export interface NetSample {
  ts: number;
  rx: number;
  tx: number;
}

export interface NetTalker {
  name: string;
  unit?: string;
  pids?: number[] | null;
  connections: number;
  rx_bps: number;
  tx_bps: number;
  source?: string; // "tcp", or "qbittorrent" when its own API supplied the numbers
}

export interface NetHost {
  peer: string;
  process?: string;
  connections: number;
  rx_bps: number;
  tx_bps: number;
}

export interface NetworkView {
  interfaces: NetInterface[];
  samples: Record<string, NetSample[]>;
  processes: NetTalker[];
  hosts: NetHost[];
  connections: number;
  since: number;
}

export interface NetworkResponse {
  enabled: boolean;
  network?: NetworkView;
  history?: Sample[]; // the loop's half hour of samples, all interfaces
}

// ---------- notifications ----------

export type Severity = 'critical' | 'warning' | 'ok' | 'info';
export type NotificationStatus = 'sent' | 'failed' | 'held' | 'digest' | 'muted' | 'kept';
export type AlertLevel = 'all' | 'warning' | 'critical' | 'off';

export interface Notification {
  id: number;
  ts: number;
  area: string;
  severity: Severity;
  title: string;
  body: string;
  fields: { name: string; value: string }[];
  subject?: string; // "check:Plex", "unit:sonarr.service", "drive:S4EV…"
  status: NotificationStatus;
  channels: string[]; // delivered to, or held for
  error?: string;
  read: boolean;
}

export interface NotificationCounts {
  unread: number;
  alerts: number; // unread warnings and criticals
  worst?: Severity;
  latest: number;
}

export interface NotificationsResponse {
  items: Notification[];
  total: number;
  counts: NotificationCounts;
  kept: boolean; // false when the database is unavailable
}

export interface Mute {
  subject: string;
  label: string;
  until: number;
}

export interface NotificationSummary {
  counts: NotificationCounts;
  mutes: Mute[];
  quiet: boolean;
  held: number;
}

export interface QuietHours {
  enabled: boolean;
  start: string; // "23:00"
  end: string;
  critical: boolean; // let critical alerts through
}

export type AlertRules = Record<string, Record<string, AlertLevel>>; // area → channel → level

export interface NotificationRules {
  areas: { id: string; label: string }[];
  channels: string[];
  rules: AlertRules;
  defaults: AlertRules;
  quiet: QuietHours;
  quiet_active: boolean;
  held: number;
  mutes: Mute[];
}

export interface NotificationQuery {
  q?: string;
  severity?: Severity[];
  area?: string;
  unread?: boolean;
  before?: number;
}

// ---------- the monitor's own log ----------

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface LogEntry {
  seq: number;
  time: number; // unix milliseconds
  level: LogLevel;
  component?: string;
  message: string;
  attrs?: { key: string; value: string }[];
}

export interface LogsResponse {
  entries: LogEntry[];
  latest: number;
  components: string[];
  level: string; // LOG_LEVEL
  file: string;
  kept: number;
}

// ---------- app store ----------

export interface StoreApp {
  id: string;
  name: string;
  category: string;
  summary: string;
  homepage: string;
  port: number; // 0: no web interface
  sign_in?: string;
  color: string;
  installed: boolean;
  by?: 'store' | 'swizzin' | 'system';
  running: boolean;
  version?: string;
  unit?: string;
  available: boolean;
}

export interface AppJob {
  id: number;
  app: string;
  action: 'install' | 'remove';
  step: string;
  started: number;
  finished?: number;
  ok: boolean;
  error?: string;
  lines: string[];
}

export interface AppsResponse {
  apps: StoreApp[];
  job: AppJob | null;
  user: string; // the account store apps run as
}
