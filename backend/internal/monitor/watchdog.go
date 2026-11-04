package monitor

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"nightjar/internal/collect"
	"nightjar/internal/discord"
)

// FreezeMonitor flags a frozen host: a stale heartbeat, metrics that stopped changing, or slow system calls.
type FreezeMonitor struct {
	mu               sync.Mutex
	Threshold        float64
	HeartbeatTimeout float64
	lastHeartbeat    time.Time
	lastMetricUpdate time.Time
	lastCPU, lastMem float64
	haveMetrics      bool
	lastDiskIO       *collect.IO
	lastNetIO        *collect.IO
}

func NewFreezeMonitor(threshold, heartbeatTimeout float64) *FreezeMonitor {
	return &FreezeMonitor{Threshold: max(10, threshold), HeartbeatTimeout: max(20, heartbeatTimeout), lastHeartbeat: time.Now()}
}

func (f *FreezeMonitor) Heartbeat() {
	f.mu.Lock()
	f.lastHeartbeat = time.Now()
	f.mu.Unlock()
}

// UpdateMetrics records this cycle's readings; any change (or an idle CPU, where flat
// readings are expected) counts as progress.
func (f *FreezeMonitor) UpdateMetrics(cpuPct, memPct float64, diskIO, netIO *collect.IO) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := !f.haveMetrics || math.Abs(cpuPct-f.lastCPU) > 0.1 || math.Abs(memPct-f.lastMem) > 0.1
	f.lastCPU, f.lastMem, f.haveMetrics = cpuPct, memPct, true
	if diskIO != nil {
		changed = changed || f.lastDiskIO == nil || *diskIO != *f.lastDiskIO
		d := *diskIO
		f.lastDiskIO = &d
	}
	if netIO != nil {
		changed = changed || f.lastNetIO == nil || netIO.BytesSent != f.lastNetIO.BytesSent || netIO.BytesRecv != f.lastNetIO.BytesRecv
		n := *netIO
		f.lastNetIO = &n
	}
	if changed || f.lastMetricUpdate.IsZero() || cpuPct < 15 {
		f.lastMetricUpdate = time.Now()
	}
}

// FreezeStatus is one freeze check.
type FreezeStatus struct {
	Frozen         bool
	Reasons        []string
	SinceHeartbeat float64
	Responsive     bool
}

func (f *FreezeMonitor) Check() FreezeStatus {
	f.mu.Lock()
	sinceHeartbeat := time.Since(f.lastHeartbeat).Seconds()
	lastUpdate := f.lastMetricUpdate
	f.mu.Unlock()

	s := FreezeStatus{SinceHeartbeat: sinceHeartbeat, Responsive: true}
	if sinceHeartbeat > f.HeartbeatTimeout {
		s.Reasons = append(s.Reasons, fmt.Sprintf("Heartbeat timeout (%.1fs > %.1fs)", sinceHeartbeat, f.HeartbeatTimeout))
	}
	if !lastUpdate.IsZero() {
		if stalled := time.Since(lastUpdate).Seconds(); stalled > f.Threshold {
			s.Reasons = append(s.Reasons, fmt.Sprintf("Metrics stalled (%.1fs > %.1fs)", stalled, f.Threshold))
		}
	}
	const timeout = 2.0
	start := time.Now()
	_, cpuErr := cpu.Percent(0, false)
	cpuTime := time.Since(start).Seconds()
	memStart := time.Now()
	_, memErr := mem.VirtualMemory()
	memTime := time.Since(memStart).Seconds()
	switch {
	case cpuErr != nil || memErr != nil:
		s.Responsive = false
		s.Reasons = append(s.Reasons, fmt.Sprintf("System unresponsive: %v", firstErr(cpuErr, memErr)))
	case cpuTime > timeout || memTime > timeout || time.Since(start).Seconds() > 2*timeout:
		s.Responsive = false
		s.Reasons = append(s.Reasons, fmt.Sprintf("System unresponsive: System calls taking too long (CPU: %.2fs, Mem: %.2fs)", cpuTime, memTime))
	}
	s.Frozen = len(s.Reasons) > 0
	return s
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// Watchdog alerts (and optionally restarts the systemd service) when the monitoring loop stops beating.
type Watchdog struct {
	Timeout         time.Duration
	AutoRestart     bool
	RestartCooldown time.Duration
	ServiceName     string
	AlertCooldown   time.Duration
	Webhook         discord.Sender

	mu           sync.Mutex
	lastBeat     time.Time
	alertSent    bool
	lastAlert    time.Time
	lastRestart  time.Time
	restartCount int
	stop         chan struct{}
	stopOnce     sync.Once
}

func (w *Watchdog) Heartbeat() {
	w.mu.Lock()
	w.lastBeat = time.Now()
	w.mu.Unlock()
}

// ResetAlertState re-arms the alert once the loop is beating again.
func (w *Watchdog) ResetAlertState() {
	w.mu.Lock()
	w.alertSent = false
	w.mu.Unlock()
}

func (w *Watchdog) Start() {
	w.Heartbeat()
	w.stop = make(chan struct{})
	go w.loop()
	info := fmt.Sprintf("Monitoring watchdog started (timeout: %.1fs", w.Timeout.Seconds())
	if w.AutoRestart {
		info += fmt.Sprintf(", auto-restart: ENABLED (cooldown: %.1fs)", w.RestartCooldown.Seconds())
	}
	log.Info(info + ")")
}

func (w *Watchdog) Stop() {
	if w.stop != nil {
		w.stopOnce.Do(func() { close(w.stop) })
	}
}

func runningUnderSystemd() bool {
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	if p, err := process.NewProcess(int32(os.Getppid())); err == nil {
		if name, err := p.Name(); err == nil && strings.Contains(strings.ToLower(name), "systemd") {
			return true
		}
	}
	return false
}

func (w *Watchdog) restart() bool {
	if _, err := exec.LookPath("systemctl"); err != nil || !runningUnderSystemd() {
		log.Warn("[WATCHDOG] Cannot restart: not running as a systemd service")
		return false
	}
	log.Info("[WATCHDOG] Attempting to restart service: " + w.ServiceName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", w.ServiceName+".service").CombinedOutput(); err != nil {
		log.Error("[WATCHDOG] Failed to restart service", "err", strings.TrimSpace(string(out)))
		return false
	}
	return true
}

func (w *Watchdog) loop() {
	ticker := time.NewTicker(w.Timeout)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		w.mu.Lock()
		since := time.Since(w.lastBeat)
		due := since > w.Timeout && (!w.alertSent || time.Since(w.lastAlert) >= w.AlertCooldown)
		w.mu.Unlock()
		if !due {
			continue
		}
		log.Error(fmt.Sprintf("[WATCHDOG] CRITICAL: Monitoring loop appears to have stopped! No heartbeat for %.1fs (timeout: %.1fs)", since.Seconds(), w.Timeout.Seconds()))

		attempted, restarted := false, false
		if w.AutoRestart {
			if time.Since(w.lastRestart) >= w.RestartCooldown {
				attempted = true
				restarted = w.restart()
				w.lastRestart = time.Now()
				w.restartCount++
			} else {
				log.Warn(fmt.Sprintf("[WATCHDOG] Auto-restart on cooldown (%.1fs remaining)", (w.RestartCooldown - time.Since(w.lastRestart)).Seconds()))
			}
		}
		if w.Webhook != nil {
			fields := []discord.Field{
				{Name: "Last heartbeat", Value: discord.Span(since.Seconds()) + " ago", Inline: true},
				{Name: "Allowed", Value: discord.Span(w.Timeout.Seconds()), Inline: true},
			}
			desc := fmt.Sprintf("Its checks have not run for %s, so nothing is being watched.", discord.Span(since.Seconds()))
			if attempted {
				outcome := map[bool]string{true: "restarted it", false: "tried to restart it, and failed"}[restarted]
				fields = append(fields, discord.Field{Name: "Restarts", Value: fmt.Sprint(w.restartCount), Inline: true})
				desc += " The watchdog " + outcome + "."
			}
			if w.Webhook.SendEmbed("Nightjar stopped checking", desc, discord.Critical, fields, "Watchdog") {
				log.Info("    Discord watchdog alert sent")
			}
		}
		w.mu.Lock()
		w.alertSent, w.lastAlert = true, time.Now()
		w.mu.Unlock()
	}
}
