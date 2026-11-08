package config

import (
	"slices"
	"strings"

	"nightjar/internal/logx"
)

var log = logx.Component("Config")

const (
	BytesPerMB = 1024 * 1024
	BytesPerGB = 1024 * 1024 * 1024

	DefaultHistoryDB = "nightjar.db"
)

// Settings is every tunable the monitor reads, loaded and clamped once at startup.
// Environment variable names are stable across versions so an existing .env keeps working.
type Settings struct {
	WebUIEnabled     bool
	WebPort          int
	WebHost          string
	WebAuthToken     string
	WebAllowedOrigin string
	FrontendDir      string

	MediaCleanup            bool
	MediaCleanupDays        int
	MediaCleanupNoticeDays  int
	MediaCleanupRatio       float64
	MediaCleanupRatioDays   int
	MediaCleanupLibraries   []string
	MediaCleanupDiskFull    int
	SonarrURL, SonarrAPIKey string
	RadarrURL, RadarrAPIKey string
	WeeklyReport            bool
	WeeklyReportAt          string
	StatusPageEnabled       bool
	StatusPageTitle         string
	StatusPageDomain        string // a host name of its own, showing only the status page
	StatusPagePath          string // where it is on the dashboard's own address
	HiddenSections          []string

	WebhookURL            string
	DiskWebhookURL        string
	CrashWebhookURL       string
	QBittorrentWebhookURL string
	PlexWebhookURL        string
	AlertLogPath          string
	DashboardURL          string

	ChannelsMinSeverity string
	NtfyURL             string
	NtfyToken           string
	TelegramBotToken    string
	TelegramChatID      string
	SlackWebhookURL     string
	DiscordBotToken     string
	DiscordChannelID    string
	DiscordPublicKey    string
	DiscordAllowedUsers []string
	AlertWebhookURL     string
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPFrom            string
	SMTPTo              []string

	MetricsReportInterval float64

	CPUThreshold                   float64
	MemoryThreshold                float64
	GPUThreshold                   float64
	DiskThreshold                  float64
	DiskEarlyWarningThreshold      float64
	DiskCleanupThreshold           float64
	DiskAggressiveCleanupThreshold float64
	DiskEmergencyThreshold         float64
	MonitorIndividualPartitions    bool
	AllowUnsafeCleanupPaths        bool
	TempDirs                       []string
	CacheDirs                      []string
	EmergencyCacheDirs             []string
	TempThreshold                  float64
	TempAlertDuration              float64
	NetworkBandwidthThreshold      float64
	ProcessCPUThreshold            float64
	ProcessMemoryThreshold         float64

	CheckInterval        float64
	AlertCooldown        float64
	AlertMaxCooldown     float64
	AlertBackoff         float64
	AlertRecoverySeconds float64

	MonitoringWatchdogTimeout float64
	WatchdogAutoRestart       bool
	WatchdogRestartCooldown   float64
	SystemdServiceName        string

	FreezeMonitorEnabled   bool
	FreezeThreshold        float64
	FreezeHeartbeatTimeout float64

	MediaMonitorEnabled          bool
	MediaDirectory               string
	MediaDirectoryThresholdGB    float64
	MediaDirectoryThresholdBytes int64
	MediaCheckInterval           int

	LogThresholdMB          float64
	LogCheckInterval        int
	RotatedLogRetentionDays float64
	LogServicesToRestart    []string
	ManageNginxLogs         bool
	NginxLogDir             string
	NginxUserGroup          string
	NginxServiceName        string

	KernelMonitorEnabled         bool
	KernelCheckInterval          int
	KernelStabilityCheckInterval int
	KernelCheckSinceMinutes      int
	KernelIgnoreDevices          []string
	KernelTrackingFile           string

	SystemUpdateEnabled         bool
	SystemUpdateIntervalSeconds float64
	SystemUpdateTimeout         int
	ServiceWatchEnabled         bool
	ServiceWatchInterval        float64
	ServiceWatchIgnore          []string
	ServiceRestartAlert         int
	SmartMonitorEnabled         bool
	SmartCheckInterval          float64
	SmartHDDTempLimit           int
	SmartSSDTempLimit           int

	UpdatesWindowStart int
	UpdatesWindowEnd   int
	UpdatesNotify      bool
	GitHubToken        string

	QBittorrentMonitor  bool
	QBittorrentHost     string
	QBittorrentPort     int
	QBittorrentUsername string
	QBittorrentPassword string

	StalledThreshold           float64
	StalledActionCooldown      float64
	StalledRecoveryGrace       float64
	StalledAutoHandle          bool
	StalledReplace             bool
	StalledRetryBackoff        float64
	StalledMaxRecoveryAttempts int
	SeedingRatioLimit          float64
	SeedingRatioActionCooldown float64
	SeedingRatioRetryBackoff   float64
	SeedingRatioDeleteFiles    bool
	InactiveTorrentAutoRemove  bool

	PlexMonitor        bool
	PlexURL            string
	PlexToken          string
	PlexVerifyTLS      bool
	PlexCheckInterval  float64
	PlexRetries        int
	PlexNotifyStreams  bool
	PlexTranscodeAlert int

	ServicesFile         string
	PluginsDir           string
	RepairsDir           string
	SSHDir               string
	AgentAutoUpdate      bool
	DockerSocket         string
	HistoryDB            string
	HistoryRetentionDays int

	LogLevel       string
	LogFormat      string
	LogFile        string
	LogMaxBytes    int
	LogBackupCount int
	ErrorLogFile   string
}

// Fields declares every setting, in the order the dashboard shows them, bound to s.
func (s *Settings) Fields() []Field {
	fields := s.declare()
	for i := range fields {
		fields[i].Live = liveKeys[fields[i].Key]
		fields[i].Advanced = !essentialKeys[fields[i].Key]
	}
	return fields
}

// liveKeys are read by the monitoring loop on every cycle, never baked into a client, webhook or
// timer at startup, so changing them needs no restart.
var liveKeys = keySet(
	"CHECK_INTERVAL", "HISTORY_RETENTION_DAYS", "METRICS_REPORT_INTERVAL",
	"CPU_THRESHOLD", "MEMORY_THRESHOLD", "GPU_THRESHOLD", "TEMP_THRESHOLD", "TEMP_ALERT_DURATION", "NETWORK_BANDWIDTH_THRESHOLD",
	"PROCESS_CPU_THRESHOLD", "PROCESS_MEMORY_THRESHOLD",
	"DISK_THRESHOLD", "DISK_EARLY_WARNING_THRESHOLD", "DISK_CLEANUP_THRESHOLD", "DISK_EMERGENCY_THRESHOLD", "MONITOR_INDIVIDUAL_PARTITIONS",
	"MEDIA_MONITOR_ENABLED", "MEDIA_DIRECTORY", "MEDIA_DIRECTORY_THRESHOLD_GB", "MEDIA_CHECK_INTERVAL",
	"LOG_CHECK_INTERVAL", "KERNEL_CHECK_INTERVAL", "KERNEL_STABILITY_CHECK_INTERVAL", "KERNEL_CHECK_SINCE_MINUTES",
	"SYSTEM_UPDATE_ENABLED", "SYSTEM_UPDATE_INTERVAL_SECONDS", "UPDATES_WINDOW_START", "UPDATES_WINDOW_END",
	"STALLED_TORRENT_THRESHOLD", "STALLED_TORRENT_ACTION_COOLDOWN", "STALLED_TORRENT_RECOVERY_GRACE", "STALLED_TORRENT_AUTO_HANDLE",
	"STALLED_TORRENT_RETRY_BACKOFF", "STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS",
	"SEEDING_RATIO_LIMIT", "SEEDING_RATIO_ACTION_COOLDOWN", "SEEDING_RATIO_RETRY_BACKOFF", "SEEDING_RATIO_DELETE_FILES", "INACTIVE_TORRENT_AUTO_REMOVE",
	"PLEX_NOTIFY_STREAMS", "PLEX_TRANSCODE_ALERT",
	"STATUS_PAGE_ENABLED", "STATUS_PAGE_TITLE", "STATUS_PAGE_DOMAIN", "STATUS_PAGE_PATH", "LOG_LEVEL", "DASHBOARD_HIDDEN",
	"SERVICE_WATCH_ENABLED", "SERVICE_WATCH_INTERVAL", "SERVICE_WATCH_IGNORE", "SERVICE_RESTART_ALERT",
	"SMART_MONITOR_ENABLED", "SMART_CHECK_INTERVAL", "SMART_HDD_TEMP_LIMIT", "SMART_SSD_TEMP_LIMIT",
)

// essentialKeys are what most people set; the dashboard shows the rest under "advanced".
var essentialKeys = keySet(
	"CHECK_INTERVAL", "ALERT_COOLDOWN", "HISTORY_RETENTION_DAYS",
	"WEBHOOK", "DASHBOARD_URL",
	"ALERT_CHANNELS_MIN_SEVERITY", "NTFY_URL", "NTFY_TOKEN", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "SLACK_WEBHOOK", "ALERT_WEBHOOK_URL",
	"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM", "SMTP_TO",
	"CPU_THRESHOLD", "MEMORY_THRESHOLD", "TEMP_THRESHOLD",
	"DISK_THRESHOLD", "DISK_CLEANUP_THRESHOLD",
	"MEDIA_MONITOR_ENABLED", "MEDIA_DIRECTORY",
	"KERNEL_MONITOR_ENABLED", "WATCHDOG_AUTO_RESTART",
	"SYSTEM_UPDATE_ENABLED", "UPDATES_WINDOW_START", "UPDATES_WINDOW_END", "UPDATES_NOTIFY",
	"QBITTORRENT_MONITOR", "QBITTORRENT_HOST", "QBITTORRENT_PORT", "QBITTORRENT_USERNAME", "QBITTORRENT_PASSWORD",
	"STALLED_TORRENT_AUTO_HANDLE", "SEEDING_RATIO_LIMIT",
	"PLEX_MONITOR", "PLEX_URL", "PLEX_TOKEN", "PLEX_NOTIFY_STREAMS",
	"STATUS_PAGE_ENABLED", "STATUS_PAGE_TITLE", "LOG_LEVEL",
	"SERVICE_WATCH_ENABLED", "SERVICE_WATCH_IGNORE", "SMART_MONITOR_ENABLED",
)

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func (s *Settings) declare() []Field {
	return slices.Concat(
		group("Monitoring",
			number(&s.CheckInterval, "CHECK_INTERVAL", 5, 0.1, 3600).describe("Check interval", "s", "Seconds between monitoring cycles."),
			number(&s.AlertCooldown, "ALERT_COOLDOWN", 300, 0).describe("Alert cooldown", "s", "Before the same alert repeats. Grows while the condition persists."),
			number(&s.AlertMaxCooldown, "ALERT_MAX_COOLDOWN", 3600, 0).describe("Longest cooldown", "s", "Cap for the growing cooldown."),
			number(&s.AlertBackoff, "ALERT_BACKOFF", 2, 1, 10).describe("Cooldown growth", "×", "Factor the cooldown grows by on each repeat."),
			number(&s.AlertRecoverySeconds, "ALERT_RECOVERY_SECONDS", 300, 0).describe("Recovery period", "s", "How long a condition must stay clear before its cooldown resets."),
			number(&s.MetricsReportInterval, "METRICS_REPORT_INTERVAL", 3600, 60).describe("Metrics report every", "s", "The monitor's own health report to Discord."),
			integer(&s.HistoryRetentionDays, "HISTORY_RETENTION_DAYS", 90, 1, 3650).describe("Keep check history", "days", ""),
		),
		group("Notifications",
			secret(&s.WebhookURL, "WEBHOOK").describe("Discord webhook", "", "Where alerts go. The webhooks below split alerts out; empty ones fall back to this one."),
			secret(&s.DiskWebhookURL, "DISK_WEBHOOK").describe("Disk webhook", "", "Disk usage, cleanup and media directory alerts."),
			secret(&s.CrashWebhookURL, "CRASH_WEBHOOK").describe("Kernel webhook", "", "Kernel crashes, hardware errors and monitor errors."),
			secret(&s.QBittorrentWebhookURL, "QBITTORRENT_WEBHOOK").describe("qBittorrent webhook", "", ""),
			secret(&s.PlexWebhookURL, "PLEX_WEBHOOK").describe("Plex webhook", "", ""),
			text(&s.AlertLogPath, "ALERT_LOG_PATH", "/var/log/nightjar-alerts.log").describe("Fallback alert log", "", "Alerts that cannot reach Discord are written here."),
			text(&s.DashboardURL, "DASHBOARD_URL", "").describe("Dashboard address", "", "Where alert titles link to, e.g. https://monitor.example.com. Empty means no link."),
		),
		group("Alert channels",
			choice(&s.ChannelsMinSeverity, "ALERT_CHANNELS_MIN_SEVERITY", "INFO", []string{"INFO", "WARNING", "CRITICAL"}, nil).describe("Send to these channels from", "", "The least severe alert the channels below receive. Recoveries always follow their outage."),
			text(&s.NtfyURL, "NTFY_URL", "").describe("ntfy topic", "", "Push to phones with ntfy, e.g. https://ntfy.sh/your-topic."),
			secret(&s.NtfyToken, "NTFY_TOKEN").describe("ntfy access token", "", "For protected topics."),
			secret(&s.TelegramBotToken, "TELEGRAM_BOT_TOKEN").describe("Telegram bot token", "", "From @BotFather."),
			text(&s.TelegramChatID, "TELEGRAM_CHAT_ID", "").describe("Telegram chat ID", "", "A user, group or channel the bot may post to."),
			secret(&s.SlackWebhookURL, "SLACK_WEBHOOK").describe("Slack webhook", "", "An incoming webhook URL."),
			secret(&s.DiscordBotToken, "DISCORD_BOT_TOKEN").describe("Discord bot token", "", "A bot posts alerts with buttons to act on them. Use it instead of the Discord webhook."),
			text(&s.DiscordChannelID, "DISCORD_CHANNEL_ID", "").describe("Bot's channel id", "", "Right-click the channel in Discord (with Developer Mode on) and copy its id."),
			text(&s.DiscordPublicKey, "DISCORD_PUBLIC_KEY", "").describe("Bot's public key", "", "From the application's General Information page. Set its Interactions Endpoint URL to this dashboard's /discord/interactions."),
			list(&s.DiscordAllowedUsers, "DISCORD_ALLOWED_USERS", "").describe("Who may press the buttons", "", "Discord user ids, separated by commas."),
			secret(&s.AlertWebhookURL, "ALERT_WEBHOOK_URL").describe("JSON webhook", "", "Each alert is POSTed as JSON (title, severity, fields…) for home automation or your own tools."),
			text(&s.SMTPHost, "SMTP_HOST", "").describe("Email server", "", "SMTP host. Port 465 uses TLS; others use STARTTLS when offered."),
			integer(&s.SMTPPort, "SMTP_PORT", 587, 1, 65535).describe("Email port", "", ""),
			text(&s.SMTPUsername, "SMTP_USERNAME", "").describe("Email username", "", ""),
			secret(&s.SMTPPassword, "SMTP_PASSWORD").describe("Email password", "", ""),
			text(&s.SMTPFrom, "SMTP_FROM", "").describe("Email from", "", ""),
			list(&s.SMTPTo, "SMTP_TO", "").describe("Email to", "", "Comma-separated addresses."),
		),
		group("Resources",
			number(&s.CPUThreshold, "CPU_THRESHOLD", 80, 0, 100).describe("CPU", "%", "Alert when average CPU usage reaches this."),
			number(&s.MemoryThreshold, "MEMORY_THRESHOLD", 80, 0, 100).describe("Memory", "%", ""),
			number(&s.GPUThreshold, "GPU_THRESHOLD", 80, 0, 100).describe("GPU", "%", "NVIDIA GPUs, read with nvidia-smi."),
			number(&s.TempThreshold, "TEMP_THRESHOLD", 80, 0).describe("CPU temperature", "°C", ""),
			number(&s.TempAlertDuration, "TEMP_ALERT_DURATION", 30, 0).describe("Temperature duration", "s", "How long it must stay high before alerting."),
			number(&s.NetworkBandwidthThreshold, "NETWORK_BANDWIDTH_THRESHOLD", 1000, 0).describe("Network bandwidth", "MB/s", "Upload plus download."),
			number(&s.ProcessCPUThreshold, "PROCESS_CPU_THRESHOLD", 50, 0, 100).describe("Single process CPU", "%", ""),
			number(&s.ProcessMemoryThreshold, "PROCESS_MEMORY_THRESHOLD", 10, 0, 100).describe("Single process memory", "%", ""),
		),
		group("Disks",
			number(&s.DiskThreshold, "DISK_THRESHOLD", 80, 0, 100).describe("Usage alert", "%", ""),
			number(&s.DiskEarlyWarningThreshold, "DISK_EARLY_WARNING_THRESHOLD", 75, 0, 100).describe("Early warning", "%", "Kept below the usage alert."),
			number(&s.DiskCleanupThreshold, "DISK_CLEANUP_THRESHOLD", 85, 0, 100).describe("Start cleanup", "%", "Removes old temp files, caches and rotated logs."),
			number(&s.DiskAggressiveCleanupThreshold, "DISK_AGGRESSIVE_CLEANUP_THRESHOLD", 92, 0, 100).describe("Aggressive cleanup", "%", "Also empties logs and package caches."),
			number(&s.DiskEmergencyThreshold, "DISK_EMERGENCY_THRESHOLD", 95, 90, 99).describe("Emergency cleanup", "%", ""),
			boolean(&s.MonitorIndividualPartitions, "MONITOR_INDIVIDUAL_PARTITIONS", true).describe("Check each partition", "", "Otherwise only the total across disks."),
			list(&s.TempDirs, "TEMP_DIRS", "").describe("Temp directories", "", "Cleaned of old files. Empty means /tmp and /var/tmp; entries must be inside those."),
			list(&s.CacheDirs, "CACHE_DIRS", "").describe("Cache paths", "", "Cleaned in routine cleanup. Empty means apt's caches; entries must be inside /var/cache."),
			list(&s.EmergencyCacheDirs, "EMERGENCY_CACHE_DIRS", "").describe("Emergency cache paths", "", "Empty means package-manager caches and temp directories."),
			boolean(&s.AllowUnsafeCleanupPaths, "ALLOW_UNSAFE_CLEANUP_PATHS", false).describe("Allow unsafe cleanup paths", "", "Lets the paths above point anywhere.").startup(),
		),
		group("Logs",
			number(&s.LogThresholdMB, "LOG_THRESHOLD_MB", 100, 1).describe("Trim logs above", "MB", ""),
			integer(&s.LogCheckInterval, "LOG_CHECK_INTERVAL", 20, 1).describe("Check logs every", "cycles", "Every 5 cycles while the disk is past the cleanup threshold."),
			number(&s.RotatedLogRetentionDays, "ROTATED_LOG_RETENTION_DAYS", 7, 0).describe("Keep rotated logs", "days", ""),
			list(&s.LogServicesToRestart, "LOG_SERVICES_TO_RESTART", "rsyslog").describe("Restart after trimming", "", "systemd services that must reopen their logs."),
			boolean(&s.ManageNginxLogs, "MANAGE_NGINX_LOGS", false).describe("Manage nginx logs", "", ""),
			text(&s.NginxLogDir, "NGINX_LOG_DIR", "/var/log/nginx").describe("nginx log directory", "", ""),
			text(&s.NginxUserGroup, "NGINX_USER_GROUP", "www-data:adm").describe("nginx log owner", "", "user:group for recreated log files."),
			text(&s.NginxServiceName, "NGINX_SERVICE_NAME", "nginx").describe("nginx service", "", ""),
		),
		group("Media directory",
			boolean(&s.MediaMonitorEnabled, "MEDIA_MONITOR_ENABLED", false).describe("Watch a media directory", "", ""),
			text(&s.MediaDirectory, "MEDIA_DIRECTORY", "/home/media").describe("Directory", "", ""),
			number(&s.MediaDirectoryThresholdGB, "MEDIA_DIRECTORY_THRESHOLD_GB", 500, 0).describe("Alert above", "GB", ""),
			integer(&s.MediaCheckInterval, "MEDIA_CHECK_INTERVAL", 10, 1).describe("Check every", "cycles", "Sizing a large tree is slow."),
		),
		group("Kernel",
			boolean(&s.KernelMonitorEnabled, "KERNEL_MONITOR_ENABLED", true).describe("Watch the kernel", "", "Crashes, hardware errors, unexpected reboots and stability."),
			integer(&s.KernelCheckInterval, "KERNEL_CHECK_INTERVAL", 30, 1).describe("Crash check every", "cycles", ""),
			integer(&s.KernelStabilityCheckInterval, "KERNEL_STABILITY_CHECK_INTERVAL", 10, 1).describe("Stability check every", "cycles", ""),
			integer(&s.KernelCheckSinceMinutes, "KERNEL_CHECK_SINCE_MINUTES", 60, 1).describe("Look back", "min", "How far back each crash check reads the logs."),
			list(&s.KernelIgnoreDevices, "KERNEL_IGNORE_DEVICES", "").describe("Ignore disk errors on", "", "Disks (e.g. sdb) whose I/O error counters to ignore."),
			text(&s.KernelTrackingFile, "KERNEL_TRACKING_FILE", "").describe("Tracking file", "", "Remembers boot time and error counts. Empty means ~/.nightjar_kernel_tracking.json.").startup(),
		),
		group("Watchdog",
			number(&s.MonitoringWatchdogTimeout, "MONITORING_WATCHDOG_TIMEOUT", 30, 5).describe("Loop stall timeout", "s", "Alert when a monitoring cycle stops for this long."),
			boolean(&s.WatchdogAutoRestart, "WATCHDOG_AUTO_RESTART", false).describe("Restart a stalled monitor", "", "Restarts the systemd service."),
			number(&s.WatchdogRestartCooldown, "WATCHDOG_RESTART_COOLDOWN", 300, 0).describe("Restart cooldown", "s", ""),
			text(&s.SystemdServiceName, "SYSTEMD_SERVICE_NAME", "nightjar").describe("systemd service", "", "").startup(),
			boolean(&s.FreezeMonitorEnabled, "FREEZE_MONITOR_ENABLED", false).describe("Detect host freezes", "", ""),
			number(&s.FreezeThreshold, "FREEZE_THRESHOLD", 30, 10).describe("Frozen metrics after", "s", ""),
			number(&s.FreezeHeartbeatTimeout, "FREEZE_HEARTBEAT_TIMEOUT", 60, 20).describe("Missed heartbeat after", "s", ""),
		),
		group("System services",
			boolean(&s.ServiceWatchEnabled, "SERVICE_WATCH_ENABLED", true).describe("Watch systemd services", "", "Alert when a service fails, keeps restarting, or comes back."),
			list(&s.ServiceWatchIgnore, "SERVICE_WATCH_IGNORE", "").describe("Never alert on", "", "Service names or patterns, e.g. apt-daily, certbot*."),
			integer(&s.ServiceRestartAlert, "SERVICE_RESTART_ALERT", 3, 0, 100).describe("Restart loop after", "restarts", "Restarts within ten minutes; 0 turns the alert off."),
			number(&s.ServiceWatchInterval, "SERVICE_WATCH_INTERVAL", 30, 10, 3600).describe("Check every", "s", ""),
		),
		group("Drive health",
			boolean(&s.SmartMonitorEnabled, "SMART_MONITOR_ENABLED", true).describe("Watch drive health", "", "Reads SMART with smartctl; sleeping disks are not woken."),
			number(&s.SmartCheckInterval, "SMART_CHECK_INTERVAL", 1800, 300, 86400).describe("Check every", "s", ""),
			integer(&s.SmartHDDTempLimit, "SMART_HDD_TEMP_LIMIT", 55, 30, 90).describe("Hard disk too hot at", "°C", ""),
			integer(&s.SmartSSDTempLimit, "SMART_SSD_TEMP_LIMIT", 70, 30, 100).describe("SSD too hot at", "°C", ""),
		),
		group("Updates",
			boolean(&s.SystemUpdateEnabled, "SYSTEM_UPDATE_ENABLED", false).describe("Check for updates", "", "OS packages, snaps and apps, listed on the Updates page."),
			number(&s.SystemUpdateIntervalSeconds, "SYSTEM_UPDATE_INTERVAL_SECONDS", 21600, 300, 2592000).describe("Check every", "s", ""),
			integer(&s.UpdatesWindowStart, "UPDATES_WINDOW_START", 4, 0, 23).describe("Auto-update from", "h", "Hour (local time) automatic updates may start; the same start and end means any time."),
			integer(&s.UpdatesWindowEnd, "UPDATES_WINDOW_END", 6, 0, 23).describe("Auto-update until", "h", ""),
			integer(&s.SystemUpdateTimeout, "SYSTEM_UPDATE_TIMEOUT", 3600, 60, 86400).describe("Longest update", "s", ""),
			boolean(&s.UpdatesNotify, "UPDATES_NOTIFY", true).describe("Announce updates", "", "Alert when updates wait for you; automatic ones alert when installed."),
			boolean(&s.AgentAutoUpdate, "AGENT_AUTO_UPDATE", true).describe("Update agents", "", "Keep the agents on other servers on this dashboard's version, over their HTTPS."),
			secret(&s.GitHubToken, "GITHUB_TOKEN").describe("GitHub token", "", "Optional. Raises GitHub's limit of 60 release checks an hour."),
		),
		group("qBittorrent",
			boolean(&s.QBittorrentMonitor, "QBITTORRENT_MONITOR", false).describe("Watch qBittorrent", "", "Stalled downloads, finished torrents and the cleanup policy."),
			text(&s.QBittorrentHost, "QBITTORRENT_HOST", "localhost").describe("Host", "", "Host name or URL of the Web UI."),
			integer(&s.QBittorrentPort, "QBITTORRENT_PORT", 8080, 1, 65535).describe("Port", "", ""),
			text(&s.QBittorrentUsername, "QBITTORRENT_USERNAME", "admin").describe("Username", "", ""),
			secret(&s.QBittorrentPassword, "QBITTORRENT_PASSWORD").describe("Password", "", ""),
			number(&s.StalledThreshold, "STALLED_TORRENT_THRESHOLD", 300, 30, 86400).describe("Stalled after", "s", ""),
			boolean(&s.StalledAutoHandle, "STALLED_TORRENT_AUTO_HANDLE", true).describe("Recover stalled torrents", "", "Reannounces and resumes them."),
			integer(&s.StalledMaxRecoveryAttempts, "STALLED_TORRENT_MAX_RECOVERY_ATTEMPTS", 2, 1, 20).describe("Recovery attempts", "", ""),
			boolean(&s.StalledReplace, "STALLED_TORRENT_REPLACE", true).describe("Then replace it", "", "When recovery does not help, Sonarr or Radarr blocklist the release and search for another."),
			number(&s.StalledActionCooldown, "STALLED_TORRENT_ACTION_COOLDOWN", 1800, 60, 86400).describe("Between attempts", "s", ""),
			number(&s.StalledRetryBackoff, "STALLED_TORRENT_RETRY_BACKOFF", 60, 5, 3600).describe("Retry a failed attempt after", "s", ""),
			number(&s.StalledRecoveryGrace, "STALLED_TORRENT_RECOVERY_GRACE", 120, 0, 3600).describe("Recovered after moving for", "s", ""),
			number(&s.SeedingRatioLimit, "SEEDING_RATIO_LIMIT", 0, 0, 100).describe("Remove at seed ratio", "", "0 keeps seeding forever."),
			boolean(&s.SeedingRatioDeleteFiles, "SEEDING_RATIO_DELETE_FILES", false).describe("Delete files with it", "", ""),
			number(&s.SeedingRatioActionCooldown, "SEEDING_RATIO_ACTION_COOLDOWN", 1800, 60, 86400).describe("Between removals", "s", ""),
			number(&s.SeedingRatioRetryBackoff, "SEEDING_RATIO_RETRY_BACKOFF", 60, 5, 3600).describe("Retry a failed removal after", "s", ""),
			boolean(&s.InactiveTorrentAutoRemove, "INACTIVE_TORRENT_AUTO_REMOVE", false).describe("Remove paused torrents that finished downloading", "", "Their files are kept. Stopped or paused downloads are left alone."),
		),
		group("Plex",
			boolean(&s.PlexMonitor, "PLEX_MONITOR", false).describe("Watch Plex", "", "Server state, streams, transcodes and libraries."),
			text(&s.PlexURL, "PLEX_URL", "http://localhost:32400").describe("Server URL", "", ""),
			secret(&s.PlexToken, "PLEX_TOKEN").describe("Token", "", "X-Plex-Token of a server admin."),
			boolean(&s.PlexVerifyTLS, "PLEX_VERIFY_TLS", true).describe("Verify TLS", "", ""),
			number(&s.PlexCheckInterval, "PLEX_CHECK_INTERVAL", 15, 5, 3600).describe("Check every", "s", ""),
			integer(&s.PlexRetries, "PLEX_RETRIES", 2, 0, 20).describe("Retries before down", "", ""),
			boolean(&s.PlexNotifyStreams, "PLEX_NOTIFY_STREAMS", false).describe("Announce new streams", "", ""),
			integer(&s.PlexTranscodeAlert, "PLEX_TRANSCODE_ALERT", 0, 0, 100).describe("Alert at transcodes", "", "0 turns it off."),
		),
		group("Media cleanup",
			boolean(&s.MediaCleanup, "MEDIA_CLEANUP", false).describe("Remove what no one watches", "", "Movies and shows nobody has ever played go on a Leaving soon list, then are removed through Sonarr or Radarr, and their torrents once seeded."),
			integer(&s.MediaCleanupDays, "MEDIA_CLEANUP_DAYS", 60, 7, 3650).describe("Never watched for", "days", "Counted from when Plex added it."),
			integer(&s.MediaCleanupNoticeDays, "MEDIA_CLEANUP_NOTICE_DAYS", 7, 1, 90).describe("Leaving soon for", "days", "How long it is listed, and Discord told, before it goes. Mark it Keep to spare it."),
			number(&s.MediaCleanupRatio, "MEDIA_CLEANUP_RATIO", 1.0, 0, 100).describe("Seed torrents to ratio", "", "Its torrents are removed once they reach it; 0 removes them at once."),
			integer(&s.MediaCleanupRatioDays, "MEDIA_CLEANUP_RATIO_DAYS", 30, 0, 3650).describe("Give up seeding after", "days", "Then it is removed even below the ratio."),
			integer(&s.MediaCleanupDiskFull, "MEDIA_CLEANUP_DISK_FULL", 90, 0, 100).describe("Hurry when the disk is", "%", "Past this, what is leaving goes after a day instead of the week, biggest first. 0 never hurries."),
			list(&s.MediaCleanupLibraries, "MEDIA_CLEANUP_LIBRARIES", "").describe("Only these libraries", "", "Library names, separated by commas. Empty takes every movie and show library."),
			text(&s.SonarrURL, "SONARR_URL", "").describe("Sonarr URL", "", "Such as http://localhost:8989. Shows are removed through it."),
			secret(&s.SonarrAPIKey, "SONARR_API_KEY").describe("Sonarr API key", "", "Settings → General in Sonarr."),
			text(&s.RadarrURL, "RADARR_URL", "").describe("Radarr URL", "", "Such as http://localhost:7878. Movies are removed through it."),
			secret(&s.RadarrAPIKey, "RADARR_API_KEY").describe("Radarr API key", "", "Settings → General in Radarr."),
		),
		group("Dashboard",
			boolean(&s.WeeklyReport, "WEEKLY_REPORT", true).describe("Weekly report", "", "A summary of the week in Discord: uptime, incidents and their causes, repairs, and what is coming."),
			text(&s.WeeklyReportAt, "WEEKLY_REPORT_AT", "Mon 09:00").describe("Send it", "", `A day and a time, such as "Mon 09:00".`),
			boolean(&s.StatusPageEnabled, "STATUS_PAGE_ENABLED", false).describe("Public status page", "", "Names, status and uptime at /status, without the token."),
			text(&s.StatusPageTitle, "STATUS_PAGE_TITLE", "Service Status").describe("Status page title", "", ""),
			hostName(&s.StatusPageDomain, "STATUS_PAGE_DOMAIN").describe("Status page domain", "", "A domain of its own, such as status.example.com: there it is the whole site, and the dashboard is never shown. Point the domain and your reverse proxy at this machine (Settings → Status page can set up nginx)."),
			pagePath(&s.StatusPagePath, "STATUS_PAGE_PATH", "/status").describe("Status page path", "", "Where it is on the dashboard's own address, such as /uptime. /status always works too."),
			list(&s.HiddenSections, "DASHBOARD_HIDDEN", "").describe("Hidden sections", "", "Dashboard areas turned off under Settings → Sections. Monitoring carries on; only the dashboard hides them."),
			boolean(&s.WebUIEnabled, "WEB_UI_ENABLED", true).describe("Dashboard", "", "").startup(),
			text(&s.WebHost, "WEB_HOST", "127.0.0.1").describe("Listen address", "", "Every address needs the admin account to sign in; 127.0.0.1 keeps the dashboard to this machine and its reverse proxy.").startup(),
			integer(&s.WebPort, "WEB_PORT", 8080, 1, 65535).describe("Port", "", "").startup(),
			secret(&s.WebAuthToken, "WEB_AUTH_TOKEN").atLeast(MinAuthTokenLength).describe("API token", "", "Lets scripts use the API without signing in, as a Bearer token, at least 16 characters (openssl rand -hex 24 makes one). People sign in with the admin account.").startup(),
			text(&s.WebAllowedOrigin, "WEB_ALLOWED_ORIGIN", "").describe("Allowed CORS origin", "", "Another site that may read this dashboard's API answers, such as https://home.example.com. Empty for none.").startup(),
			text(&s.FrontendDir, "FRONTEND_DIR", "").describe("Frontend directory", "", "Serve the UI from disk instead of the binary.").startup(),
		),
		group("Storage and logging",
			choice(&s.LogLevel, "LOG_LEVEL", "INFO", []string{"DEBUG", "INFO", "WARNING", "ERROR"}, map[string]string{"WARN": "WARNING", "CRITICAL": "ERROR"}).describe("Log level", "", "DEBUG adds every cycle's readings; changes apply at once."),
			choice(&s.LogFormat, "LOG_FORMAT", "text", []string{"text", "json"}, nil).describe("Log format", "", "text is one aligned line per entry; json suits log shippers (Loki, Vector, Datadog).").startup(),
			text(&s.HistoryDB, "HISTORY_DB", DefaultHistoryDB).describe("Database", "", "Check history, checks and dashboard settings.").startup(),
			text(&s.ServicesFile, "SERVICES_FILE", "services.json").describe("Import checks from", "", "Read once, while the database has no checks.").startup(),
			text(&s.PluginsDir, "PLUGINS_DIR", "plugins").describe("Plugins folder", "", "Nightjar looks here for plugins, one folder each.").startup(),
			text(&s.RepairsDir, "REPAIRS_DIR", "repairs").describe("Repair scripts folder", "", "Scripts a check can run to repair itself when it goes down.").startup(),
			text(&s.SSHDir, "SSH_DIR", "ssh").describe("SSH keys folder", "", "Keys Nightjar makes to log in to other servers, and the host keys it has seen.").startup(),
			text(&s.DockerSocket, "DOCKER_SOCKET", "/var/run/docker.sock").describe("Docker socket", "", "Where Docker listens, for container checks and restarts.").startup(),
			text(&s.LogFile, "LOG_FILE", "nightjar.log").describe("Log file", "", "").startup(),
			integer(&s.LogMaxBytes, "LOG_MAX_BYTES", 10*1024*1024, 1024).describe("Rotate log at", "bytes", "").startup(),
			integer(&s.LogBackupCount, "LOG_BACKUP_COUNT", 5, 0, 100).describe("Rotated logs kept", "", "").startup(),
			text(&s.ErrorLogFile, "ERROR_LOG_FILE", "nightjar_errors.log").describe("Error log", "", "Unexpected monitor errors with stack traces.").startup(),
		),
	)
}

// Lookup finds a setting's raw value; os.LookupEnv is one.
type Lookup func(key string) (string, bool)

// Layered tries each lookup in turn, so earlier ones win; empty values count as unset.
func Layered(lookups ...Lookup) Lookup {
	return func(key string) (string, bool) {
		for _, l := range lookups {
			if v, ok := l(key); ok && strings.TrimSpace(v) != "" {
				return v, true
			}
		}
		return "", false
	}
}

// MapLookup looks keys up in m.
func MapLookup(m map[string]string) Lookup {
	return func(key string) (string, bool) { v, ok := m[key]; return v, ok }
}

// MinAuthTokenLength is the shortest WEB_AUTH_TOKEN accepted; a shorter one is ignored as unset.
const MinAuthTokenLength = 16

// LoadFrom reads and cross-validates every setting. Invalid values fall back to the default with a
// warning, and adjustments are logged.
func LoadFrom(lookup Lookup) Settings {
	var s Settings
	for _, f := range s.Fields() {
		raw, _ := lookup(f.Key)
		if err := f.Set(raw); err != nil {
			shown := raw
			if f.Kind == KindSecret {
				shown = "(hidden)" // a rejected token is still the owner's token
			}
			log.Warn("Invalid setting, using default", "key", f.Key, "value", shown, "err", err.Error(), "default", f.Default)
			f.Set("")
		}
	}
	s.adjust()
	return s
}

// Validate checks dashboard edits and returns the problems by key. Every key must be a setting that
// the dashboard may change, with a valid value ("" restores the default).
func Validate(values map[string]string) map[string]string {
	var scratch Settings
	fields := map[string]Field{}
	for _, f := range scratch.Fields() {
		fields[f.Key] = f
	}
	problems := map[string]string{}
	for key, raw := range values {
		f, ok := fields[key]
		switch {
		case !ok:
			problems[key] = "unknown setting"
		case f.Startup:
			problems[key] = "only settable in the environment (.env)"
		default:
			if err := f.Set(raw); err != nil {
				problems[key] = err.Error()
			}
		}
	}
	return problems
}

func (s *Settings) adjust() {
	if strings.Contains(s.MediaDirectory, "..") || strings.HasPrefix(s.MediaDirectory, "/proc") || strings.HasPrefix(s.MediaDirectory, "/sys") {
		log.Warn("MEDIA_DIRECTORY contains suspicious path components", "path", s.MediaDirectory)
		s.MediaDirectory = "/home/media"
	}
	s.MediaDirectoryThresholdBytes = int64(s.MediaDirectoryThresholdGB * BytesPerGB)
	if s.DiskAggressiveCleanupThreshold < s.DiskCleanupThreshold {
		s.DiskAggressiveCleanupThreshold = s.DiskCleanupThreshold
		log.Warn("Aggressive cleanup threshold adjusted (must not be below the cleanup threshold)", "value", s.DiskAggressiveCleanupThreshold)
	}
	if s.DiskEarlyWarningThreshold >= s.DiskThreshold {
		s.DiskEarlyWarningThreshold = max(0, s.DiskThreshold-5)
		log.Warn("Early warning threshold adjusted (must be less than main threshold)", "value", s.DiskEarlyWarningThreshold)
	}
	if s.DiskEmergencyThreshold <= s.DiskThreshold {
		s.DiskEmergencyThreshold = min(99, s.DiskThreshold+10)
		log.Warn("Emergency threshold adjusted (must be greater than main threshold)", "value", s.DiskEmergencyThreshold)
	}
}
