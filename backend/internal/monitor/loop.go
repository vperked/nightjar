package monitor

import (
	"context"
	"fmt"
	"time"

	"nightjar/internal/analytics"
	"nightjar/internal/discord"
)

const metricsRetryDelay = 300

// Loop owns the handlers and runs them once per check interval.
type Loop struct {
	ctx               *Context
	lastMetricsReport float64

	collector  *collector
	disk       *diskCleanup
	cpu        cpuAlert
	memory     memoryAlert
	gpu        gpuAlert
	diskAlerts diskAlerts
	partitions *partitionMaintenance
	temp       *temperatureAlert
	network    networkAlert
	processes  *processAlerts
	freeze     freezeCheck
	media      *mediaCheck
	torrents   *torrentWatcher
	logs       *logCleanup
	kernel     *kernelChecks
	updates    *updateScheduler
	services   *serviceChecks
	plex       *plexWatcher
	units      *serviceWatcher
	drives     *driveWatcher
}

// NewLoop wires every handler.
func NewLoop(ctx *Context) *Loop {
	return &Loop{
		ctx:        ctx,
		collector:  &collector{handler: newHandler(ctx, "Core")},
		disk:       &diskCleanup{handler: newHandler(ctx, "Disk")},
		cpu:        cpuAlert{newHandler(ctx, "Cpu")},
		memory:     memoryAlert{newHandler(ctx, "Memory")},
		gpu:        gpuAlert{newHandler(ctx, "Gpu")},
		diskAlerts: diskAlerts{newHandler(ctx, "Disk")},
		partitions: &partitionMaintenance{handler: newHandler(ctx, "Disk"), lastCleanup: map[string]float64{}},
		temp:       &temperatureAlert{handler: newHandler(ctx, "Temperature")},
		network:    networkAlert{newHandler(ctx, "Network")},
		processes:  &processAlerts{handler: newHandler(ctx, "Processes")},
		freeze:     freezeCheck{newHandler(ctx, "Freeze")},
		media:      &mediaCheck{handler: newHandler(ctx, "Disk")},
		torrents:   &torrentWatcher{handler: newHandler(ctx, "QBittorrent"), tracked: map[string]*trackedTorrent{}},
		logs:       &logCleanup{handler: newHandler(ctx, "Logs"), lastAlert: map[string]float64{}, cleaned: map[string]bool{}},
		kernel:     &kernelChecks{handler: newHandler(ctx, "Kernel"), lastHardwareAlert: map[string]float64{}},
		updates:    &updateScheduler{handler: newHandler(ctx, "Updates")},
		services:   &serviceChecks{handler: newHandler(ctx, "Services"), slots: make(chan struct{}, maxParallelChecks)},
		plex:       &plexWatcher{handler: newHandler(ctx, "Plex")},
		units:      &serviceWatcher{handler: newHandler(ctx, "Systemd")},
		drives:     &driveWatcher{handler: newHandler(ctx, "Disk")},
	}
}

// Wait blocks until background work started by earlier cycles (uptime checks, and for up to a
// minute a running update) has finished; call it before closing what that work writes to.
func (l *Loop) Wait() {
	l.services.wait()
	l.units.wg.Wait()
	l.drives.wg.Wait()
	if l.ctx.Updates != nil {
		l.ctx.Updates.Wait(time.Minute)
	}
}

// Run executes cycles until ctx is cancelled. A failing cycle is logged and reported, never fatal.
func (l *Loop) Run(stop context.Context) {
	for {
		l.safeCycle()
		select {
		case <-stop.Done():
			return
		// Read every time, so a check interval changed in the dashboard applies at once.
		case <-time.After(time.Duration(l.ctx.Cfg.CheckInterval * float64(time.Second))):
		}
	}
}

func (l *Loop) safeCycle() {
	defer func() {
		if p := recover(); p != nil {
			err := fmt.Errorf("panic: %v", p)
			log.Error("Unhandled error in monitoring loop iteration", "err", err)
			l.ctx.ReportError(err, "Monitoring loop iteration")
			l.ctx.Heartbeat()
		}
	}()
	l.RunCycle()
}

// RunCycle collects one snapshot, runs disk cleanup, then evaluates it.
func (l *Loop) RunCycle() {
	ctx := l.ctx
	if ctx.applyPending() {
		log.Info("Applied settings changed in the dashboard")
	}
	ctx.Heartbeat()
	if ctx.Watchdog != nil {
		ctx.Watchdog.ResetAlertState()
	}
	now := time.Now()
	s := &Snapshot{Time: now, Now: float64(now.UnixNano()) / 1e9}
	if ctx.Cfg.FreezeMonitorEnabled && ctx.Freeze != nil {
		ctx.Freeze.Heartbeat()
	}

	l.collector.collectCore(s)
	l.disk.check(s)
	l.collector.collectPeripherals(s)
	l.collector.logStatus(s)
	l.Evaluate(s)
}

// Evaluate runs every alert handler on a collected snapshot and publishes it. The order is the alert order.
func (l *Loop) Evaluate(s *Snapshot) {
	ctx := l.ctx
	l.reportMetrics(s.Now)
	l.cpu.check(s)
	l.memory.check(s)
	l.gpu.check(s)
	l.diskAlerts.alertPartitions(s)
	l.diskAlerts.alertTotal(s)
	l.partitions.check(s)
	l.temp.check(s)
	l.network.check(s)
	l.processes.check(s)
	l.freeze.check(s)
	l.media.check(s)
	l.torrents.check(s)
	l.logs.check(s)
	l.kernel.checkStability(s)
	l.kernel.checkCrashes(s)
	l.updates.check(s)
	l.services.check(s)
	l.plex.check(s)
	l.units.check(s)
	l.drives.check(s)
	if ctx.Notifications != nil {
		ctx.Notifications.Tick()
	}
	ctx.Analytics.Add(analytics.Local, localReading(s))
	ctx.Analytics.Prune(time.Duration(ctx.Config().HistoryRetentionDays) * 24 * time.Hour)
	ctx.setLatest(s)
}

// localReading is this machine's snapshot in analytics' terms; Disk is the fullest partition.
func localReading(s *Snapshot) analytics.Reading {
	r := analytics.Reading{Up: true, CPU: &s.CPUPercent, Mem: &s.Memory.Percent, Temp: s.CPUTemp}
	for _, p := range s.Partitions {
		if r.Disk == nil || p.Percent > *r.Disk {
			v := p.Percent
			r.Disk = &v
		}
	}
	if s.Bandwidth != nil {
		r.Rx, r.Tx = &s.Bandwidth.RecvPerSec, &s.Bandwidth.SentPerSec
	}
	return r
}

func (l *Loop) reportMetrics(now float64) {
	ctx := l.ctx
	if ctx.Webhook == nil || now-l.lastMetricsReport < ctx.Cfg.MetricsReportInterval {
		return
	}
	ok := discord.MetricsReport(ctx.Webhook, ctx.Metrics.Summary())
	ctx.Metrics.RecordWebhookResult(ok)
	if ok {
		log.Info("Metrics report sent to Discord")
		l.lastMetricsReport = now
	} else {
		// Retry in 5 minutes rather than every cycle while Discord is unreachable.
		log.Warn("Failed to send metrics report to Discord")
		l.lastMetricsReport = now - ctx.Cfg.MetricsReportInterval + metricsRetryDelay
	}
}
