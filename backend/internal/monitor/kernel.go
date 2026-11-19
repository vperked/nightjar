package monitor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/kernel"
)

// kernelChecks runs the stability and crash checks on their own cycle counts.
type kernelChecks struct {
	handler
	stabilityCounter, crashCounter int
	stabilitySent                  bool
	lastStabilityAlert             float64
	lastStabilityHash              string
	// Critical early warnings have their own cooldown. Reboots and crashes have none: the kernel
	// package hands each over only once, so one held back by a cooldown would be lost for good.
	warningSent      bool
	lastWarningAlert float64
	// heldWarnings are critical warnings seen during the cooldown, sent with the next warning alert.
	heldWarnings      []kernel.Event
	lastHardwareAlert map[string]float64
	// lastFloodAlert is when a kernel flooding its log was last reported.
	lastFloodAlert float64
	// crashCheckRunning is set while a crash check runs, one that timed out included: until it
	// ends, no other kernel check starts, so none waits on it or runs beside it.
	crashCheckRunning atomic.Bool
}

// floodEvery is how often a kernel that keeps flooding its log is reported again.
const floodEvery = 6 * 3600

func formatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.2fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// DescribeHardwareError renders one hardware error line for the early-warning embed.
func DescribeHardwareError(e kernel.Event) string {
	source := e.Source
	if source == "" {
		source = "unknown"
	}
	switch {
	case e.Type == "high_interrupts":
		return fmt.Sprintf("**High Interrupts** on %s: %s interrupts - possible hardware issue", e.Device, formatCount(e.Count))
	case e.CountIncrease != 0:
		return fmt.Sprintf("**%s**: %d new %s errors (total: %d)", source, e.CountIncrease, e.ErrorType, e.Count)
	case e.Count != 0 && e.Device != "":
		return fmt.Sprintf("**%s** (%s): %d %s errors", source, e.Device, e.Count, e.ErrorType)
	case e.Count != 0:
		return fmt.Sprintf("**%s**: %d %s errors", source, e.Count, e.ErrorType)
	case e.Message != "":
		msg := e.Message
		if len(msg) > 200 {
			msg = msg[:197] + "..."
		}
		return fmt.Sprintf("**%s**: %s", source, msg)
	}
	return fmt.Sprintf("**%s**: %s error detected", source, e.Type)
}

func (h *kernelChecks) checkStability(s *Snapshot) {
	cfg := h.ctx.Cfg
	h.stabilityCounter++
	if !cfg.KernelMonitorEnabled || h.ctx.Kernel == nil || h.stabilityCounter < cfg.KernelStabilityCheckInterval {
		return
	}
	if h.crashCheckRunning.Load() {
		return // it would wait on the crash check still running; tried again next cycle
	}
	h.stabilityCounter = 0
	st := h.ctx.Kernel.CheckStability()
	if st.RiskLevel != "high" && st.RiskLevel != "critical" {
		h.stabilitySent, h.lastStabilityHash = false, ""
		return
	}
	if h.stabilitySent && s.Now-h.lastStabilityAlert < cfg.AlertCooldown {
		return
	}
	h.lastStabilityAlert, h.stabilitySent = s.Now, true
	level := strings.ToUpper(st.RiskLevel)
	h.log.Warn(fmt.Sprintf("[KERNEL] System stability risk: %s (score: %d)", level, st.RiskScore))
	if h.ctx.CrashWebhook == nil {
		return
	}
	fields := []discord.Field{field("Risk", fmt.Sprintf("%s · %d", strings.ToLower(level), st.RiskScore), true)}
	if len(st.Indicators) > 0 {
		fields = append(fields, field("Signs", strings.Join(st.Indicators[:min(6, len(st.Indicators))], "\n"), false))
	}
	if len(st.Recommendations) > 0 {
		fields = append(fields, field("What to do", strings.Join(st.Recommendations[:min(4, len(st.Recommendations))], "\n"), false))
	}
	title := "The kernel may crash"
	if st.RiskLevel == "critical" {
		title = "The kernel is close to crashing"
	}
	desc := "The kernel log shows signs of instability."
	sorted := append([]discord.Field(nil), fields...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name+sorted[i].Value < sorted[j].Name+sorted[j].Value })
	raw, _ := json.Marshal(map[string]any{"title": title, "description": desc, "fields": sorted})
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if hash == h.lastStabilityHash {
		return
	}
	sev := discord.Warning
	if st.RiskLevel == "critical" {
		sev = discord.Critical
	}
	if h.ctx.CrashWebhook.SendEmbed(title, desc, sev, fields, "Kernel Stability") {
		h.log.Info("    Discord kernel stability alert sent")
		h.lastStabilityHash = hash
	}
}

// warningDue says whether a critical early warning may alert now.
func (h *kernelChecks) warningDue(now float64) bool {
	return !h.warningSent || now-h.lastWarningAlert >= h.ctx.Cfg.AlertCooldown
}

// maxHeldWarnings caps the critical warnings kept for the next alert; the embed shows five anyway.
const maxHeldWarnings = 50

func hardwareKey(e kernel.Event) string { return e.Type + "_" + e.Device + "_" + e.Source }

func (h *kernelChecks) checkCrashes(s *Snapshot) {
	cfg := h.ctx.Cfg
	h.crashCounter++
	if !cfg.KernelMonitorEnabled || h.ctx.Kernel == nil || h.crashCounter < cfg.KernelCheckInterval {
		return
	}
	// One that timed out may still be running; another beside it would only queue behind it.
	if !h.crashCheckRunning.CompareAndSwap(false, true) {
		h.log.Debug("Skipping the kernel crash check: the last one is still running")
		return
	}
	h.crashCounter = 0
	now := s.Now
	// journalctl and dmesg can take up to 40s between them: the watchdog hears from the loop meanwhile.
	r := withTimeout(h.ctx, 2*time.Minute, kernel.CrashReport{}, "kernel crash check", func() kernel.CrashReport {
		defer h.crashCheckRunning.Store(false)
		return h.ctx.Kernel.CheckCrashes(cfg.KernelCheckSinceMinutes)
	})

	if f := r.Flood; f != nil && now-h.lastFloodAlert >= floodEvery {
		h.lastFloodAlert = now
		h.alertFlood(*f)
	}
	// A reboot is reported once per boot, so it always goes out: after an unexpected one, the
	// crash below is what explains it.
	if r.Reboot != nil {
		h.alertReboot(*r.Reboot)
	}
	critical := h.heldWarnings
	for _, w := range r.EarlyWarnings {
		if w.Severity == "critical" {
			critical = append(critical, w)
		}
	}
	if len(critical) > maxHeldWarnings {
		critical = critical[len(critical)-maxHeldWarnings:]
	}
	h.heldWarnings = nil
	hardwareDue := false
	for _, e := range r.HardwareErrors {
		cooldown := cfg.AlertCooldown
		if e.Severity != "critical" {
			cooldown *= 4
		}
		if now-h.lastHardwareAlert[hardwareKey(e)] >= cooldown {
			hardwareDue = true
		}
	}
	warningDue := len(critical) > 0 && h.warningDue(now)
	switch {
	case warningDue || hardwareDue:
		h.alertEarlyWarnings(critical, r.HardwareErrors, now)
		if len(critical) > 0 {
			h.lastWarningAlert, h.warningSent = now, true
		}
	case len(critical) > 0:
		// Each warning is handed over once: keep it for the alert after the cooldown.
		h.heldWarnings = critical
	}
	// Crashes are already deduplicated, so each new one is told; a cooldown would only drop them.
	if len(r.Crashes) > 0 {
		h.alertCrashes(r.Crashes)
	}
}

// alertFlood says the kernel is writing its log far faster than it should, and what it repeats.
func (h *kernelChecks) alertFlood(f kernel.Flood) {
	h.log.Warn("The kernel is flooding its log", "per_second", int(f.PerSecond), "top", f.Top)
	desc := fmt.Sprintf("It is writing about %s messages a second. That uses CPU and disk, pushes older logs out of the journal, "+
		"and slows anything that reads it; Nightjar skips its journal checks until it stops.", formatCount(int64(f.PerSecond)))
	var fields []discord.Field
	if f.Top != "" {
		fields = append(fields, field(fmt.Sprintf("Repeated most · %.0f%%", f.TopShare*100), discord.Truncate(f.Top, 300), false))
	}
	h.ctx.send(h.ctx.CrashWebhook, discord.Warning, "The kernel is flooding its log", desc, fields, "Kernel")
}

func bootTimeString(ts float64) string {
	if ts == 0 {
		return "Unknown"
	}
	return time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")
}

func (h *kernelChecks) alertReboot(r kernel.Reboot) {
	fields := []discord.Field{
		field("Booted before", bootTimeString(r.PreviousBootTime), true),
		field("Booted now", bootTimeString(r.CurrentBootTime), true),
	}
	if r.Clean {
		h.log.Info(fmt.Sprintf("[KERNEL] The system rebooted after a clean shutdown. Previous boot: %s, Current boot: %s", bootTimeString(r.PreviousBootTime), bootTimeString(r.CurrentBootTime)))
		h.ctx.send(h.ctx.CrashWebhook, discord.OK, "The system rebooted", "It shut down cleanly first, so the reboot was asked for.", fields, "Kernel")
		return
	}
	why := "Its last boot's log shows no clean shutdown: it may have crashed or lost power."
	if !r.Known {
		why = "Its last boot's log could not be read, so whether it crashed, lost power or was rebooted on purpose is unknown."
	}
	h.log.Error(fmt.Sprintf("[KERNEL] The system rebooted without a clean shutdown in the log. Previous boot: %s, Current boot: %s", bootTimeString(r.PreviousBootTime), bootTimeString(r.CurrentBootTime)))
	h.ctx.send(h.ctx.CrashWebhook, discord.Critical, "The system rebooted unexpectedly", why, fields, "Kernel")
}

func (h *kernelChecks) alertEarlyWarnings(critical, hardware []kernel.Event, now float64) {
	h.log.Warn(fmt.Sprintf("[KERNEL] Early warning signs detected: %d critical warnings, %d hardware errors", len(critical), len(hardware)))
	for _, e := range hardware {
		h.lastHardwareAlert[hardwareKey(e)] = now
	}
	if h.ctx.CrashWebhook == nil {
		return
	}
	var fields []discord.Field
	if len(hardware) > 0 {
		var lines []string
		for _, e := range hardware[:min(5, len(hardware))] {
			lines = append(lines, DescribeHardwareError(e))
		}
		fields = append(fields, field(fmt.Sprintf("Hardware errors · %d", len(hardware)), strings.Join(lines, "\n"), false))
	}
	if len(critical) > 0 {
		var lines []string
		for _, w := range critical[:min(5, len(critical))] {
			lines = append(lines, fmt.Sprintf("**%s** %s", w.Keyword, discord.Truncate(w.Message, 150)))
		}
		fields = append(fields, field(fmt.Sprintf("Warnings · %d", len(critical)), strings.Join(lines, "\n"), false))
	}
	h.ctx.send(h.ctx.CrashWebhook, discord.Warning, "The kernel is warning of trouble", "These can come before a crash. Keep an eye on the machine and check its hardware.", fields, "Kernel Warning")
}

func (h *kernelChecks) alertCrashes(crashes []kernel.Event) {
	h.log.Error(fmt.Sprintf("[KERNEL] %d kernel crash/panic detected!", len(crashes)))
	if h.ctx.CrashWebhook == nil {
		return
	}
	var order []string
	byType := map[string][]kernel.Event{}
	for _, c := range crashes {
		if _, ok := byType[c.Keyword]; !ok {
			order = append(order, c.Keyword)
		}
		byType[c.Keyword] = append(byType[c.Keyword], c)
	}
	var details []string
	for _, kind := range order[:min(5, len(order))] {
		items := byType[kind]
		details = append(details, fmt.Sprintf("**%s** ×%d · %s\n`%s`", kind, len(items), items[0].Source, discord.Truncate(items[0].Message, 200)))
	}
	if len(order) > 5 {
		details = append(details, fmt.Sprintf("and %d more kinds; the kernel log has them all.", len(order)-5))
	}
	title := "The kernel crashed"
	if len(crashes) > 1 {
		title = fmt.Sprintf("The kernel crashed %d times", len(crashes))
	}
	h.ctx.send(h.ctx.CrashWebhook, discord.Critical, title, fmt.Sprintf("Seen in the kernel log in the last %d minutes.", h.ctx.Cfg.KernelCheckSinceMinutes),
		[]discord.Field{field("What happened", strings.Join(details, "\n\n"), false)}, "Kernel")
}
