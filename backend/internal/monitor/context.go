// Package monitor runs the monitoring loop: one cycle collects a snapshot and runs every handler in order.
package monitor

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"servermonitor/internal/alerts"
	"servermonitor/internal/apps"
	"servermonitor/internal/cleanup"
	"servermonitor/internal/collect"
	"servermonitor/internal/config"
	"servermonitor/internal/discord"
	"servermonitor/internal/kernel"
	"servermonitor/internal/logx"
	"servermonitor/internal/metrics"
	"servermonitor/internal/netload"
	"servermonitor/internal/notify"
	"servermonitor/internal/plex"
	"servermonitor/internal/qbittorrent"
	"servermonitor/internal/services"
	"servermonitor/internal/smart"
	"servermonitor/internal/storage"
	"servermonitor/internal/units"
	"servermonitor/internal/updates"
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
	Network          *netload.Meter // the dashboard's live network meter
	Store            *storage.Store // the database, for state kept across restarts (nil-safe)
	Apps             *apps.Manager  // the app store
	Freeze           *FreezeMonitor
	Watchdog         *Watchdog

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
	return true
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

// Heartbeat tells the watchdog the loop is still alive.
func (c *Context) Heartbeat() {
	if c.Watchdog != nil {
		c.Watchdog.Heartbeat()
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
		{Name: "Error Type", Value: fmt.Sprintf("%T", err), Inline: true},
		{Name: "Error Message", Value: discord.Truncate(err.Error(), 500)},
		{Name: "Context", Value: discord.Truncate(where, 500)},
		{Name: "Traceback", Value: "```\n" + stack + "\n```"},
	}
	if target.SendEmbed("Monitoring Error Detected", "An error occurred in the monitoring system", discord.Critical, fields, "Error Alert") {
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
	gate := h.ctx.Gates.Get(key, now)
	if !gate.ShouldAlert(now) {
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
	gate.MarkSent(now)
	return true
}

// withTimeout runs fn in a goroutine and returns def if it does not finish in time.
func withTimeout[T any](timeout time.Duration, def T, name string, fn func() T) T {
	ch := make(chan T, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in "+name, "panic", p)
			}
		}()
		ch <- fn()
	}()
	select {
	case v := <-ch:
		return v
	case <-time.After(timeout):
		log.Warn(fmt.Sprintf("%s timed out after %s", name, timeout))
		return def
	}
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
