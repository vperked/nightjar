package monitor

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"servermonitor/internal/cleanup"
	"servermonitor/internal/collect"
	"servermonitor/internal/discord"
	"servermonitor/internal/format"
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
		field("Usage Before", fmt.Sprintf("%.1f%%", pctOr(st.InitialUsage, fallback)), true),
		field("Usage After", fmt.Sprintf("%.1f%%", pctOr(st.FinalUsage, fallback)), true),
		field("Space Cleared", format.Bytes(float64(st.BytesCleared)), true),
		field("Files Removed", fmt.Sprint(st.FilesRemoved), true),
	}
}

func errorsField(st cleanup.Stats, during string) []discord.Field {
	if len(st.Errors) == 0 {
		return nil
	}
	return []discord.Field{field("Errors", fmt.Sprintf("%d error(s) occurred during %s", len(st.Errors), during), false)}
}

func largestDirsField(dirs []cleanup.DirEntry, label string) []discord.Field {
	if len(dirs) == 0 {
		return nil
	}
	lines := make([]string, len(dirs))
	for i, d := range dirs {
		lines[i] = fmt.Sprintf("%d. `%s`: %s", i+1, filepath.Base(d.Path), format.Bytes(float64(d.Size)))
	}
	return []discord.Field{field(fmt.Sprintf("Largest Directories (%s)", label), strings.Join(lines, "\n"), false)}
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

func (h *diskCleanup) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	since := s.Now - h.lastCleanup
	if s.DiskPercent >= cfg.DiskCleanupThreshold && since >= logCleanupInterval {
		h.cleanLogs(s)
		since = 0
	}
	switch {
	case s.DiskPercent >= cfg.DiskEmergencyThreshold:
		if since >= diskEmergencyCooldown {
			h.emergency(s)
		}
	case s.DiskPercent >= cfg.DiskCleanupThreshold:
		if cd := cleanupCooldown(s.DiskPercent); since >= cd {
			h.routine(s)
		} else {
			h.log.Debug(fmt.Sprintf("Disk cleanup cooldown: %.0fs", cd-since))
		}
	}
}

func (h *diskCleanup) cleanLogs(s *Snapshot) {
	ctx := h.ctx
	h.log.Warn(fmt.Sprintf("[CLEANUP] Disk at %.1f%% - starting log cleanup...", s.DiskPercent))
	st := withTimeout(5*time.Minute, cleanup.Stats{}, "log cleanup operation", ctx.Cleaner.CleanLogsAndRestart)
	ctx.Heartbeat()
	if st.LogsCleaned > 0 {
		h.log.Warn(fmt.Sprintf("[CLEANUP] Cleared %d log file(s), freed %s", st.LogsCleaned, format.Bytes(float64(st.BytesCleared))))
		if u, err := collect.DiskUsage("/"); err == nil {
			h.log.Info(fmt.Sprintf("[CLEANUP] Disk usage after log cleanup: %.1f%%", u.Percent))
		}
	}
	h.lastCleanup = s.Now
}

func (h *diskCleanup) emergency(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	threshold := cfg.DiskEmergencyThreshold
	h.log.Error(fmt.Sprintf("[EMERGENCY] Disk usage at %.1f%% (emergency threshold: %.1f%%) - CRITICAL! Starting emergency cleanup...", s.DiskPercent, threshold))
	st := withTimeout(10*time.Minute, cleanup.Stats{}, "emergency disk cleanup", func() cleanup.Stats { return ctx.Cleaner.EmergencyClean("/") })
	ctx.Heartbeat()
	h.lastCleanup = s.Now
	if st.BytesCleared == 0 {
		h.log.Error("No space could be freed; the disk may fill completely")
		ctx.send(ctx.DiskWebhook, discord.Critical, "Disk Cleanup Failed",
			fmt.Sprintf("Disk at %.1f%% and emergency cleanup could not free space! Manual intervention required immediately!", s.DiskPercent),
			[]discord.Field{
				field("Current Usage", fmt.Sprintf("%.1f%%", s.DiskPercent), false),
				field("Emergency Threshold", fmt.Sprintf("%.1f%%", threshold), true),
				field("Status", "CRITICAL - Manual intervention required", false),
			}, "Critical Alert")
		return
	}
	const stillFull = "Disk still critically full after cleanup! Manual intervention required!"
	h.log.Error(fmt.Sprintf("[EMERGENCY] Emergency cleanup cleared %s %s", format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	final := pctOr(st.FinalUsage, s.DiskPercent)
	if st.FinalUsage != nil {
		h.log.Error(fmt.Sprintf("[EMERGENCY] Disk usage after emergency cleanup: %.1f%%", final))
		if final >= threshold {
			h.log.Error("[EMERGENCY] " + stillFull)
		}
	}
	fields := append([]discord.Field{field("EMERGENCY CLEANUP", "Disk critically full - aggressive cleanup performed", false)}, usageFields(st, s.DiskPercent)...)
	fields = append(fields, field("Logs Cleaned", fmt.Sprint(st.LogsCleaned), true))
	if final >= threshold {
		fields = append(fields, field("CRITICAL", stillFull, false))
	}
	fields = append(fields, errorsField(st, "emergency cleanup")...)
	if ctx.send(ctx.DiskWebhook, discord.Critical, "Disk Critically Full", "Emergency cleanup performed to prevent disk from reaching 100%", fields, "Emergency") {
		h.log.Info("    Discord emergency notification sent")
	}
}

func (h *diskCleanup) routine(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	h.log.Warn(fmt.Sprintf("[CLEANUP] Disk usage at %.1f%% (cleanup threshold: %.1f%%), starting automatic cleanup...", s.DiskPercent, cfg.DiskCleanupThreshold))
	st := withTimeout(5*time.Minute, cleanup.Stats{}, "disk cleanup operation", func() cleanup.Stats { return ctx.Cleaner.CleanDiskSpace("/", targetFreePercent) })
	h.lastCleanup = s.Now
	if st.BytesCleared == 0 {
		h.log.Info("[CLEANUP] No files were cleaned (disk may need manual intervention)")
		return
	}
	h.log.Info(fmt.Sprintf("[CLEANUP] Cleaned %s %s", format.Bytes(float64(st.BytesCleared)), cleanupCounts(st)))
	fields := append(usageFields(st, s.DiskPercent), field("Directories Removed", fmt.Sprint(st.DirsRemoved), true))
	fields = append(fields, errorsField(st, "cleanup")...)
	if ctx.send(ctx.DiskWebhook, discord.Info, "Disk Cleanup Performed", "Automatic disk cleanup completed to prevent disk from filling up", fields, "") {
		h.log.Info("    Discord cleanup notification sent")
	}
}

type diskAlerts struct{ handler }

func (h diskAlerts) gateOK(key string, now float64) bool {
	gate := h.ctx.Gates.Get(key, now)
	if !gate.ShouldAlert(now) {
		h.log.Debug(fmt.Sprintf("%s cooldown: %.0fs", key, gate.SecondsUntilNext(now)))
		return false
	}
	gate.MarkSent(now)
	return true
}

func (h diskAlerts) alertPartitions(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
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
		fields := []discord.Field{
			field("Mountpoint", p.Mountpoint, true), field("Usage", fmt.Sprintf("%.1f%%", p.Percent), true),
			field("Threshold", fmt.Sprintf("%.1f%%", cfg.DiskThreshold), true),
			field("Total Capacity", format.Bytes(float64(p.Total)), false),
			field("Used", format.Bytes(float64(p.Used)), true), field("Free", format.Bytes(float64(p.Free)), true),
		}
		fields = append(fields, largestDirsField(cleanup.LargestSubdirs(p.Mountpoint, maxDirectoriesDisplay, 15*time.Second), p.Mountpoint)...)
		if ctx.send(ctx.DiskWebhook, discord.Warning, "Disk Usage Alert: "+p.Mountpoint,
			fmt.Sprintf("Disk usage on %s has exceeded the threshold of %.1f%%", p.Mountpoint, cfg.DiskThreshold), fields, "") {
			h.log.Info("    Discord alert sent")
		} else {
			h.log.Warn("    Discord alert failed")
		}
	}
}

func (h diskAlerts) alertTotal(s *Snapshot) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if len(s.Partitions) > 0 || s.DiskPercent < cfg.DiskThreshold || !h.gateOK("disk", s.Now) {
		return
	}
	msg := fmt.Sprintf("Disk: %.1f%% (threshold: %.1f%%)", s.DiskPercent, cfg.DiskThreshold)
	if ctx.DiskWebhook == nil {
		h.log.Warn(msg)
		return
	}
	h.log.Warn("[ALERT] " + msg)
	fields := []discord.Field{
		field("Overall Usage", fmt.Sprintf("%.1f%%", s.DiskPercent), true), field("Threshold", fmt.Sprintf("%.1f%%", cfg.DiskThreshold), true),
		field("Total Capacity", format.Bytes(float64(s.TotalDisk.Total)), false),
		field("Used", format.Bytes(float64(s.TotalDisk.Used)), true), field("Free", format.Bytes(float64(s.TotalDisk.Free)), true),
	}
	if root, err := collect.DiskUsage("/"); err == nil && root.Percent >= cfg.DiskThreshold {
		fields = append(fields, largestDirsField(cleanup.LargestSubdirs("/", maxDirectoriesDisplay, 15*time.Second), "root")...)
	}
	ctx.send(ctx.DiskWebhook, discord.Warning, "Disk Usage Alert", fmt.Sprintf("Disk usage has exceeded the threshold of %.1f%%", cfg.DiskThreshold), fields, "")
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
	for _, p := range s.Partitions {
		if p.IsNVMe && p.Percent >= cfg.DiskEarlyWarningThreshold {
			h.earlyWarning(p, "nvme:"+p.Mountpoint, "NVMe "+p.Mountpoint, "NVMe early warning", s.Now)
		}
	}
}

func (h *partitionMaintenance) maintain(p collect.Partition, now float64) {
	cfg, key := h.ctx.Cfg, p.Mountpoint
	since := now - h.lastCleanup[key]
	switch {
	case p.Percent >= cfg.DiskEmergencyThreshold:
		if since >= diskEmergencyCooldown {
			h.emergency(p, now)
		}
	case p.Percent >= cfg.DiskCleanupThreshold && since >= diskCleanupCooldownNormal:
		h.routine(p, now)
	}
	switch {
	case p.Percent >= cfg.DiskThreshold:
		h.thresholdAlert(p, now)
	case p.Percent >= cfg.DiskEarlyWarningThreshold:
		h.earlyWarning(p, "partition-early:"+key, "Partition "+key, "partition early warning", now)
	}
}

func (h *partitionMaintenance) run(name string, fn func() cleanup.Stats) cleanup.Stats {
	h.ctx.Heartbeat()
	st := withTimeout(5*time.Minute, cleanup.Stats{Errors: []string{"cleanup timed out"}}, name, fn)
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
	fields := append([]discord.Field{field("EMERGENCY CLEANUP", "Partition "+key+" critically full", false)}, usageFields(st, p.Percent)...)
	fields = append(fields, field("Logs Cleaned", fmt.Sprint(st.LogsCleaned), true))
	if pctOr(st.FinalUsage, p.Percent) >= cfg.DiskEmergencyThreshold {
		fields = append(fields, field("CRITICAL", "Partition still critically full after cleanup! Manual intervention required!", false))
	}
	if ctx.send(ctx.DiskWebhook, discord.Critical, "Partition Critically Full", "Emergency cleanup performed for partition "+key, fields, "Emergency") {
		h.log.Info("    Discord emergency notification sent for " + key)
	}
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
	fields := append([]discord.Field{field("Partition", key, false)}, usageFields(st, p.Percent)...)
	fields = append(fields, field("Directories Removed", fmt.Sprint(st.DirsRemoved), true))
	fields = append(fields, errorsField(st, "cleanup")...)
	if ctx.send(ctx.DiskWebhook, discord.Info, "Partition Cleanup Performed", "Automatic cleanup completed for partition "+key, fields, "") {
		h.log.Info("    Discord partition cleanup notification sent for " + key)
	}
}

func (h *partitionMaintenance) thresholdAlert(p collect.Partition, now float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	gate := ctx.Gates.Get("partition:"+p.Mountpoint, now)
	if !gate.ShouldAlert(now) {
		return
	}
	msg := fmt.Sprintf("Partition %s: %.1f%% (threshold: %.1f%%)", p.Mountpoint, p.Percent, cfg.DiskThreshold)
	if ctx.DiskWebhook != nil {
		h.log.Warn("[ALERT] " + msg)
		if discord.PartitionAlert(ctx.DiskWebhook, p, cfg.DiskThreshold, false) {
			h.log.Info("    Discord partition alert sent for " + p.Mountpoint)
		}
	} else {
		h.log.Warn(msg)
	}
	gate.MarkSent(now)
}

func (h *partitionMaintenance) earlyWarning(p collect.Partition, key, label, kind string, now float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	gate := ctx.Gates.Get(key, now)
	if !gate.ShouldAlert(now) {
		return
	}
	if ctx.DiskWebhook != nil {
		h.log.Warn(fmt.Sprintf("[WARNING] %s: %.1f%% (early warning: %.1f%%)", label, p.Percent, cfg.DiskEarlyWarningThreshold))
		if discord.PartitionAlert(ctx.DiskWebhook, p, cfg.DiskEarlyWarningThreshold, true) {
			h.log.Info(fmt.Sprintf("    Discord %s sent for %s", kind, p.Mountpoint))
		}
	}
	gate.MarkSent(now)
}
