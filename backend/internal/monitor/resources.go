package monitor

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"nightjar/internal/cleanup"
	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/format"
)

const (
	processCheckInterval = 3
	maxProcessesDisplay  = 5
)

// collector fills a Snapshot each cycle.
type collector struct {
	handler
	lastStatus time.Time // when the readings were last logged at info
	// partitioning is set while a partition read runs. One stuck on a dead mount outlives its
	// timeout; until it returns, no other is started, so stuck reads cannot pile up cycle by cycle.
	partitioning atomic.Bool
}

// statusEvery is how often the readings are logged at info; every cycle logs them at debug.
const statusEvery = 15 * time.Minute

func (c *collector) collectCore(s *Snapshot) {
	ctx := c.ctx
	ctx.Metrics.StartExecution("cpu")
	if per, err := collect.PerCore(); err == nil && len(per) > 0 {
		s.PerCore = per
		sum := 0.0
		for _, v := range per {
			sum += v
		}
		s.CPUPercent = sum / float64(len(per))
	} else if err != nil {
		ctx.Metrics.RecordError("cpu_monitor")
		c.log.Error("Error getting CPU metrics", "err", err)
	}
	ctx.Metrics.EndExecution("cpu")
	ctx.Heartbeat()

	ctx.Metrics.StartExecution("memory")
	if m, err := collect.VirtualMemory(); err == nil {
		s.Memory, s.Swap = m, func() collect.Memory { sw, _ := collect.Swap(); return sw }()
	} else {
		ctx.Metrics.RecordError("memory_monitor")
		c.log.Error("Error getting memory metrics", "err", err)
	}
	ctx.Metrics.EndExecution("memory")
	ctx.Heartbeat()

	s.GPU = withTimeout(ctx, 20*time.Second, (*collect.GPU)(nil), "GPU data collection", ctx.GPU.Read)

	ctx.Metrics.StartExecution("disk")
	if c.partitioning.CompareAndSwap(false, true) {
		s.Partitions = withTimeout(ctx, 10*time.Second, []collect.Partition(nil), "disk partitions collection", func() []collect.Partition {
			defer c.partitioning.Store(false)
			return collect.Partitions(true)
		})
	} else {
		ctx.Metrics.RecordError("disk_monitor")
		c.log.Warn("Disk usage unknown: an earlier read of the partitions is still stuck, likely on an unresponsive mount")
	}
	s.TotalDisk = collect.TotalDisk(s.Partitions)
	s.DiskPercent = s.TotalDisk.Percent
	ctx.Metrics.EndExecution("disk")
	ctx.Heartbeat()
}

func (c *collector) collectPeripherals(s *Snapshot) {
	ctx := c.ctx
	if ctx.Sensors.Available() {
		s.CPUTemp = ctx.Sensors.CPUTemperature()
	}
	// The loop is the meter's only reader, so each rate covers exactly one cycle.
	if bw, ok := ctx.Bandwidth.Read(); ok {
		s.Bandwidth = &bw
		mbps := (bw.SentPerSec + bw.RecvPerSec) / (1024 * 1024)
		s.NetworkMBps = &mbps
	}
	if io, err := collect.NetworkIO(); err == nil {
		s.NetworkIO = &io
	}
	if io, err := collect.DiskIO(); err == nil {
		s.DiskIO = &io
	}
	if ctx.Cfg.FreezeMonitorEnabled && ctx.Freeze != nil {
		ctx.Freeze.UpdateMetrics(s.CPUPercent, s.Memory.Percent, s.DiskIO, s.NetworkIO)
	}
	ctx.Heartbeat()
}

// logStatus logs the cycle's readings: at debug every cycle, and at info every quarter hour so the
// log shows the machine's state over time without a line every few seconds.
func (c *collector) logStatus(s *Snapshot) {
	attrs := []any{"cpu", pct(s.CPUPercent), "memory", pct(s.Memory.Percent)}
	for _, p := range s.Partitions {
		name := p.Mountpoint
		if name == "/" {
			name = "root"
		} else if i := strings.LastIndex(name, "/"); i >= 0 && i < len(name)-1 {
			name = name[i+1:]
		}
		attrs = append(attrs, "disk."+name, pct(p.Percent))
	}
	if len(s.Partitions) == 0 {
		attrs = append(attrs, "disk", pct(s.DiskPercent))
	}
	if s.GPU != nil {
		attrs = append(attrs, "gpu", pct(s.GPU.Utilization))
	}
	if s.CPUTemp != nil {
		attrs = append(attrs, "temp", fmt.Sprintf("%.0f°C", *s.CPUTemp))
	}
	if s.NetworkMBps != nil {
		attrs = append(attrs, "network", fmt.Sprintf("%.1fMB/s", *s.NetworkMBps))
	}
	if s.Time.Sub(c.lastStatus) >= statusEvery {
		c.lastStatus = s.Time
		c.log.Info("Readings", attrs...)
		return
	}
	c.log.Debug("Readings", attrs...)
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

type cpuAlert struct{ handler }

func (h cpuAlert) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	if s.CPUPercent < cfg.CPUThreshold {
		return
	}
	h.raiseAlert("cpu", s.Now, fmt.Sprintf("CPU: %.1f%% (threshold: %.1f%%)", s.CPUPercent, cfg.CPUThreshold), h.ctx.Webhook,
		func() bool { return discord.CPUAlert(h.ctx.Webhook, s.CPUPercent, cfg.CPUThreshold, s.PerCore) }, "cpu", true)
	var hot []string
	for i, u := range s.PerCore {
		if u >= cfg.CPUThreshold {
			hot = append(hot, fmt.Sprintf("Core %d: %.1f%%", i, u))
		}
	}
	if len(hot) > 0 {
		h.log.Debug("High core usage: " + strings.Join(hot, ", "))
	}
}

type memoryAlert struct{ handler }

func (h memoryAlert) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	if s.Memory.Percent < cfg.MemoryThreshold {
		return
	}
	h.raiseAlert("memory", s.Now, fmt.Sprintf("Memory: %.1f%% (threshold: %.1f%%)", s.Memory.Percent, cfg.MemoryThreshold), h.ctx.Webhook,
		func() bool {
			return discord.MemoryAlert(h.ctx.Webhook, s.Memory.Percent, cfg.MemoryThreshold, s.Memory, s.Swap)
		}, "memory", true)
}

type gpuAlert struct{ handler }

func (h gpuAlert) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	if s.GPU == nil || s.GPU.Utilization < cfg.GPUThreshold {
		return
	}
	h.raiseAlert("gpu", s.Now, fmt.Sprintf("GPU: %.1f%% (threshold: %.1f%%)", s.GPU.Utilization, cfg.GPUThreshold), h.ctx.Webhook,
		func() bool { return discord.GPUAlert(h.ctx.Webhook, *s.GPU, cfg.GPUThreshold) }, "gpu", false)
}

type temperatureAlert struct {
	handler
	highSince *float64
	disk, net counterRate
}

// counterRate turns a pair of cumulative counters into per-second rates between readings.
type counterRate struct {
	a, b     uint64
	at       float64
	aPS, bPS float64
}

func (c *counterRate) update(a, b uint64, now float64) {
	if c.at > 0 && now > c.at && a >= c.a && b >= c.b {
		c.aPS, c.bPS = float64(a-c.a)/(now-c.at), float64(b-c.b)/(now-c.at)
	}
	c.a, c.b, c.at = a, b, now
}

func (h *temperatureAlert) check(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if s.DiskIO != nil {
		h.disk.update(s.DiskIO.ReadBytes, s.DiskIO.WriteBytes, s.Now)
	}
	if s.NetworkIO != nil {
		h.net.update(s.NetworkIO.BytesRecv, s.NetworkIO.BytesSent, s.Now)
	}
	if s.CPUTemp == nil || *s.CPUTemp < cfg.TempThreshold {
		h.highSince = nil
		return
	}
	if h.highSince == nil {
		now := s.Now
		h.highSince = &now
	}
	duration := s.Now - *h.highSince
	if duration < cfg.TempAlertDuration {
		h.log.Debug(fmt.Sprintf("Temperature %.1fC, waiting %.0fs before alert", *s.CPUTemp, cfg.TempAlertDuration-duration))
		return
	}
	send := func() bool {
		if s.TopCPU == nil {
			s.TopCPU = ctx.Processes.Overview(maxProcessesDisplay).TopCPU
		}
		freq := collect.Freq()
		fans := ctx.Sensors.Fans()
		in := TemperatureInputs{
			CPUPercent: s.CPUPercent, Freq: freq, PerCore: s.PerCore, Top: s.TopCPU, Load: collect.LoadAverage(),
			Fans: fans, DiskReadBps: h.disk.aPS, DiskWriteBps: h.disk.bPS, NetRecvBps: h.net.aPS, NetSentBps: h.net.bPS,
		}
		if in.Fans == nil {
			in.Fans = []collect.Fan{}
		}
		if s.GPU != nil {
			in.GPUTemp = s.GPU.Temperature
		}
		reason := AnalyzeTemperatureReason(in)
		h.log.Info("    Likely cause: " + reason)
		return discord.TemperatureAlert(ctx.Webhook, discord.TemperatureDetails{
			Temperature: *s.CPUTemp, Threshold: cfg.TempThreshold, Duration: duration, CPUPercent: s.CPUPercent,
			Reason: reason, Freq: freq, TopProcesses: s.TopCPU, Fans: fans,
		})
	}
	h.raiseAlert("temp", s.Now, fmt.Sprintf("Temperature: %.1fC (threshold: %.1fC) for %.0fs", *s.CPUTemp, cfg.TempThreshold, duration),
		ctx.Webhook, send, "temperature", true)
}

type networkAlert struct{ handler }

func (h networkAlert) check(s *Snapshot) {
	threshold := h.ctx.Cfg.NetworkBandwidthThreshold
	if s.NetworkMBps == nil || *s.NetworkMBps < threshold {
		return
	}
	send := func() bool {
		var fields []discord.Field
		if s.Bandwidth != nil {
			fields = append(fields,
				discord.Field{Name: "Download", Value: format.BytesPerSecond(s.Bandwidth.RecvPerSec), Inline: true},
				discord.Field{Name: "Upload", Value: format.BytesPerSecond(s.Bandwidth.SentPerSec), Inline: true})
		}
		return h.ctx.Webhook.SendEmbed(fmt.Sprintf("Network at %.1f MB/s", *s.NetworkMBps), fmt.Sprintf("Above the %.1f MB/s limit.", threshold),
			discord.Warning, fields, "")
	}
	h.raiseAlert("network", s.Now, fmt.Sprintf("Network: %.1f MB/s (threshold: %.1f MB/s)", *s.NetworkMBps, threshold), h.ctx.Webhook, send, "", false)
}

type processAlerts struct {
	handler
	counter int
}

func (h *processAlerts) check(s *Snapshot) {
	h.counter++
	if h.counter < processCheckInterval {
		return
	}
	h.counter = 0
	o := h.ctx.Processes.Overview(maxProcessesDisplay)
	s.TopCPU, s.TopMemory, s.ProcessTotal = o.TopCPU, o.TopMemory, o.Total
	cfg := h.ctx.Cfg
	h.alert(s.TopCPU, "cpu", "CPU", cfg.ProcessCPUThreshold, s.Now)
	h.alert(s.TopMemory, "memory", "Memory", cfg.ProcessMemoryThreshold, s.Now)
}

func (h *processAlerts) alert(procs []collect.Process, resource, label string, threshold, now float64) {
	for _, p := range procs {
		usage := p.CPUPercent
		if resource == "memory" {
			usage = p.MemoryPercent
		}
		if usage < threshold {
			continue
		}
		h.raiseAlert(fmt.Sprintf("proc:%s_%d", resource, p.PID), now,
			fmt.Sprintf("Process %s (PID: %d): %s %.1f%% (threshold: %.1f%%)", p.Name, p.PID, label, usage, threshold),
			h.ctx.Webhook, func() bool { return discord.ProcessAlert(h.ctx.Webhook, p, resource, threshold) }, "", false)
	}
}

type freezeCheck struct{ handler }

func (h freezeCheck) check(s *Snapshot) {
	ctx := h.ctx
	if !ctx.Cfg.FreezeMonitorEnabled || ctx.Freeze == nil {
		return
	}
	st := ctx.Freeze.Check()
	if !st.Frozen {
		return
	}
	h.raiseAlert("freeze", s.Now, "System Freeze Detected: "+strings.Join(st.Reasons, ", "), ctx.Webhook, func() bool {
		return discord.FreezeAlert(ctx.Webhook, discord.FreezeDetails{
			Reasons: st.Reasons, SinceHeartbeat: st.SinceHeartbeat, Responsive: st.Responsive,
			UptimeFormatted: collect.SystemInfo().UptimeFormatted, CPUPercent: s.CPUPercent, MemPct: s.Memory.Percent,
		})
	}, "", false)
}

type mediaCheck struct {
	handler
	counter int
	// sizing is set while the directory is being sized. On a network share that stopped answering
	// that can outlive its timeout; until it returns, no other is started.
	sizing atomic.Bool
}

// mediaSize is the media directory's size; partial when the walk ran out of time, so the
// directory is at least that big.
type mediaSize struct {
	size           int64
	found, partial bool
}

func (h *mediaCheck) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	h.counter++
	if !cfg.MediaMonitorEnabled || h.counter < cfg.MediaCheckInterval {
		return
	}
	h.counter = 0
	if !h.sizing.CompareAndSwap(false, true) {
		h.log.Warn("Still sizing " + cfg.MediaDirectory + " from an earlier check; is the mount responding?")
		return
	}
	// Stat and size off the loop: either can hang on a share that stopped answering.
	m := withTimeout(h.ctx, 90*time.Second, mediaSize{}, "sizing "+cfg.MediaDirectory, func() mediaSize {
		defer h.sizing.Store(false)
		if _, err := os.Stat(cfg.MediaDirectory); err != nil {
			return mediaSize{}
		}
		size, partial := cleanup.DirSize(cfg.MediaDirectory, 60*time.Second)
		return mediaSize{size: size, found: true, partial: partial}
	})
	if !m.found {
		return
	}
	size := m.size
	if m.partial {
		h.log.Info(fmt.Sprintf("Sizing %s ran out of time: it holds at least %s", cfg.MediaDirectory, format.Bytes(float64(size))))
	}
	if size < cfg.MediaDirectoryThresholdBytes {
		return
	}
	// Looked at every MediaCheckInterval cycles, so the episode must outlast that.
	every := float64(cfg.MediaCheckInterval) * cfg.CheckInterval
	h.raiseAlertEvery("media", s.Now, every, fmt.Sprintf("Directory %s: %s (threshold: %s)", cfg.MediaDirectory, format.Bytes(float64(size)), format.Bytes(float64(cfg.MediaDirectoryThresholdBytes))),
		h.ctx.DiskWebhook, func() bool {
			var subs []discord.Subdirectory
			for _, d := range h.largest(cfg.MediaDirectory, 5, 30*time.Second) {
				subs = append(subs, discord.Subdirectory{Path: d.Path, Size: d.Size})
			}
			return discord.DirectorySizeAlert(h.ctx.DiskWebhook, cfg.MediaDirectory, size, cfg.MediaDirectoryThresholdBytes, subs)
		}, "", false)
}
