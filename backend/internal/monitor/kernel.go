package monitor

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"servermonitor/internal/discord"
	"servermonitor/internal/kernel"
)

// kernelChecks runs the stability and crash checks on their own cycle counts.
type kernelChecks struct {
	handler
	stabilityCounter, crashCounter int
	stabilitySent                  bool
	lastStabilityAlert             float64
	lastStabilityHash              string
	crashSent                      bool
	lastCrashAlert                 float64
	lastHardwareAlert              map[string]float64
}

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
	fields := []discord.Field{
		field("Risk Level", level, true), field("Risk Score", fmt.Sprint(st.RiskScore), true),
		field("Status", "System showing signs of instability - crash risk elevated", false),
	}
	if len(st.Indicators) > 0 {
		fields = append(fields, field("Warning Indicators", strings.Join(st.Indicators[:min(8, len(st.Indicators))], "\n"), false))
	}
	if len(st.Recommendations) > 0 {
		fields = append(fields, field("Recommended Actions", strings.Join(st.Recommendations[:min(5, len(st.Recommendations))], "\n"), false))
	}
	title := fmt.Sprintf("Kernel Alert: System Stability Risk (%s)", level)
	desc := "System showing signs of instability that may lead to kernel crash"
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

func (h *kernelChecks) due(now float64) bool {
	return !h.crashSent || now-h.lastCrashAlert >= h.ctx.Cfg.AlertCooldown
}

func (h *kernelChecks) mark(now float64) { h.lastCrashAlert, h.crashSent = now, true }

func hardwareKey(e kernel.Event) string { return e.Type + "_" + e.Device + "_" + e.Source }

func (h *kernelChecks) checkCrashes(s *Snapshot) {
	cfg := h.ctx.Cfg
	h.crashCounter++
	if !cfg.KernelMonitorEnabled || h.ctx.Kernel == nil || h.crashCounter < cfg.KernelCheckInterval {
		return
	}
	h.crashCounter = 0
	now := s.Now
	r := h.ctx.Kernel.CheckCrashes(cfg.KernelCheckSinceMinutes)

	if r.Reboot != nil && h.due(now) {
		h.alertReboot(*r.Reboot)
		h.mark(now)
	}
	var critical []kernel.Event
	for _, w := range r.EarlyWarnings {
		if w.Severity == "critical" {
			critical = append(critical, w)
		}
	}
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
	if (len(critical) > 0 && h.due(now)) || hardwareDue {
		h.alertEarlyWarnings(critical, r.HardwareErrors, now)
		if len(critical) > 0 {
			h.mark(now)
		}
	}
	if len(r.Crashes) > 0 {
		if h.due(now) {
			h.alertCrashes(r.Crashes)
			h.mark(now)
		}
	} else {
		h.crashSent = false
	}
}

func bootTimeString(ts float64) string {
	if ts == 0 {
		return "Unknown"
	}
	return time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")
}

func (h *kernelChecks) alertReboot(r kernel.Reboot) {
	h.log.Error(fmt.Sprintf("[KERNEL] Unexpected system reboot detected! Previous boot: %s, Current boot: %s", bootTimeString(r.PreviousBootTime), bootTimeString(r.CurrentBootTime)))
	h.ctx.send(h.ctx.CrashWebhook, discord.Critical, "Kernel Alert: Unexpected System Reboot", "System reboot detected - possible kernel crash or unexpected shutdown", []discord.Field{
		field("Event Type", "Unexpected System Reboot", false),
		field("Previous Boot Time", bootTimeString(r.PreviousBootTime), true),
		field("Current Boot Time", bootTimeString(r.CurrentBootTime), true),
		field("Status", "System may have crashed or was unexpectedly rebooted", false),
	}, "Kernel")
}

func (h *kernelChecks) alertEarlyWarnings(critical, hardware []kernel.Event, now float64) {
	h.log.Warn(fmt.Sprintf("[KERNEL] Early warning signs detected: %d critical warnings, %d hardware errors", len(critical), len(hardware)))
	for _, e := range hardware {
		h.lastHardwareAlert[hardwareKey(e)] = now
	}
	if h.ctx.CrashWebhook == nil {
		return
	}
	fields := []discord.Field{
		field("Warning Type", "Early Warning - System may be heading towards crash", false),
		field("Critical Warnings", fmt.Sprint(len(critical)), true),
		field("Hardware Errors", fmt.Sprint(len(hardware)), true),
	}
	if len(hardware) > 0 {
		var lines []string
		for _, e := range hardware[:min(5, len(hardware))] {
			lines = append(lines, DescribeHardwareError(e))
		}
		fields[2] = field(fmt.Sprintf("Hardware Error Details (%d total)", len(hardware)), strings.Join(lines, "\n"), false)
	}
	if len(critical) > 0 {
		var lines []string
		for _, w := range critical[:min(5, len(critical))] {
			lines = append(lines, fmt.Sprintf("**%s**: %s", w.Keyword, discord.Truncate(w.Message, 150)))
		}
		fields = append(fields, field("Critical Warnings", strings.Join(lines, "\n"), false))
	}
	fields = append(fields, field("Action Required", "System showing signs of instability. Monitor closely and investigate hardware/system issues.", false))
	h.ctx.send(h.ctx.CrashWebhook, discord.Warning, "Kernel Alert: Early Warning Signs Detected", "System showing early warning signs that may indicate impending kernel crash", fields, "Kernel Warning")
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
	fields := []discord.Field{field("Total Crashes", fmt.Sprint(len(crashes)), true), field("Check Period", fmt.Sprintf("%d minutes", h.ctx.Cfg.KernelCheckSinceMinutes), true)}
	var details []string
	for _, kind := range order[:min(5, len(order))] {
		items := byType[kind]
		details = append(details, fmt.Sprintf("**%s** (%d occurrence(s)) - Source: %s\n`%s`", kind, len(items), items[0].Source, discord.Truncate(items[0].Message, 200)))
	}
	fields = append(fields, field("Crash Details", strings.Join(details, "\n\n"), false))
	if len(order) > 5 {
		fields = append(fields, field("Note", fmt.Sprintf("Showing first 5 of %d crash types. Check logs for full details.", len(order)), false))
	}
	h.ctx.send(h.ctx.CrashWebhook, discord.Critical, "Kernel Alert: Crash/Panic Detected", "Kernel crashes, panics, or oops detected in system logs", fields, "Kernel")
}
