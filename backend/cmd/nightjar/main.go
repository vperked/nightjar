// Command nightjar watches host resources and service uptime, alerts to Discord and serves the dashboard API.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/alerts"
	"nightjar/internal/analytics"
	"nightjar/internal/api"
	"nightjar/internal/apps"
	"nightjar/internal/arr"
	"nightjar/internal/cleanup"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/discord"
	"nightjar/internal/docker"
	"nightjar/internal/format"
	"nightjar/internal/kernel"
	"nightjar/internal/logx"
	"nightjar/internal/maintenance"
	"nightjar/internal/mediacleanup"
	"nightjar/internal/metrics"
	"nightjar/internal/monitor"
	"nightjar/internal/netload"
	"nightjar/internal/notify"
	"nightjar/internal/plex"
	"nightjar/internal/plugins"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/remote"
	"nightjar/internal/repair"
	"nightjar/internal/report"
	"nightjar/internal/services"
	"nightjar/internal/smart"
	"nightjar/internal/storage"
	"nightjar/internal/systemd"
	"nightjar/internal/units"
	"nightjar/internal/updates"
)

var log = logx.Component("Core")

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// ssh runs this program to ask for a password when Nightjar puts an SSH key on a server; the
	// password comes in the environment ssh was given, so it is never on a command line or in a file.
	if os.Getenv(remote.AskPassEnv) == "1" {
		fmt.Println(os.Getenv(remote.PasswordEnv))
		return
	}
	envFile := flag.String("env", "", "path to a .env file (default: ./.env, then ../.env)")
	selfTest := flag.Bool("test", false, "run the self-test and exit")
	installSystemd := flag.Bool("install-systemd", false, "install as a systemd service and exit")
	force := flag.Bool("force", false, "with -install-systemd, overwrite an existing unit")
	showVersion := flag.Bool("version", false, "print the version and exit")
	resetAdmin := flag.Bool("reset-admin", false, "delete the dashboard's admin account (for a forgotten password) and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("nightjar", version)
		return
	}

	environ := os.Environ() // before .env is loaded, so a restart reads .env afresh
	loadedEnv := loadEnv(*envFile)
	// The database holds the dashboard's settings, so its own path comes from the environment.
	db := storage.Open(cmp.Or(strings.TrimSpace(os.Getenv("HISTORY_DB")), config.DefaultHistoryDB))
	cfg := config.LoadFrom(config.Layered(config.MapLookup(db.Settings()), os.LookupEnv))
	logx.Setup(logx.Options{Level: cfg.LogLevel, File: cfg.LogFile, MaxBytes: int64(cfg.LogMaxBytes), Backups: cfg.LogBackupCount, JSON: cfg.LogFormat == "json"})
	if loadedEnv != "" {
		log.Info("Loaded environment", "file", loadedEnv)
	}

	switch {
	case *resetAdmin:
		os.Exit(runResetAdmin(db))
	case *selfTest:
		os.Exit(runSelfTest(cfg, db))
	case *installSystemd || config.Bool("INSTALL_SYSTEMD", false):
		os.Exit(install(cfg, loadedEnv, *force || config.Bool("SYSTEMD_FORCE_INSTALL", false)))
	}
	if run(cfg, db) {
		restart(environ)
	}
}

// runResetAdmin deletes the admin account and signs everyone out. The next start logs a new setup
// code to create the account again with.
func runResetAdmin(db *storage.Store) int {
	if err := accounts.Reset(db); err != nil {
		fmt.Fprintln(os.Stderr, "could not reset the admin account:", err)
		return 1
	}
	fmt.Println("Admin account deleted. Restart the monitor, then create a new one with the setup code it logs.")
	return 0
}

// restartedEnv marks a process started by restart, which skips the startup and shutdown notices.
const restartedEnv = "NIGHTJAR_RESTARTED"

// restart replaces this process with a fresh copy (same PID, so systemd keeps tracking it).
func restart(environ []string) {
	exe, err := os.Executable()
	if err == nil {
		environ = append(slices.DeleteFunc(environ, func(kv string) bool { return strings.HasPrefix(kv, restartedEnv+"=") }), restartedEnv+"=1")
		err = syscall.Exec(exe, os.Args, environ)
	}
	log.Error("Restart failed; exiting so the service manager starts the monitor again", "err", err)
	os.Exit(1)
}

func loadEnv(explicit string) string {
	candidates := []string{explicit}
	if explicit == "" {
		candidates = []string{".env", "../.env"}
	}
	for _, c := range candidates {
		if err := config.LoadDotenv(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

func install(cfg config.Settings, envFile string, force bool) int {
	log.Info("Installing the systemd service")
	if !systemd.Available() {
		log.Error("systemd is not available on this system")
		return 1
	}
	wd, _ := os.Getwd()
	if err := systemd.Install(cfg.SystemdServiceName, wd, envFile, force); err != nil {
		log.Error("Failed to install systemd service", "err", err)
		return 1
	}
	name := cfg.SystemdServiceName
	log.Info("systemd service installed; it starts on boot")
	log.Info("Start it now with: sudo systemctl start " + name)
	log.Info("View logs with: sudo journalctl -u " + name + " -f")
	return 0
}

type webhooks struct {
	hub                                  *notify.Hub
	main, disk, crash, qbittorrent, plex discord.Sender
	// channels is every configured destination, unfiltered, for test messages.
	channels []notify.Channel
}

// discordHook returns nil when the URL is unset or invalid.
func discordHook(name, url string, cfg config.Settings, origin discord.Origin) *discord.Webhook {
	if url == "" {
		return nil
	}
	w, err := discord.NewWebhook(url, cfg.AlertLogPath)
	if err != nil {
		log.Warn(name+" webhook invalid", "err", err)
		return nil
	}
	w.Origin = origin
	return w
}

// alertChannels builds the channels besides Discord that have settings.
func alertChannels(cfg config.Settings, origin discord.Origin) []notify.Channel {
	channels, problems := notify.FromSettings(cfg, origin)
	for _, err := range problems {
		log.Warn("Alert channel not configured correctly", "err", err)
	}
	return channels
}

// connectWebhooks routes every alert through the notification hub, which keeps it in the history
// and sends it on by the dashboard's rules. Until those say otherwise, Discord hears everything on
// each category's webhook (disk, kernel and Plex fall back to the main one; qBittorrent does not)
// and every other channel hears ALERT_CHANNELS_MIN_SEVERITY and up.
func connectWebhooks(cfg config.Settings, db *storage.Store, pl *plugins.Manager) webhooks {
	host, _ := os.Hostname()
	origin := discord.Origin{Host: host, DashboardURL: cfg.DashboardURL}
	main := discordHook("Discord", cfg.WebhookURL, cfg, origin)
	qb := discordHook("qBittorrent Discord", cfg.QBittorrentWebhookURL, cfg, origin)
	byArea := map[string]notify.Channel{}
	for area, hook := range map[string]*discord.Webhook{
		"storage":  discordHook("Disk Discord", cfg.DiskWebhookURL, cfg, origin),
		"system":   discordHook("Crash Discord", cfg.CrashWebhookURL, cfg, origin),
		"media":    discordHook("Plex Discord", cfg.PlexWebhookURL, cfg, origin),
		"torrents": qb,
	} {
		if hook != nil {
			byArea[area] = hook
		}
	}

	var w webhooks
	var dests []notify.Destination
	if main != nil || len(byArea) > 0 {
		d := notify.Destination{ByArea: byArea, Default: func(area string) notify.Level {
			if area == "torrents" && qb == nil {
				return notify.Off
			}
			return notify.All
		}}
		if main != nil {
			d.Channel = main
			w.channels = append(w.channels, main)
		}
		dests = append(dests, d)
	}
	level := notify.LevelFor(cfg.ChannelsMinSeverity)
	for _, c := range alertChannels(cfg, origin) {
		dests = append(dests, notify.Destination{Channel: c, Default: func(string) notify.Level { return level }})
		w.channels = append(w.channels, c)
	}
	// Every plugin that delivers alerts is a channel; one turned off hears nothing until it is on.
	for _, p := range pl.Providing(plugins.Notify) {
		id := p.ID
		c := notify.NewPlugin(p.Name, func(payload map[string]any) error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			return pl.Send(ctx, id, payload)
		}, origin)
		dests = append(dests, notify.Destination{Channel: c, Default: func(string) notify.Level {
			if !pl.Enabled(id) {
				return notify.Off
			}
			return level
		}})
		w.channels = append(w.channels, c)
	}

	w.hub = notify.NewHub(db, dests)
	if names := w.hub.Channels(); len(names) > 0 {
		log.Info("Alerts go to " + strings.Join(names, ", "))
	} else {
		log.Info("No alert channels set up; alerts are kept in the dashboard's notifications")
	}
	w.main, w.disk, w.crash = w.hub.Route("resources"), w.hub.Route("storage"), w.hub.Route("system")
	if cfg.QBittorrentMonitor {
		w.qbittorrent = w.hub.Route("torrents")
	}
	if cfg.PlexMonitor {
		w.plex = w.hub.Route("media")
	}
	return w
}

func buildContext(cfg config.Settings, db *storage.Store) *monitor.Context {
	log.Info("Starting monitors")
	pl := plugins.New(cfg.PluginsDir, db)
	if err := pl.Scan(); err != nil {
		log.Warn("Plugins folder not readable", "dir", cfg.PluginsDir, "err", err)
	} else if found := pl.List(); len(found) > 0 {
		log.Info(fmt.Sprintf("Found %d plugins in %s", len(found), pl.Dir()))
	}
	services.PluginCheck = func(s services.Spec, id, arg string) services.Result {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.Timeout*float64(time.Second)))
		defer cancel()
		r := pl.RunCheck(ctx, id, arg)
		return services.Result{Up: r.Up, Detail: r.Detail, LatencyMS: r.LatencyMS}
	}
	wh := connectWebhooks(cfg, db, pl)
	logs := cleanup.NewLogMonitor(cfg.LogFile, cfg.LogThresholdMB, cfg.RotatedLogRetentionDays)
	ctx := &monitor.Context{
		Cfg:                cfg,
		Processes:          collect.NewProcessScanner(),
		Sensors:            collect.NewSensors(),
		GPU:                &collect.GPUReader{},
		Bandwidth:          &collect.BandwidthMeter{},
		Logs:               logs,
		Webhook:            wh.main,
		DiskWebhook:        wh.disk,
		CrashWebhook:       wh.crash,
		QBittorrentWebhook: wh.qbittorrent,
		PlexWebhook:        wh.plex,
		Channels:           wh.channels,
		Notifications:      wh.hub,
		Metrics:            metrics.New(),
		Gates: alerts.NewGates(cfg.AlertCooldown, max(0, cfg.AlertMaxCooldown), min(10, max(1, cfg.AlertBackoff)),
			max(cfg.AlertRecoverySeconds, 3*cfg.CheckInterval)),
		ErrorLogFile: cfg.ErrorLogFile,
		Store:        db,
		Units:        units.New(),
		Drives:       smart.NewReader(),
		Network:      netload.New(),
		Apps:         apps.New(apps.Options{GitHubToken: cfg.GitHubToken}),
		Plugins:      pl,
		Services:     services.NewMonitor(services.Stored(db, cfg.ServicesFile, func(msg string) { log.Warn(msg) }), db),
	}
	ctx.Docker = docker.New(cfg.DockerSocket)
	services.DockerCheck = func(s services.Spec) services.Result {
		c, cancel := context.WithTimeout(context.Background(), time.Duration(s.Timeout*float64(time.Second)))
		defer cancel()
		start := time.Now()
		up, detail := ctx.Docker.Check(c, s.Target)
		ms := float64(time.Since(start).Microseconds()) / 1000
		return services.Result{Up: up, Detail: detail, LatencyMS: &ms}
	}
	ctx.Repairs = repair.New(ctx.Units, cfg.RepairsDir, db)
	ctx.Repairs.Docker = ctx.Docker
	ctx.MediaCleanup = &mediacleanup.Manager{Store: db, Config: ctx.Config, Send: ctx.PlexWebhook, Now: time.Now,
		Connect: func(c config.Settings) mediacleanup.Sources {
			var src mediacleanup.Sources
			if c.PlexMonitor {
				src.Plex, _ = plex.New(c.PlexURL, c.PlexToken, c.PlexVerifyTLS, 60*time.Second)
			}
			src.QB = ctx.QBittorrent
			src.Sonarr = arr.Connect("sonarr", c.SonarrURL, c.SonarrAPIKey)
			src.Radarr = arr.Connect("radarr", c.RadarrURL, c.RadarrAPIKey)
			return src
		}}
	ctx.Maintenance = maintenance.New(db)
	ctx.Maintenance.Prune(time.Now())
	ctx.Services.Resting = func(name string, now float64) bool {
		_, on := ctx.Maintenance.For(name, time.Unix(int64(now), 0))
		return on
	}
	wh.hub.Maintenance = ctx.Maintenance.Holds
	ctx.Activity = func(who, kind, target, detail string) {
		db.AddActivity(storage.Activity{TS: float64(time.Now().Unix()), Who: who, Kind: kind, Action: detail, Target: target})
	}
	ctx.MediaCleanup.Record = func(action, target string) { ctx.Activity("Nightjar", "cleanup", target, action) }
	monitor.ConnectBot(ctx)
	ctx.Analytics = analytics.NewRecorder(db)
	ctx.Remote = remote.New(remote.Options{Store: db, Alerts: wh.hub.Route("servers"), Analytics: ctx.Analytics, SSHDir: cfg.SSHDir, AutoUpdate: func() bool { return ctx.Config().AgentAutoUpdate }, Limits: func() remote.Limits {
		c := ctx.Config()
		return remote.Limits{Temp: c.TempThreshold, Memory: c.MemoryThreshold, Disk: c.DiskThreshold}
	}})
	ctx.Remote.MoveLegacyChecks(ctx.Services)
	// A monitor restarting itself to apply settings already announced services that were failed.
	ctx.Units.AnnounceExisting = os.Getenv(restartedEnv) == ""
	monitor.LoadDrives(ctx)
	ctx.Cleaner = &cleanup.Cleaner{Logs: logs, Opts: cleanup.Options{
		AllowUnsafePaths:     cfg.AllowUnsafeCleanupPaths,
		AggressiveThreshold:  cfg.DiskAggressiveCleanupThreshold,
		LogServicesToRestart: cfg.LogServicesToRestart,
		ManageNginxLogs:      cfg.ManageNginxLogs,
		NginxLogDir:          cfg.NginxLogDir,
		NginxUserGroup:       cfg.NginxUserGroup,
		NginxServiceName:     cfg.NginxServiceName,
		TempDirs:             cfg.TempDirs,
		CacheDirs:            cfg.CacheDirs,
		EmergencyCacheDirs:   cfg.EmergencyCacheDirs,
	}}
	if cfg.KernelMonitorEnabled {
		ctx.Kernel = kernel.New(cfg.KernelTrackingFile, cfg.KernelIgnoreDevices)
		log.Info("Kernel monitoring enabled")
	}
	if cfg.QBittorrentMonitor {
		if qb, err := qbittorrent.New(cfg.QBittorrentHost, cfg.QBittorrentPort, cfg.QBittorrentUsername, cfg.QBittorrentPassword); err == nil {
			ctx.QBittorrent = qb
			ctx.QBittorrentBoard = &qbittorrent.Board{}
			log.Info("qBittorrent monitoring enabled")
		} else {
			log.Warn("qBittorrent monitoring disabled", "err", err)
		}
	} else {
		log.Info("qBittorrent monitoring disabled (set QBITTORRENT_MONITOR=true to enable)")
	}
	if cfg.PlexMonitor {
		if c, err := plex.New(cfg.PlexURL, cfg.PlexToken, cfg.PlexVerifyTLS, 5*time.Second); err == nil {
			ctx.Plex = plex.NewMonitor(c, cfg.PlexCheckInterval, cfg.PlexRetries)
			log.Info("Plex monitoring enabled", "url", cfg.PlexURL)
			if cfg.PlexToken == "" {
				log.Warn("PLEX_TOKEN is not set; most Plex servers will reject the monitor's requests")
			}
		} else {
			log.Warn("Plex monitoring disabled", "err", err)
		}
	}
	if cfg.FreezeMonitorEnabled {
		ctx.Freeze = monitor.NewFreezeMonitor(cfg.FreezeThreshold, cfg.FreezeHeartbeatTimeout)
	}
	ctx.Watchdog = &monitor.Watchdog{
		Timeout:         time.Duration(cfg.MonitoringWatchdogTimeout * float64(time.Second)),
		AutoRestart:     cfg.WatchdogAutoRestart,
		RestartCooldown: time.Duration(cfg.WatchdogRestartCooldown * float64(time.Second)),
		ServiceName:     cfg.SystemdServiceName,
		AlertCooldown:   time.Duration(cfg.AlertCooldown * float64(time.Second)),
		Webhook:         wh.crash,
	}
	log.Info("Monitors started")
	return ctx
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func logSummary(ctx *monitor.Context) {
	cfg := ctx.Cfg
	sys := collect.SystemInfo()
	mem, _ := collect.VirtualMemory()
	log.Info(fmt.Sprintf("System: %s %s | Host: %s", sys.Platform, sys.PlatformRelease, sys.Hostname))
	log.Info("Uptime: " + sys.UptimeFormatted)
	log.Info(fmt.Sprintf("CPU: %d cores | Threshold: %.1f%%", collect.CPUCount(), cfg.CPUThreshold))
	log.Info(fmt.Sprintf("Memory: %s | Threshold: %.1f%%", format.Bytes(float64(mem.Total)), cfg.MemoryThreshold))
	log.Info(fmt.Sprintf("Disk: threshold %.1f%% | early warning %.1f%% | cleanup %.1f%% | emergency %.1f%%",
		cfg.DiskThreshold, cfg.DiskEarlyWarningThreshold, cfg.DiskCleanupThreshold, cfg.DiskEmergencyThreshold))
	if gpu := ctx.GPU.Read(); gpu != nil {
		log.Info(fmt.Sprintf("GPU: %s | Threshold: %.1f%%", gpu.Name, cfg.GPUThreshold))
	}
	if t := ctx.Sensors.CPUTemperature(); t != nil {
		log.Info(fmt.Sprintf("Temperature: %.1fC | Threshold: %.1fC for %.0fs", *t, cfg.TempThreshold, cfg.TempAlertDuration))
	}
	log.Info(fmt.Sprintf("Network: Bandwidth threshold %.1f MB/s", cfg.NetworkBandwidthThreshold))
	log.Info("Freeze Detection: " + onOff(cfg.FreezeMonitorEnabled))
	log.Info("Directory Monitoring: " + onOff(cfg.MediaMonitorEnabled))
	log.Info("Kernel Monitoring: " + onOff(cfg.KernelMonitorEnabled))
	if ctx.Updates != nil {
		log.Info(fmt.Sprintf("Updates: checks %s | %d targets | auto-updates %s", onOff(cfg.SystemUpdateEnabled), len(ctx.Updates.Statuses()), monitor.UpdateWindow(cfg)))
	}
	log.Info(fmt.Sprintf("Uptime checks: %d", len(ctx.Services.Specs())))
	log.Info("Plex monitoring: " + onOff(ctx.Plex != nil))
	log.Info(fmt.Sprintf("Settings: Check every %.1fs | Cooldown %.1fs", cfg.CheckInterval, cfg.AlertCooldown))
}

// lifecycleGap stops a monitor that keeps crashing and restarting from announcing every start and stop.
const lifecycleGap = 30 * time.Minute

// announce reports whether a start or stop notice may go out now, and if so remembers that it did.
func announce(db *storage.Store, kind string) bool {
	key := "notify.last_" + kind
	if last, err := strconv.ParseInt(db.Meta(key), 10, 64); err == nil && time.Since(time.Unix(last, 0)) < lifecycleGap {
		log.Info(fmt.Sprintf("Not sending the %s notice: one went out less than %s ago", kind, format.Duration(lifecycleGap.Seconds())))
		return false
	}
	db.SetMeta(key, strconv.FormatInt(time.Now().Unix(), 10))
	return true
}

func sendStartup(ctx *monitor.Context) {
	if ctx.Webhook == nil || os.Getenv(restartedEnv) != "" || !announce(ctx.Store, "startup") {
		return
	}
	log.Info("Sending system info to Discord")
	collect.PerCore() // prime the CPU delta
	time.Sleep(500 * time.Millisecond)
	per, _ := collect.PerCore()
	cpuPct := 0.0
	for _, v := range per {
		cpuPct += v
	}
	if len(per) > 0 {
		cpuPct /= float64(len(per))
	}
	mem, _ := collect.VirtualMemory()
	if discord.SystemInfo(ctx.Webhook, collect.SystemInfo(), cpuPct, collect.CPUCount(), mem,
		collect.TotalDisk(collect.Partitions(true)), ctx.Processes.Overview(5)) {
		log.Info("System info sent to Discord")
	}
	if ctx.QBittorrent != nil && ctx.QBittorrentWebhook != nil && ctx.QBittorrent.Available() {
		torrents := ctx.QBittorrent.Torrents()
		s := qbittorrent.Summarize(torrents)
		log.Info(fmt.Sprintf("qBittorrent: %d torrents (%d downloading, %d seeding)", s.Total, s.Downloading, s.Seeding))
		var downloading, seeding []discord.TorrentInfo
		for _, t := range torrents {
			info := discord.TorrentInfo{Name: t.Name, ProgressPercent: t.ProgressPercent(), Ratio: t.Ratio, DLSpeed: t.DLSpeed, UPSpeed: t.UPSpeed}
			if qbittorrent.DownloadingStates[t.State] {
				downloading = append(downloading, info)
			} else if qbittorrent.SeedingActiveStates[t.State] {
				seeding = append(seeding, info)
			}
		}
		if discord.TorrentList(ctx.QBittorrentWebhook, downloading, seeding) {
			log.Info("qBittorrent status sent to Discord")
		}
	}
}

// run monitors until a signal stops it, or until settings saved in the dashboard ask for a
// restart, which it reports.
func run(cfg config.Settings, db *storage.Store) (restart bool) {
	log.Info("Starting Nightjar " + version)
	ctx := buildContext(cfg, db)
	ctx.Watchdog.Start()

	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()
	restartRequested := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-restartRequested:
			restart = true
			cancel()
		case <-stop.Done():
		}
	}()

	requestRestart := func() { once.Do(func() { close(restartRequested) }) }
	ctx.Updates = updates.New(updates.Options{
		Store: db, GitHubToken: cfg.GitHubToken, Timeout: time.Duration(cfg.SystemUpdateTimeout) * time.Second,
		Alerts: ctx.Webhook, NotifyAvailable: cfg.UpdatesNotify, Restart: requestRestart,
	}, func() []updates.Spec { return updates.Detect(updates.Exec) }, legacyAutoUpdate(db))
	logSummary(ctx)

	var srv *api.Server
	if cfg.WebUIEnabled {
		srv = api.New(api.Config{
			Host: cfg.WebHost, Port: strconv.Itoa(cfg.WebPort), Accounts: accounts.New(db), AuthToken: cfg.WebAuthToken,
			AllowedOrigin: cfg.WebAllowedOrigin, FrontendDir: cfg.FrontendDir,
			Version: version,
			Store:   db, Restart: requestRestart,
		}, ctx)
		if err := srv.Start(); err != nil {
			if errors.Is(err, api.ErrPublicWithoutToken) {
				log.Error(err.Error())
			} else {
				log.Error("Web server failed to start", "err", err)
			}
			srv = nil
		} else {
			log.Info("Access the dashboard at http://" + net.JoinHostPort(displayHost(cfg.WebHost), strconv.Itoa(cfg.WebPort)))
		}
	} else {
		log.Info("Web UI disabled (set WEB_UI_ENABLED=true to enable)")
	}
	sendStartup(ctx)

	log.Info("Starting continuous monitoring (Press Ctrl+C to stop)")
	started := time.Now()
	remoteDone := make(chan struct{})
	go func() {
		defer close(remoteDone)
		ctx.Remote.Run(stop)
	}()
	go ctx.Plugins.Loop(stop)
	go report.Schedule(stop.Done(), ctx)
	go ctx.MediaCleanup.Loop(stop.Done())
	loop := monitor.NewLoop(ctx)
	loop.Run(stop)

	shutdownStart := time.Now()
	if restart {
		log.Info("Restarting to apply new settings or an update")
	} else {
		log.Info("Shutdown signal received; stopping")
	}
	ctx.Watchdog.Stop()
	if srv != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
		srv.Shutdown(sctx)
		scancel()
	}
	loop.Wait() // running uptime checks still record their results
	<-remoteDone
	ctx.Analytics.Flush() // the minute in progress
	announceStop := !restart && ctx.Webhook != nil && announce(db, "shutdown")
	db.Close() // after the API and the checks, which use it, have stopped
	elapsed := time.Since(shutdownStart).Seconds()
	log.Info(fmt.Sprintf("Shutdown complete (%.2fs)", elapsed))
	if announceStop && discord.Shutdown(ctx.Webhook, time.Since(started).Seconds(), elapsed) {
		log.Info("Shutdown notification sent")
	}
	if !restart {
		log.Info("Nightjar stopped gracefully")
	}
	return restart
}

// legacyAutoUpdate carries SYSTEM_UPDATE_APPLY (from before updates had their own page) over to
// the OS packages target the first time targets are detected.
func legacyAutoUpdate(db *storage.Store) bool {
	return config.Bool("SYSTEM_UPDATE_APPLY", false) || strings.EqualFold(db.Settings()["SYSTEM_UPDATE_APPLY"], "true")
}

func displayHost(h string) string {
	if h == "0.0.0.0" || h == "::" || h == "" {
		return "localhost"
	}
	return h
}

func runSelfTest(cfg config.Settings, db *storage.Store) int {
	ok := true
	report := func(name string, pass bool, msg string) {
		status := "PASS"
		if !pass {
			status, ok = "FAIL", false
		}
		log.Info(fmt.Sprintf("[%s] %s %s", status, name, msg))
	}
	collect.PerCore()
	time.Sleep(300 * time.Millisecond)
	per, err := collect.PerCore()
	report("cpu", err == nil && len(per) > 0, fmt.Sprintf("%d cores", len(per)))
	mem, err := collect.VirtualMemory()
	report("memory", err == nil && mem.Total > 0, format.Bytes(float64(mem.Total)))
	parts := collect.Partitions(true)
	report("disk", len(parts) > 0, fmt.Sprintf("%d partitions", len(parts)))
	_, err = collect.NetworkIO()
	report("network", err == nil, "")
	sensors := collect.NewSensors()
	log.Info(fmt.Sprintf("[INFO] sensors available=%v fans=%d", sensors.Available(), len(sensors.Fans())))
	gpu := &collect.GPUReader{}
	log.Info(fmt.Sprintf("[INFO] nvidia-smi available=%v", gpu.Available()))
	o := collect.NewProcessScanner().Overview(5)
	report("processes", o.Total > 0, fmt.Sprintf("%d running", o.Total))
	{
		for _, s := range services.Stored(db, cfg.ServicesFile, func(msg string) { log.Warn(msg) }) {
			r := services.Run(s)
			log.Info(fmt.Sprintf("[INFO] service %s: up=%v (%s)", s.Name, r.Up, r.Detail))
		}
	}
	if cfg.PlexMonitor {
		c, err := plex.New(cfg.PlexURL, cfg.PlexToken, cfg.PlexVerifyTLS, 5*time.Second)
		var info plex.Server
		if err == nil {
			info, err = c.Server()
		}
		msg := info.Name + " " + info.Version
		if err != nil {
			msg = err.Error()
		}
		report("plex", err == nil, msg)
	}
	for _, r := range notify.Test(connectWebhooks(cfg, nil, plugins.New(cfg.PluginsDir, nil)).channels) {
		report("alerts "+r.Channel, r.OK, r.Error)
	}
	if ok {
		return 0
	}
	return 1
}
