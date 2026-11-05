package monitor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"nightjar/internal/cleanup"
	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/format"
)

const (
	targetFreePercent         = 20.0
	logCleanupInterval        = 1800.0
	diskCleanupCooldownHigh   = 300.0
	diskCleanupCooldownMedium = 900.0
	diskCleanupCooldownLow    = 1800.0
	diskCleanupCooldownNormal = 3600.0
	diskEmergencyCooldown     = 300.0
	maxDirectoriesDisplay     = 5
)

func cleanupCounts(st cleanup.Stats) string {
	return fmt.Sprintf("(%d files, %d dirs, %d logs)", st.FilesRemoved, st.DirsRemoved, st.LogsCleaned)
}

func pctOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func usageFields(st cleanup.Stats, fallback float64) []discord.Field {
	return []discord.Field{
		field("Usage", fmt.Sprintf("%.0f%% → %.0f%%", pctOr(st.InitialUsage, fallback), pctOr(st.FinalUsage, fallback)), true),
		field("Freed", format.Bytes(float64(st.BytesCleared)), true),
		field("Files removed", fmt.Sprint(st.FilesRemoved), true),
	}
}

func errorsField(st cleanup.Stats, during string) []discord.Field {
	if len(st.Errors) == 0 {
		return nil
	}
	return []discord.Field{field("Errors", fmt.Sprintf("%d during the %s; the monitor log has them.", len(st.Errors), during), false)}
}

func largestDirsField(dirs []cleanup.DirEntry) []discord.Field {
	if len(dirs) == 0 {
		return nil
	}
	lines := make([]string, len(dirs))
	for i, d := range dirs {
		lines[i] = fmt.Sprintf("`%s` · %s", filepath.Base(d.Path), format.Bytes(float64(d.Size)))
	}
	return []discord.Field{field("Largest inside", strings.Join(lines, "\n"), false)}
}

func cleanupCooldown(percent float64) float64 {
	switch {
	case percent >= 80:
		return diskCleanupCooldownHigh
	case percent >= 70:
		return diskCleanupCooldownMedium
	case percent >= 60:
		return diskCleanupCooldownLow
	}
	return diskCleanupCooldownNormal
}

type diskCleanup struct {
	handler
	lastCleanup float64
}

// rootPercent is how full the root filesystem is. What disk cleanup frees (logs, temporary files,
// caches) lives there, so another disk filling up, or the total across disks, must not set it off.
func rootPercent(s *Snapshot) float64 {
	for _, p := range s.Partitions {
		if p.Mountpoint == "/" {
			return p.Percent
		}
	}
	if u, err := collect.DiskUsage("/"); err == nil {
		return u.Percent
	}
	return 0
}

func (h *diskCleanup) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	percent := rootPercent(s)
	since := s.Now - h.lastCleanup
	if percent >= cfg.DiskCleanupThreshold && since >= logCleanupInterval {
		h.cleanLogs(s, percent)
		since = 0
	}
	switch {
	case percent >= cfg.DiskEmergencyThreshold:
		if since >= diskEmergencyCooldown {
			h.emergency(s, percent)
		}
	case percent >= cfg.DiskCleanupThreshold:
		if cd := cleanupCooldown(percent); since >= cd {
			h.routine(s, percent)
		} else {
			h.log.Debug(fmt.Sprintf("Disk cleanup cooldown: %.0fs", cd-since))
		}
	}
}

func (h *diskCleanup) cleanLogs(s *Snapshot, percent float64) {
	ctx := h.ctx
	h.log.Warn(fmt.Sprintf("[CLEANUP] Disk at %.1f%% - starting log cleanup...", percent))
	st := withTimeout(h.ctx, 5*time.Minute, cleanup.Stats{}, "log cleanup operation", ctx.Cleaner.CleanLogsAndRestart)
	ctx.Heartbeat()
	if st.LogsCleaned > 0 {
		h.log.Warn(fmt.Sprintf("[CLEANUP] Cleared %d log file(s), freed %s", st.LogsCleaned, format.Bytes(float64(st.BytesCleared))))
		if u, err := collect.DiskUsage("/"); err == nil {
			h.log.Info(fmt.Sprintf("[CLEANUP] Disk usage after log cleanup: %.1f%%", u.Percent))
		}
	}
	h.lastCleanup = s.Now
}

// notify sends what a cleanup did through the alert gate under key, so a disk that stays full
// does not repeat it on every run (every interval seconds) but backs off like any other alert.
func (h handler) notify(key string, now, interval float64, sev discord.Severity, title, desc string, fields []discord.Field, footer string) {
	ctx := h.ctx
	h.raiseAlertEvery(key, now, interval, title, ctx.DiskWebhook, func() bool {
		return ctx.send(ctx.DiskWebhook, sev, title, desc, fields, footer)
	}, "", false)
}

func (h *diskCleanup) emergency(s *Snapshot, percent float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	threshold := cfg.DiskEmergencyThreshold
	h.log.Error(fmt.Sprintf("[EMERGENCY] Disk usage at %.1f%% (emergency threshold: %.1f%%) - CRITICAL! Starting emergency cleanup...", percent, threshold))
	st := withTimeout(h.ctx, 10*time.Minute, cleanup.Stats{}, "emergency disk cleanup", func() cleanup.Stats { return ctx.Cleaner.EmergencyClean("/") })
	ctx.Heartbeat()
	h.lastCleanup = s.Now
	const key = "cleanup-emergency:/"
	if st.BytesCleared == 0 {
		h.log.Error("No space could be freed; the disk may fill completely")
		h.notify(key, s.Now, diskEmergencyCooldown, discord.Critical, fmt.Sprintf("The disk is %.0f%% full, and cleanup freed nothing", percent),
			fmt.Sprintf("Emergency cleanup starts at %.0f%%, but found nothing it may remove. Free space by hand now.", threshold),
			nil, "Critical Alert")
		return
	}
	const stillFull = "Disk still critically full after cleanup! Manual intervention required!"
	h.log.Error(fmt.Sprintf("[EMERGENCY] Emergency cleanup cleared %s %s", format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	final := pctOr(st.FinalUsage, percent)
	if st.FinalUsage != nil {
		h.log.Error(fmt.Sprintf("[EMERGENCY] Disk usage after emergency cleanup: %.1f%%", final))
		if final >= threshold {
			h.log.Error("[EMERGENCY] " + stillFull)
		}
	}
	fields := append(usageFields(st, percent), field("Logs emptied", fmt.Sprint(st.LogsCleaned), true))
	fields = append(fields, errorsField(st, "emergency cleanup")...)
	desc := "Nightjar ran an emergency cleanup so it would not reach 100%."
	if final >= threshold {
		desc += " It is still too full: free space by hand."
	}
	h.notify(key, s.Now, diskEmergencyCooldown, discord.Critical, fmt.Sprintf("The disk was %.0f%% full", pctOr(st.InitialUsage, percent)), desc, fields, "Emergency")
}

func (h *diskCleanup) routine(s *Snapshot, percent float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	h.log.Warn(fmt.Sprintf("[CLEANUP] Disk usage at %.1f%% (cleanup threshold: %.1f%%), starting automatic cleanup...", percent, cfg.DiskCleanupThreshold))
	st := withTimeout(h.ctx, 5*time.Minute, cleanup.Stats{}, "disk cleanup operation", func() cleanup.Stats { return ctx.Cleaner.CleanDiskSpace("/", targetFreePercent) })
	h.lastCleanup = s.Now
	if st.BytesCleared == 0 {
		h.log.Info("[CLEANUP] No files were cleaned (disk may need manual intervention)")
		return
	}
	h.log.Info(fmt.Sprintf("[CLEANUP] Cleaned %s %s", format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	fields := append(usageFields(st, percent), errorsField(st, "cleanup")...)
	h.notify("cleanup:/", s.Now, cleanupCooldown(percent), discord.Info, "Freed "+format.Bytes(float64(st.BytesCleared))+" on the disk",
		"Nightjar cleaned up before the disk filled.", fields, "")
}

type diskAlerts struct{ handler }

func (h diskAlerts) gateOK(key string, now float64) bool {
	gate := h.ctx.Gates.Get(key, now)
	if !gate.Allow(now) {
		h.log.Debug(fmt.Sprintf("%s cooldown: %.0fs", key, gate.SecondsUntilNext(now)))
		return false
	}
	return true
}

// largestBudget is the longest an alert waits for the largest directories on its disk; the alert
// goes without them after that. It runs inside the loop, so it stays short.
const largestBudget = 10 * time.Second

// largest sizes the biggest directories inside path for an alert, within largestBudget in all: one
// slow subdirectory after another (or a hung network mount) must not hold up the loop for long.
// LargestSubdirs stops itself after twice perDir, so nothing is left running much past that.
func (h handler) largest(path string, limit int, perDir time.Duration) []cleanup.DirEntry {
	return withTimeout(h.ctx, largestBudget, []cleanup.DirEntry(nil), "sizing the largest directories in "+path, func() []cleanup.DirEntry {
		return cleanup.LargestSubdirs(path, limit, perDir)
	})
}

// alertPartitions alerts on each partition past the threshold, when partitions are watched one by
// one; otherwise alertTotal alerts on the total across disks.
func (h diskAlerts) alertPartitions(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if !cfg.MonitorIndividualPartitions {
		return
	}
	for _, p := range s.Partitions {
		if p.Percent < cfg.DiskThreshold || !h.gateOK("partition:"+p.Mountpoint, s.Now) {
			continue
		}
		msg := fmt.Sprintf("Disk %s: %.1f%% (threshold: %.1f%%)", p.Mountpoint, p.Percent, cfg.DiskThreshold)
		if ctx.DiskWebhook == nil {
			h.log.Warn(msg)
			continue
		}
		h.log.Warn("[ALERT] " + msg)
		fields := []discord.Field{field("Used", format.Bytes(float64(p.Used)), true), field("Free", format.Bytes(float64(p.Free)), true), field("Size", format.Bytes(float64(p.Total)), true)}
		fields = append(fields, largestDirsField(h.largest(p.Mountpoint, maxDirectoriesDisplay, largestBudget/2))...)
		if ctx.send(ctx.DiskWebhook, discord.Warning, fmt.Sprintf("%s is %.0f%% full", p.Mountpoint, p.Percent),
			fmt.Sprintf("Above the %.0f%% limit.", cfg.DiskThreshold), fields, "") {
			h.log.Info("    Discord alert sent")
		} else {
			h.log.Warn("    Discord alert failed")
		}
	}
}

func (h diskAlerts) alertTotal(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if (cfg.MonitorIndividualPartitions && len(s.Partitions) > 0) || s.DiskPercent < cfg.DiskThreshold || !h.gateOK("disk", s.Now) {
		return
	}
	msg := fmt.Sprintf("Disk: %.1f%% (threshold: %.1f%%)", s.DiskPercent, cfg.DiskThreshold)
	if ctx.DiskWebhook == nil {
		h.log.Warn(msg)
		return
	}
	h.log.Warn("[ALERT] " + msg)
	fields := []discord.Field{
		field("Used", format.Bytes(float64(s.TotalDisk.Used)), true), field("Free", format.Bytes(float64(s.TotalDisk.Free)), true),
		field("Size", format.Bytes(float64(s.TotalDisk.Total)), true),
	}
	if root, err := collect.DiskUsage("/"); err == nil && root.Percent >= cfg.DiskThreshold {
		fields = append(fields, largestDirsField(h.largest("/", maxDirectoriesDisplay, largestBudget/2))...)
	}
	ctx.send(ctx.DiskWebhook, discord.Warning, fmt.Sprintf("Disks are %.0f%% full", s.DiskPercent), fmt.Sprintf("Above the %.0f%% limit.", cfg.DiskThreshold), fields, "")
}

type partitionMaintenance struct {
	handler
	lastCleanup map[string]float64
}

func (h *partitionMaintenance) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	if !cfg.MonitorIndividualPartitions {
		return
	}
	for _, p := range s.Partitions {
		h.maintain(p, s.Now)
	}
}

func (h *partitionMaintenance) maintain(p collect.Partition, now float64) {
	cfg, key := h.ctx.Cfg, p.Mountpoint
	since := now - h.lastCleanup[key]
	switch {
	case key == "/":
		// diskCleanup looks after the root filesystem every cycle.
	case p.Percent >= cfg.DiskEmergencyThreshold:
		if since >= diskEmergencyCooldown {
			h.emergency(p, now)
		}
	case p.Percent >= cfg.DiskCleanupThreshold && since >= diskCleanupCooldownNormal:
		h.routine(p, now)
	}
	// Past DiskThreshold, alertPartitions has already alerted.
	if p.Percent < cfg.DiskThreshold && p.Percent >= cfg.DiskEarlyWarningThreshold {
		h.earlyWarning(p, now)
	}
}

func (h *partitionMaintenance) run(name string, fn func() cleanup.Stats) cleanup.Stats {
	h.ctx.Heartbeat()
	st := withTimeout(h.ctx, 5*time.Minute, cleanup.Stats{Errors: []string{"cleanup timed out"}}, name, fn)
	h.ctx.Heartbeat()
	return st
}

func (h *partitionMaintenance) emergency(p collect.Partition, now float64) {
	cfg, ctx, key := h.ctx.Cfg, h.ctx, p.Mountpoint
	h.log.Error(fmt.Sprintf("[EMERGENCY] Partition %s: %.1f%% (emergency threshold: %.1f%%) - CRITICAL! Starting emergency cleanup...", key, p.Percent, cfg.DiskEmergencyThreshold))
	st := h.run("emergency cleanup for "+key, func() cleanup.Stats { return ctx.Cleaner.EmergencyClean(p.Mountpoint) })
	h.lastCleanup[key] = now
	if st.BytesCleared == 0 {
		h.log.Error(fmt.Sprintf("[EMERGENCY] Partition %s: No space could be freed!", key))
		return
	}
	h.log.Error(fmt.Sprintf("[EMERGENCY] Partition %s: Emergency cleanup cleared %s %s", key, format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	fields := append(usageFields(st, p.Percent), field("Logs emptied", fmt.Sprint(st.LogsCleaned), true))
	desc := "Nightjar ran an emergency cleanup so it would not reach 100%."
	if pctOr(st.FinalUsage, p.Percent) >= cfg.DiskEmergencyThreshold {
		desc += " It is still too full: free space by hand."
	}
	h.notify("cleanup-emergency:"+key, now, diskEmergencyCooldown, discord.Critical, fmt.Sprintf("%s was %.0f%% full", key, pctOr(st.InitialUsage, p.Percent)), desc, fields, "Emergency")
}

func (h *partitionMaintenance) routine(p collect.Partition, now float64) {
	cfg, ctx, key := h.ctx.Cfg, h.ctx, p.Mountpoint
	h.log.Warn(fmt.Sprintf("[CLEANUP] Partition %s: %.1f%% (cleanup threshold: %.1f%%), starting automatic cleanup...", key, p.Percent, cfg.DiskCleanupThreshold))
	st := h.run("disk cleanup for "+key, func() cleanup.Stats { return ctx.Cleaner.CleanDiskSpace(p.Mountpoint, targetFreePercent) })
	h.lastCleanup[key] = now
	if st.BytesCleared == 0 {
		h.log.Info(fmt.Sprintf("[CLEANUP] Partition %s: No files were cleaned", key))
		return
	}
	h.log.Info(fmt.Sprintf("[CLEANUP] Partition %s: Cleaned %s %s", key, format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	fields := append(usageFields(st, p.Percent), errorsField(st, "cleanup")...)
	h.notify("cleanup:"+key, now, diskCleanupCooldownNormal, discord.Info, "Freed "+format.Bytes(float64(st.BytesCleared))+" on "+key, "Nightjar cleaned up before it filled.", fields, "")
}

func (h *partitionMaintenance) earlyWarning(p collect.Partition, now float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if !ctx.Gates.Get("partition-early:"+p.Mountpoint, now).Allow(now) {
		return
	}
	if ctx.DiskWebhook != nil {
		h.log.Warn(fmt.Sprintf("[WARNING] Partition %s: %.1f%% (early warning: %.1f%%)", p.Mountpoint, p.Percent, cfg.DiskEarlyWarningThreshold))
		if discord.PartitionAlert(ctx.DiskWebhook, p, cfg.DiskEarlyWarningThreshold, true) {
			h.log.Info("    Discord partition early warning sent for " + p.Mountpoint)
		}
	}
}
