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

export type ServiceStatus = 'up' | 'down' | 'unknown' | 'paused' | 'maintenance';

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
  cause?: Finding; // a down check's detail, explained
  schedule?: string; // a heartbeat's
  pings?: Pings; // what a heartbeat has heard
  repair?: Repair; // what Nightjar does when it goes down
  server?: string; // the id of the watched server it runs on
}

// Pings is what a heartbeat check has heard, in unix seconds (0 for never).
export interface Pings {
  seen: number;
  start: number;
  ok: number;
  fail: number;
  exit: number;
  output?: string;
}

export interface ServicesResponse {
  services: Service[];
}

export type CheckType = 'http' | 'tcp' | 'ping' | 'tls' | 'plugin' | 'heartbeat' | 'docker';

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
  // How soon a failed check runs again while it is not yet down, in seconds.
  retry_interval: number;
  // A ping check sends ping_count echoes, ping_interval seconds apart.
  ping_count: number;
  ping_interval: number;
  // A heartbeat's schedule (cron, @daily or "every 1h"), how late its ping may be and how long a
  // run may take (0 for no limit), in seconds. Its target is the token in its ping URL.
  schedule?: string;
  grace?: number;
  max_runtime?: number;
  repair?: Repair;
  // server is the id of the watched server the checked thing runs on.
  server?: string;
}

// Repair is what Nightjar does when a check goes down: restart a systemd service or a Docker
// container, or run a script from the repairs folder, at most `max` times an outage, `wait` seconds apart.
export interface Repair {
  action: 'restart' | 'container' | 'script';
  unit?: string;
  container?: string;
  script?: string;
  max: number;
  wait: number;
}

// RepairAttempt is one repair Nightjar made, and whether the check came back after it.
export interface RepairAttempt {
  id: number;
  ts: number;
  check: string;
  action: Repair['action'];
  target: string;
  ok: boolean;
  detail: string;
  output: string;
  fixed: boolean;
}

export interface RepairsResponse {
  repairs: RepairAttempt[];
  dir: string;
  scripts: { name: string; problem?: string }[];
}

export type IncidentRole = 'before' | 'problem' | 'recovery' | 'action' | 'note';

export interface IncidentEvent {
  id: number;
  ts: number;
  area: string;
  severity: Notification['severity'];
  title: string;
  body?: string;
  subject?: string;
  role: IncidentRole;
}

export interface IncidentCause {
  kind: 'memory' | 'disk' | 'hardware' | 'restart' | 'crash' | 'network' | 'change' | 'overload' | 'check' | 'first';
  text: string;
  confidence: 'likely' | 'possibly';
  event?: number;
  learned?: boolean; // shaped by feedback on earlier incidents
}

// IncidentFeedback is what the owner said about an incident's cause.
export interface IncidentFeedback {
  verdict: 'right' | 'wrong';
  kind: string; // the cause Nightjar gave
  actual?: IncidentCause['kind'] | '';
  note?: string;
  subjects: string[];
  at: number;
}

// IncidentNotice is an incident as visitors to the status page read it.
export interface IncidentNotice {
  id: number;
  title: string;
  text: string;
  started: number;
  ended: number | null;
  published: boolean;
  updated?: number;
}

// MaintenanceWindow is planned downtime: one-off (start to end) or weekly (days, from to to).
export interface MaintenanceWindow {
  id: string;
  title: string;
  checks: string[]; // empty covers everything
  start?: number;
  end?: number;
  days?: number[]; // 0 is Sunday
  from?: string; // "02:00"
  to?: string;
  public: boolean;
  note?: string;
}

export interface MaintenanceResponse {
  windows: MaintenanceWindow[];
  upcoming: { window: MaintenanceWindow; start: number; end: number; on: boolean }[];
}

export interface Container {
  name: string;
  image: string;
  state: string;
  status: string;
  health?: string;
}

export interface ContainersResponse {
  available: boolean;
  error?: string;
  containers: Container[];
}

// PlexStats is Plex over some days: what played and by whom, from Nightjar's record of every
// finished stream, and what Plex's own history says was watched.
export interface PlexStats {
  days: number;
  from: number;
  to: number;
  plays: number;
  hours: number;
  users: PlexCount[];
  titles: PlexCount[];
  players: PlexCount[];
  peak: number;
  peak_at: number;
  transcoded: number | null; // percent of streams
  remote: number | null;
  reasons?: PlexCount[]; // why streams were converted
  weekdays: number[]; // hours, Sunday first
  watched: number;
  viewers: PlexCount[];
  finished: PlexCount[];
  history?: string; // why Plex's history could not be read
  added: { library: string; items: number; label: string }[];
}

export interface PlexCount {
  name: string;
  n: number;
  hours?: number;
}

// CleanupItem is a movie or show the media cleanup listed: nobody has watched it since Plex added it.
export interface CleanupItem {
  key: string;
  kind: 'movie' | 'show';
  title: string;
  year?: number;
  library: string;
  added: number;
  size: number;
  status: 'preview' | 'leaving' | 'waiting' | 'removed' | 'failed';
  listed: number;
  leaving_at: number;
  reason?: string;
  manager: 'radarr' | 'sonarr' | 'plex';
  torrents: { hash: string; name: string; ratio: number; size: number }[];
  removed_at?: number;
}

export interface CleanupView {
  enabled: boolean;
  items: CleanupItem[];
  kept: { key: string; title: string; at: number }[];
  scanned: number;
  error?: string;
  freed: number;
  found?: string[]; // Sonarr and Radarr found on this machine rather than set
  days: number;
  notice_days: number;
  ratio: number;
  ratio_days: number;
  managers: string[];
}

// WeeklyReport is the week as the Monday report sums it up.
export interface WeeklyReport {
  from: number;
  to: number;
  checks: { name: string; uptime: number | null }[];
  overall: number | null;
  incidents: number;
  open: number;
  down: number;
  causes: { name: string; n: number }[];
  top: { id: number; title: string; cause: string; started: number; length: number; open: boolean }[];
  repairs: number;
  fixed: number;
  alerts: number;
  noisiest: { name: string; n: number }[];
  forecasts: string[];
  plex?: PlexStats;
}

export interface ReportResponse {
  report: WeeklyReport;
  enabled: boolean;
  at: string;
  next?: number;
}

// Incident is one stretch of trouble: what went wrong, what happened around it, and why.
export interface Incident {
  id: number;
  started: number;
  ended: number | null; // null while something in it is still down
  severity: Notification['severity'];
  title: string;
  cause: IncidentCause | null;
  also: IncidentCause[];
  affected: { subject: string; label: string; kind: 'check' | 'service' | 'drive' | 'other'; down: number; up?: number }[];
  events: IncidentEvent[];
  series: { t: number; cpu: number | null; mem: number | null; disk: number | null }[];
  peaks: { cpu: number | null; mem: number | null; disk: number | null };
  feedback: IncidentFeedback | null;
  notice: IncidentNotice | null;
  draft: IncidentNotice | null; // a notice worded from it, when something on the status page was affected
}

export interface IncidentsResponse {
  incidents: Incident[];
  days: number;
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
  uptime_90d?: number | null;
  recent: Beat[];
  days?: PublicDay[]; // the last 90 days, oldest first; empty when the design hides history
}

// PublicDay is one day of a check on the status page: the share of checks it passed (null without
// any) and about how long it was down.
export interface PublicDay {
  t: number; // local midnight
  uptime: number | null;
  down_s: number;
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
  css?: string; // the owner's own styles
  header_html?: string; // the owner's HTML above the title, cleaned before it is shown
  footer_html?: string; // and above the footer
  maintenance?: { title: string; note?: string; start: number; end: number; on: boolean; covers: string[]; all: boolean }[];
  incidents?: IncidentNotice[]; // the notices the owner published
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
  css?: string;
  header_html?: string;
  footer_html?: string;
}

export interface StatusDesignResponse {
  enabled: boolean;
  designed: boolean;
  design: StatusDesign;
  checks: string[];
  days?: Record<string, PublicDay[]>; // each check's days, for the preview
}

export interface AppConfig {
  auth_required: boolean;
  authorized: boolean; // signed in
  account?: { configured: boolean; username?: string; service?: string; two_factor?: boolean; recovery_left?: number }; // the admin account; not configured on a fresh install
  status_page_enabled: boolean;
  status_page_title: string;
  plex_enabled?: boolean;
  qbittorrent_enabled?: boolean;
  needs_setup?: boolean; // no alert channel or no uptime check yet
  hidden?: string[]; // dashboard sections turned off (DASHBOARD_HIDDEN)
  appearance?: Appearance; // how the dashboard looks; without a sign-in, no CSS
  version?: string;
}

// Appearance is the dashboard's look, chosen under Settings → Appearance.
export interface Appearance {
  style: 'matrix' | 'cards';
  charts: '' | 'dots' | 'lines'; // empty follows the style
  density: 'comfortable' | 'compact';
  css: string; // the owner's own, over the style
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
  reason?: string; // why Plex converts it
  fix?: string; // what would let it play as it is
  speed?: number;
  quality: string;
  bitrate_kbps: number;
  bandwidth_kbps: number;
  duration_ms: number;
  offset_ms: number;
  progress: number;
  started_at: number;
  address?: string;
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
  // log is the end of what the update did and printed.
  log?: string[];
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
  // live is what the update under way has done so far: each command, what it prints, and the steps.
  live?: { started: number; lines: string[] };
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
// Finding is one problem read out of the logs: what it is, what it means, and what to do.
export interface Finding {
  id: string;
  title: string;
  explain: string;
  fix?: string;
  severity: 'critical' | 'warning';
  line?: string; // the latest log line that shows it
  count: number;
  known: boolean; // a recognised cause, or a repeated error grouped by its shape
}

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
  cause?: Finding;
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
export type NotificationStatus = 'sent' | 'failed' | 'held' | 'digest' | 'muted' | 'kept' | 'throttled' | 'maintenance';
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
  findings?: Finding[];
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

// RemoteReadings are what an agent on another server reports (backend/internal/agent).
export interface RemoteReadings {
  version: number;
  agent: string;
  // attacks is the agent's own watch for attacks on its server (agents from this version on).
  attacks?: AttackView;
  time: number;
  system: HostInfo & { boot_time: number };
  cpu: { percent: number; cores: number[] | null; count: number; load: number[] | null; freq_mhz: number };
  memory: Memory;
  swap: Memory;
  temperature: number | null;
  fans: { name: string; label: string; speed_rpm: number }[];
  gpu: Stats['gpu'];
  disks: Partition[];
  network: { bytes_sent_per_sec: number; bytes_recv_per_sec: number };
  disk_io: { read_per_sec: number; write_per_sec: number };
  drives: Drive[];
  drives_error?: string;
}

// HostPoint is one poll; null while the server was offline. Agents fill cpu, mem and temp; checks fill latency.
export interface HostPoint {
  t: number;
  cpu: number | null;
  mem: number | null;
  temp: number | null;
}

// HostBody adds or changes a server (POST /api/hosts, PUT /api/hosts/{id}): its agent's address and
// token, or with kind "ssh", a server watched through its SSH login alone (renamed with just a name).
export type HostBody = { kind: 'agent'; name: string; address: string; token: string } | { kind: 'ssh'; name: string; ssh?: HostSSH };

// RemoteHost is another server the dashboard watches (GET /api/hosts).
export interface RemoteHost {
  id: string;
  name: string;
  address: string;
  added: number;
  // agent: readings from the agent on it; ssh: just its SSH login, whether its SSH port answers.
  kind: 'agent' | 'ssh';
  // ssh is how Nightjar logs in to it, when set up; ssh_command logs in the same way by hand.
  ssh?: HostSSH;
  ssh_command?: string;
  // ssh_check is how the last login with it went.
  ssh_check?: SSHCheck;
  // update is how keeping its agent on this dashboard's build is going.
  update?: AgentUpdate;
  status: 'waiting' | 'online' | 'offline';
  last_seen?: number;
  error?: string;
  readings: RemoteReadings | null;
  history: HostPoint[];
  problems: string[];
}

// HostSSH is a server's SSH login: a key on the monitor's machine (by path), a user and a port. An
// empty host connects to the server's own address.
export interface HostSSH {
  host?: string;
  port: number;
  user: string;
  key: string;
}

// SSHCheck is how the last login with a server's SSH login went: a test, or any action that worked.
export interface SSHCheck {
  ok: boolean;
  at: number;
  // as is who it logged in as, user@hostname, from a test.
  as?: string;
  error?: string;
  // host_key is the server's host key Nightjar trusts, as a type and fingerprint.
  host_key?: string;
}

// AgentUpdate: "current", "updating", "updated", "failed", or "manual" for an agent that needs one
// reinstall before it can update itself.
export interface AgentUpdate {
  state: 'current' | 'updating' | 'updated' | 'failed' | 'manual';
  detail?: string;
  at?: number;
}

// SSHKey is a key pair Nightjar can log in with (GET /api/ssh/keys); only its public half is sent.
export interface SSHKey {
  path: string;
  name: string;
  type: string;
  fingerprint: string;
  comment?: string;
  public: string;
  // own is a key Nightjar made, in its own folder.
  own: boolean;
}

export type SSHAction = 'test' | 'restart-agent' | 'reinstall-agent' | 'reboot';

// LoginTest is how trying a server's SSH login went, right after it was saved.
export interface LoginTest {
  ok: boolean;
  output?: string;
  error?: string;
}

// AgentBuild is an agent built into this monitor, to download (GET /api/agent/download?arch=).
export interface AgentBuild {
  arch: 'amd64' | 'arm64';
  file: string;
  size: number;
}

export interface HostsResponse {
  hosts: RemoteHost[];
  port: number;
  agents: AgentBuild[];
}

// AnalyticsPoint is one bucket of a server's stored minutes (GET /api/analytics): means, peaks, and
// up, the share of checks it answered. null had no reading.
export interface AnalyticsPoint {
  t: number;
  up: number;
  cpu: number | null;
  cpu_max: number | null;
  mem: number | null;
  mem_max: number | null;
  temp: number | null;
  temp_max: number | null;
  disk: number | null;
  rx: number | null;
  tx: number | null;
  latency: number | null;
  latency_max: number | null;
  minutes: number;
}

export interface AnalyticsSummary {
  coverage: number;
  uptime: number | null;
  downtime_s: number;
  incidents: number;
  cpu_avg: number | null;
  cpu_peak: number | null;
  mem_avg: number | null;
  mem_peak: number | null;
  temp_avg: number | null;
  temp_peak: number | null;
  disk_now: number | null;
  disk_change: number | null;
  disk_full_days: number | null;
  rx_bytes: number;
  tx_bytes: number;
  latency_avg: number | null;
  latency_peak: number | null;
}

export interface Outage {
  start: number;
  end: number;
  ongoing?: boolean;
}

export interface HostAnalytics {
  id: string; // "local" for this machine
  name: string;
  kind: 'local' | 'agent';
  status: string;
  points: AnalyticsPoint[];
  summary: AnalyticsSummary;
  incidents: Outage[];
}

export type AnalyticsRangeName = '1h' | '24h' | '7d' | '30d';

export interface AnalyticsResponse {
  range: { name: AnalyticsRangeName; label: string };
  ranges: { name: AnalyticsRangeName; label: string }[];
  step: number;
  hosts: HostAnalytics[];
  kept: boolean;
}

// Plugins are the owner's own checks, cards and alert channels, each a folder with a plugin.json.
export type PluginProvides = 'check' | 'card' | 'notify';

export interface PluginSetting {
  key: string;
  label: string;
  type?: 'text' | 'secret' | 'number' | 'bool' | 'select' | '';
  default?: string;
  help?: string;
  placeholder?: string;
  options?: string[];
  required?: boolean;
}

export interface PluginRun {
  mode: string;
  at: number;
  ok: boolean;
  output?: string;
  ms: number;
}

export interface Plugin {
  id: string;
  name: string;
  version: string;
  description: string;
  author?: string;
  homepage?: string;
  provides: PluginProvides[];
  run: string;
  interval?: number;
  timeout?: number;
  settings?: PluginSetting[];
  check?: { argument?: string; placeholder?: string };
  dir: string;
  problem?: string; // why it cannot run
  enabled: boolean;
  values: Record<string, string>; // settings, without secrets
  secrets_set: string[];
  missing?: string[]; // required settings still empty
  last?: PluginRun;
}

export interface PluginsResponse {
  dir: string;
  plugins: Plugin[];
  // restarting: a rescan found the plugins that deliver alerts changed, and Nightjar is restarting to set up their channels.
  restarting?: boolean;
}

export interface PluginCard {
  plugin: string;
  title: string;
  text?: string;
  items?: { label: string; value: string; tone?: 'good' | 'warn' | 'crit'; hint?: string }[];
  link?: string;
  updated: number;
  error?: string;
}

export interface PluginChange {
  enabled?: boolean;
  settings?: Record<string, string>;
  clear?: string[];
}

// ActivityEntry is one thing done: on the dashboard, through a Discord button, or by Nightjar.
export interface ActivityEntry {
  id: number;
  ts: number;
  who: string;
  kind: string;
  action: string;
  target?: string;
  detail?: string;
}

// AttackMethod is one way of attacking, as the detector names it.
export interface AttackMethod {
  id: string;
  name: string;
  how: string;
  unit: string;
}

// AttackFinding is one method seen now: its strength, what counts as an attack, and what is normal
// here. expected says what running on the machine explains it, so it is not counted.
export interface AttackFinding {
  method: string;
  value: number;
  threshold: number;
  normal: number;
  expected?: string;
}

// AttackSource is an address taking part; network names a proxy (Cloudflare) carrying many visitors.
export interface AttackSource {
  address: string;
  established: number;
  syn_recv: number;
  requests: number;
  network?: string;
  // ssh is an address logged in over SSH, never limited or blocked.
  ssh?: boolean;
}

// AttackKind is an exact type of flood ("DNS amplification", "Slowloris"), from sampled packet headers
// and the web log, with its share of the traffic sampled.
export interface AttackKind {
  id: string;
  name: string;
  share: number;
  detail?: string;
  how: string;
}

// Attack is one attack, from start to end (end 0 while it goes on).
export interface Attack {
  id: string;
  start: number;
  end?: number;
  methods: AttackFinding[];
  kinds?: AttackKind[];
  peak_rx_bits: number;
  peak_rx_packets: number;
  sources: AttackSource[];
}

// AttackView is the attack watch (GET /api/attacks, and an agent's readings.attacks).
export interface AttackView {
  attacking: boolean;
  current?: Attack;
  findings: AttackFinding[];
  busy: { installing?: string; torrents?: string };
  rx_bits: number;
  rx_packets: number;
  learning: boolean;
  history: Attack[];
  methods?: AttackMethod[];
  // defense is what is being done about attacks, where the watch defends.
  defense?: AttackDefense;
}

// AttackDefense is the firewall's answer to an attack: rules suited to it in iptables chains of
// Nightjar's own, and the addresses it blocked. on is the switch; problem says why it cannot defend
// here; cannot says what it cannot do about the attack under way.
export interface AttackDefense {
  on: boolean;
  problem?: string;
  active: boolean;
  measures: { id: string; name: string; how: string }[];
  blocked: AttackBlock[];
  cannot?: string[];
  error?: string;
  // web is whether addresses coming through Cloudflare can be blocked in nginx.
  web: boolean;
}

// AttackBlock is an address cut off until a time, in the firewall or (behind a proxy) in nginx.
export interface AttackBlock {
  address: string;
  until: number;
  why: string;
  where: 'firewall' | 'web';
}

// FirewallRule is a rule set from the dashboard: allow, block or limit traffic to a port, ports or
// range (none for all) over tcp, udp or both, from an address or network (none for anyone); for an
// outgoing rule, to it. Rules are checked in order and the first that matches decides. A limit's rate
// is for each address: "6/min", "20/sec", "100/hour".
export interface FirewallRule {
  id: string;
  action: 'allow' | 'block' | 'limit';
  direction?: 'out';
  source?: string;
  port?: string;
  proto?: 'tcp' | 'udp';
  rate?: string;
  note?: string;
  paused?: boolean;
  added: number;
}

// FirewallPort is a port something listens on; local ones are open on loopback only. allowed: anyone
// may reach it whatever the default (a rule allows everyone, or it is kept open).
export interface FirewallPort {
  port: number;
  proto: 'tcp' | 'udp';
  program?: string;
  local?: boolean;
  allowed?: boolean;
}

// FirewallDrop is one packet the firewall dropped, from the kernel log: rule is the rule's id, or
// "default" for incoming traffic no rule allowed.
export interface FirewallDrop {
  at: number;
  rule: string;
  direction?: 'out';
  source: string;
  dest?: string;
  proto?: string;
  port?: number;
}

// FirewallStatus is a machine's firewall (GET /api/firewall, GET /api/hosts/{id}/firewall): which it
// runs and what its INPUT does by default; what happens here to incoming traffic no rule decides
// (default: '' leaves it to the machine's firewall, 'drop' drops it, with pending until it is kept);
// the ports kept open whatever the default; the rules in order and what each caught since `since`;
// the latest dropped packets; and the machine's own rules. full is false for an agent from before all
// this, which has rules and open ports only.
export interface FirewallStatus {
  full?: boolean;
  tool: 'ufw' | 'firewalld' | 'iptables';
  policy?: string;
  default?: '' | 'drop';
  pending?: { default: '' | 'drop'; until: number };
  open?: { port: number; what: string }[];
  rules: FirewallRule[];
  listening: FirewallPort[];
  hits?: Record<string, { packets: number; bytes: number }>;
  since?: number;
  drops?: FirewallDrop[];
  drops_note?: string;
  native?: string;
  problem?: string;
}

// AgentFirewall is what an agent says of its firewall and defense.
export interface AgentFirewall {
  firewall: FirewallStatus;
  defense?: AttackDefense;
}
