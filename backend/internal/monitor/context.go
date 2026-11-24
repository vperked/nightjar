// Package monitor runs the monitoring loop: one cycle collects a snapshot and runs every handler in order.
package monitor

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"nightjar/internal/alerts"
	"nightjar/internal/analytics"
	"nightjar/internal/apps"
	"nightjar/internal/cleanup"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/ddos"
	"nightjar/internal/discord"
	"nightjar/internal/docker"
	"nightjar/internal/firewall"
	"nightjar/internal/kernel"
	"nightjar/internal/logx"
	"nightjar/internal/maintenance"
	"nightjar/internal/mediacleanup"
	"nightjar/internal/metrics"
	"nightjar/internal/netload"
	"nightjar/internal/notify"
	"nightjar/internal/plex"
	"nightjar/internal/plugins"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/remote"
	"nightjar/internal/repair"
	"nightjar/internal/services"
	"nightjar/internal/smart"
	"nightjar/internal/sshd"
	"nightjar/internal/storage"
	"nightjar/internal/tailscale"
	"nightjar/internal/units"
	"nightjar/internal/updates"
)

var log = logx.Component("Core")

// Context is everything the handlers share. Optional members are nil when not configured.
type Context struct {
	Cfg         config.Settings
	Processes   *collect.ProcessScanner
	Sensors     *collect.Sensors
	GPU         *collect.GPUReader
	Bandwidth   *collect.BandwidthMeter
	Logs        *cleanup.LogMonitor
	Cleaner     *cleanup.Cleaner
	Kernel      *kernel.Monitor
	QBittorrent *qbittorrent.Client
	// QBittorrentBoard is what the loop last saw in qBittorrent, for the dashboard.
	QBittorrentBoard *qbittorrent.Board
	Services         *services.Monitor
	Plex             *plex.Monitor
	Updates          *updates.Manager
	Units            *units.Monitor
	Drives           *smart.Reader
	Network          *netload.Meter        // the dashboard's live network meter
	Store            *storage.Store        // the database, for state kept across restarts (nil-safe)
	Apps             *apps.Manager         // the app store
	Plugins          *plugins.Manager      // the owner's own checks, cards and alert channels
	Repairs          *repair.Runner        // repairs checks that go down, when they say how
	Maintenance      *maintenance.Manager  // planned downtime
	MediaCleanup     *mediacleanup.Manager // removes what nobody watches
	DiscordBot       *discord.Bot          // when alerts go through a bot, for its button clicks
	// Activity records something done, by whom (for the activity log); nil records nothing.
	Activity func(who, kind, target, detail string)
	Docker   *docker.Client    // containers, for container checks and repairs
	Remote   *remote.Monitor   // other servers, watched through their agents
	Attacks  *ddos.Watcher     // denial-of-service attacks on this machine
	Firewall *firewall.Manager // firewall rules set from the dashboard
	SSHD     *sshd.Manager     // who may log in over SSH: keys, and passwords on or off
	// Tailscale is this machine's Tailscale: connecting, logging in, exit node, routes.
	Tailscale *tailscale.Manager
	// Analytics records every server's minutes for the analytics page; nil records nothing.
	Analytics *analytics.Recorder
	Freeze    *FreezeMonitor
	Watchdog  *Watchdog

	Webhook            discord.Sender
	DiskWebhook        discord.Sender
	CrashWebhook       discord.Sender
	QBittorrentWebhook discord.Sender
	PlexWebhook        discord.Sender
	Channels           []notify.Channel // every alert destination, for test messages
	// Notifications keeps and routes every alert the webhooks above are sent; nil in tests.
	Notifications *notify.Hub

	Metrics *metrics.Collector
	Gates   *alerts.Gates

	ErrorLogFile      string
	errorMu           sync.Mutex
	lastErrorWebhook  time.Time
	errorWebhookCycle time.Duration

	snapMu  sync.RWMutex
	latest  *Snapshot
	samples []Sample

	// Settings changed in the dashboard wait in pending until the loop's next cycle, so the loop
	// never sees them change mid-cycle. cfgMu guards Cfg for readers outside the loop.
	cfgMu   sync.RWMutex
	pending *config.Settings

	// cleaning is set while a disk or log cleanup runs, a timed-out one included (see cleanupStep).
	cleaning atomic.Bool
	// loopThread is the OS thread the loop's goroutine is locked to while Run runs, 0 otherwise:
	// it is how LoopWaiting knows the loop from any other goroutine.
	loopThread atomic.Int64
}

// Config is a copy of the settings in force, for code outside the monitoring loop.
func (c *Context) Config() config.Settings {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	if c.pending != nil {
		return *c.pending
	}
	return c.Cfg
}

// Reconfigure hands the loop new settings to use from its next cycle. Only settings marked live
// may differ: everything else was built from the old ones at startup.
func (c *Context) Reconfigure(s config.Settings) {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	c.pending = &s
}

// applyPending switches to settings from Reconfigure; the loop calls it between cycles.
func (c *Context) applyPending() bool {
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	if c.pending == nil {
		return false
	}
	c.Cfg, c.pending = *c.pending, nil
	logx.SetLevel(c.Cfg.LogLevel)
	// The check interval is live, and an episode must outlast a few cycles of it.
	if c.Gates != nil {
		c.Gates.SetRecovery(GateRecovery(c.Cfg))
	}
	return true
}

// GateRecovery is how long an alert's condition must go unreported before its episode ends:
// ALERT_RECOVERY_SECONDS, and never less than three check intervals.
func GateRecovery(cfg config.Settings) float64 {
	return max(cfg.AlertRecoverySeconds, 3*cfg.CheckInterval)
}

// Sample is one cycle's headline numbers, kept so the dashboard can draw trends immediately.
type Sample struct {
	TS     float64  `json:"ts"`
	CPU    float64  `json:"cpu"`
	Memory float64  `json:"memory"`
	Disk   float64  `json:"disk"`
	RxBps  *float64 `json:"rx_bps"`
	TxBps  *float64 `json:"tx_bps"`
}

// maxSamples is about 30 minutes at the default 5s check interval.
const maxSamples = 360

// History returns a copy of the recent samples, oldest first.
func (c *Context) History() []Sample {
	c.snapMu.RLock()
	defer c.snapMu.RUnlock()
	return append([]Sample(nil), c.samples...)
}

// Heartbeat tells the watchdog (and the freeze monitor) the loop is still alive. Only the loop's
// goroutine may call it: a beat from anywhere else would hide a stuck loop.
func (c *Context) Heartbeat() {
	if c.Watchdog != nil {
		c.Watchdog.Heartbeat()
	}
	if c.Freeze != nil {
		c.Freeze.Heartbeat()
	}
}

// LoopWaiting is Heartbeat for hooks that any goroutine may call, such as the notification hub's
// Waiting, called while an alert waits on a slow channel: it beats only when called on the loop's
// own goroutine. The loop waiting on its own alert is busy, not stuck; an uptime check or the API
// waiting on theirs says nothing about the loop, and must not hide it hanging.
func (c *Context) LoopWaiting() {
	if t := c.loopThread.Load(); t != 0 && int64(unix.Gettid()) == t {
		c.Heartbeat()
	}
}

// claimThread locks the calling goroutine to its OS thread and marks it the loop's, until release
// is called. A locked thread runs no other goroutine, so its id names the loop's goroutine alone.
func (c *Context) claimThread() (release func()) {
	runtime.LockOSThread()
	c.loopThread.Store(int64(unix.Gettid()))
	return func() {
		c.loopThread.Store(0)
		runtime.UnlockOSThread()
	}
}

// Latest is the most recent completed snapshot, for the API.
func (c *Context) Latest() *Snapshot {
	c.snapMu.RLock()
	defer c.snapMu.RUnlock()
	return c.latest
}

func (c *Context) setLatest(s *Snapshot) {
	sample := Sample{TS: s.Now, CPU: s.CPUPercent, Memory: s.Memory.Percent, Disk: s.DiskPercent}
	if s.Bandwidth != nil {
		rx, tx := s.Bandwidth.RecvPerSec, s.Bandwidth.SentPerSec
		sample.RxBps, sample.TxBps = &rx, &tx
	}
	c.snapMu.Lock()
	defer c.snapMu.Unlock()
	c.latest = s
	c.samples = append(c.samples, sample)
	if len(c.samples) > maxSamples {
		c.samples = c.samples[len(c.samples)-maxSamples:]
	}
}

// ReportError appends to the error log and notifies the crash (or main) webhook, at most every 5 minutes.
func (c *Context) ReportError(err error, where string) {
	stack := string(debug.Stack())
	entry := fmt.Sprintf("[%s] %T: %v\nContext: %s\nTraceback:\n%s\n%s\n",
		time.Now().Format("2006-01-02 15:04:05"), err, err, where, stack, "================================================================================")
	c.errorMu.Lock()
	defer c.errorMu.Unlock()
	if c.ErrorLogFile != "" {
		if f, ferr := os.OpenFile(c.ErrorLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640); ferr == nil {
			f.WriteString(entry)
			f.Close()
		}
	}
	target := c.CrashWebhook
	if target == nil {
		target = c.Webhook
	}
	cooldown := c.errorWebhookCycle
	if cooldown == 0 {
		cooldown = 5 * time.Minute
	}
	if target == nil || time.Since(c.lastErrorWebhook) < cooldown {
		return
	}
	if len(stack) > 1000 {
		stack = stack[:1000] + "... (truncated)"
	}
	fields := []discord.Field{
		{Name: "Where", Value: discord.Truncate(where, 500), Inline: true},
		{Name: "Kind", Value: fmt.Sprintf("`%T`", err), Inline: true},
		{Name: "Stack", Value: "```\n" + stack + "\n```"},
	}
	if target.SendEmbed("Nightjar hit an error", discord.Truncate(err.Error(), 500), discord.Critical, fields, "Error Alert") {
		c.lastErrorWebhook = time.Now()
	}
}

// Snapshot is one cycle's readings. Collectors fill it in; handlers only read it.
type Snapshot struct {
	Time         time.Time
	Now          float64
	CPUPercent   float64
	PerCore      []float64
	Memory       collect.Memory
	Swap         collect.Memory
	GPU          *collect.GPU
	Partitions   []collect.Partition
	TotalDisk    collect.DiskTotal
	DiskPercent  float64
	CPUTemp      *float64
	Bandwidth    *collect.Bandwidth
	NetworkMBps  *float64
	DiskIO       *collect.IO
	NetworkIO    *collect.IO
	TopCPU       []collect.Process
	TopMemory    []collect.Process
	ProcessTotal int
}

// handler is the shared alert flow: the gate decides whether this occurrence may notify.
type handler struct {
	ctx *Context
	log *slog.Logger
}

func newHandler(ctx *Context, component string) handler {
	return handler{ctx: ctx, log: logx.Component(component)}
}

// raiseAlert logs "[ALERT] message" and calls send when webhook is set (or logs plainly otherwise),
// then records the alert. It returns false when the gate suppressed this occurrence.
func (h handler) raiseAlert(key string, now float64, message string, webhook discord.Sender, send func() bool, alertType string, recordResult bool) bool {
	return h.raiseAlertEvery(key, now, 0, message, webhook, send, alertType, recordResult)
}

// raiseAlertEvery is raiseAlert for a condition looked at only every interval seconds, so its
// episode (and the cooldown's backoff) survives the gaps between looks.
func (h handler) raiseAlertEvery(key string, now, interval float64, message string, webhook discord.Sender, send func() bool, alertType string, recordResult bool) bool {
	gate := h.ctx.Gates.GetEvery(key, now, interval)
	// Marked sent before sending, so a second report of the same condition arriving while this
	// one is still being sent waits out the cooldown instead of alerting twice.
	if !gate.Allow(now) {
		h.log.Debug(fmt.Sprintf("%s cooldown: %.0fs", key, gate.SecondsUntilNext(now)))
		return false
	}
	if webhook != nil {
		h.log.Warn("[ALERT] " + message)
		if alertType != "" {
			h.ctx.Metrics.RecordAlert(alertType)
		}
		ok := send()
		if recordResult {
			h.ctx.Metrics.RecordWebhookResult(ok)
		}
		if ok {
			h.log.Info("    Discord alert sent")
		} else {
			h.log.Warn("    Discord alert failed")
		}
	} else {
		h.log.Warn(message)
	}
	return true
}

// beatEvery is how often the loop tells the watchdog it is alive while it waits on slow work.
const beatEvery = 5 * time.Second

// withTimeout runs fn in a goroutine and returns def if it does not finish in time. It is for the
// loop's goroutine: while it waits it beats c's heartbeat, since it always returns within timeout
// and a loop waiting on bounded work has not stopped (c may be nil, for no beats).
func withTimeout[T any](c *Context, timeout time.Duration, def T, name string, fn func() T) T {
	ch := make(chan T, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in "+name, "panic", p)
			}
		}()
		ch <- fn()
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	beat := time.NewTicker(beatEvery)
	defer beat.Stop()
	for {
		select {
		case v := <-ch:
			return v
		case <-beat.C:
			if c != nil {
				c.Heartbeat()
			}
		case <-deadline.C:
			log.Warn(fmt.Sprintf("%s timed out after %s", name, timeout))
			return def
		}
	}
}

// cleanupStep runs one disk or log cleanup like withTimeout, but never two at once. A cleanup that
// times out goes on in the background, and another started beside it would trim, empty or delete
// the same files under it; so while one is still going, the next is skipped (ran is false) and
// tried again on a later cycle.
func cleanupStep[T any](h handler, timeout time.Duration, def T, name string, fn func() T) (v T, ran bool) {
	if !h.ctx.cleaning.CompareAndSwap(false, true) {
		h.log.Debug(fmt.Sprintf("Skipping %s: an earlier cleanup is still running", name))
		return def, false
	}
	return withTimeout(h.ctx, timeout, def, name, func() T {
		defer h.ctx.cleaning.Store(false)
		return fn()
	}), true
}

func field(name, value string, inline bool) discord.Field {
	return discord.Field{Name: name, Value: value, Inline: inline}
}

// send posts an alert when the webhook is configured.
func (c *Context) send(webhook discord.Sender, sev discord.Severity, title, desc string, fields []discord.Field, footer string) bool {
	if webhook == nil {
		return false
	}
	return webhook.SendEmbed(title, desc, sev, fields, footer)
}

// UpdateWindow is when automatic updates may start.
func UpdateWindow(cfg config.Settings) updates.Window {
	return updates.Window{Start: cfg.UpdatesWindowStart, End: cfg.UpdatesWindowEnd}
}

// AlertDestinations says where alerts go, for the log: the channels set up, else the dashboard's
// notifications alone.
func (c *Context) AlertDestinations() string {
	if c.Notifications != nil {
		if ch := c.Notifications.Channels(); len(ch) > 0 {
			return strings.Join(ch, ", ")
		}
	}
	return "the dashboard's notifications (no alert channels set up)"
}
