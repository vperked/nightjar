package monitor

import (
	"fmt"
	"time"

	"nightjar/internal/cleanup"
	"nightjar/internal/discord"
	"nightjar/internal/format"
)

const pressureCheckInterval = 5

// logCleanup trims oversized logs every few cycles, more often under disk pressure.
type logCleanup struct {
	handler
	counter   int
	lastAlert map[string]float64
	// cleaned is each log trimmed while it stays a candidate, with its size just after. Under
	// pressure every log stays a candidate, so one that grows past that size is trimmed again.
	cleaned map[string]int64
}

func (h *logCleanup) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	h.counter++
	// The pressure that matters is on the filesystem the logs are on (see logsPercent).
	percent := logsPercent(h.ctx, s)
	interval := cfg.LogCheckInterval
	if percent >= cfg.DiskCleanupThreshold {
		interval = pressureCheckInterval
	}
	if h.counter < interval {
		return
	}
	h.counter = 0
	h.checkLogs(s, percent >= cfg.DiskAggressiveCleanupThreshold)
	h.ctx.Heartbeat()
}

func (h *logCleanup) checkLogs(s *Snapshot, aggressive bool) {
	logs := h.ctx.Logs
	rotated := withTimeout(h.ctx, 60*time.Second, cleanup.Stats{}, "rotated log cleanup", func() cleanup.Stats { return logs.CleanupRotated(aggressive) })
	if rotated.FilesRemoved > 0 {
		h.log.Warn(fmt.Sprintf("[CLEANUP] Removed %d rotated log archive(s), freed %s", rotated.FilesRemoved, format.Bytes(float64(rotated.BytesCleared))))
	}
	sizes := withTimeout(h.ctx, 10*time.Second, cleanup.Sizes{}, "log survey", logs.Survey)
	candidates := sizes.Exceeded
	if aggressive {
		all := withTimeout(h.ctx, 30*time.Second, []cleanup.LogInfo(nil), "log listing", logs.AllLogFiles)
		if sizes.App != nil {
			all = append(all, *sizes.App)
		}
		all = append(all, sizes.System...)
		if sizes.Journal != nil {
			all = append(all, *sizes.Journal)
		}
		candidates = cleanup.LargestPerPath(all)
		h.log.Info(fmt.Sprintf("[LOG CHECK] Disk at %.1f%% - cleaning ALL logs proactively", s.DiskPercent))
	}
	var batch []discord.CleanedLog
	for _, info := range candidates {
		if c, ok := h.cleanOne(info.Path, s, aggressive); ok {
			batch = append(batch, c)
		}
	}
	h.report(batch, logs.ThresholdFormatted())
	if len(batch) > 0 {
		h.ctx.Cleaner.RestartLogServices()
	}
	still := map[string]bool{}
	for _, c := range candidates {
		still[c.Path] = true
	}
	for p := range h.cleaned {
		if !still[p] {
			delete(h.cleaned, p)
		}
	}
}

func (h *logCleanup) cleanOne(path string, s *Snapshot, aggressive bool) (discord.CleanedLog, bool) {
	logs := h.ctx.Logs
	current, ok := logs.Size(path)
	if path == logs.JournalPath {
		current, ok = logs.JournalSize()
	}
	if !ok || (!aggressive && current.Size <= logs.ThresholdBytes) {
		return discord.CleanedLog{}, false
	}
	// Outside pressure a log still over the threshold after its trim (a journal's active files
	// cannot be vacuumed) is left alone; under pressure one that regrows is trimmed again.
	if after, done := h.cleaned[path]; done && (!aggressive || current.Size <= after) {
		return discord.CleanedLog{}, false
	}
	if s.Now-h.lastAlert[path] < h.ctx.Cfg.AlertCooldown {
		return discord.CleanedLog{}, false
	}
	size := format.Bytes(float64(current.Size))
	if aggressive {
		h.log.Info(fmt.Sprintf("    Proactive cleanup (disk %.1f%%): %s (%s)", s.DiskPercent, path, size))
	} else {
		h.log.Info(fmt.Sprintf("    Cleaning %s: %s > %s", path, size, logs.ThresholdFormatted()))
	}
	ok = withTimeout(h.ctx, 30*time.Second, false, "cleanup log "+path, func() bool { return logs.Clean(current, aggressive) })
	h.lastAlert[path] = s.Now
	if !ok {
		h.log.Error(fmt.Sprintf("    Log cleanup failed for %s - check permissions (may need root/sudo)", path))
		return discord.CleanedLog{}, false
	}
	after := current
	if a, ok := logs.Size(path); ok {
		after = a
	}
	if path == logs.JournalPath {
		if a, ok := logs.JournalSize(); ok {
			after = a
		}
	}
	h.cleaned[path] = after.Size
	h.log.Info(fmt.Sprintf("    Log cleanup successful for %s (was %s, now %s)", path, size, format.Bytes(float64(after.Size))))
	return discord.CleanedLog{Path: path, OriginalSize: size, CurrentSize: format.Bytes(float64(after.Size))}, true
}

func (h *logCleanup) report(batch []discord.CleanedLog, threshold string) {
	if len(batch) == 0 {
		return
	}
	if h.ctx.Webhook == nil {
		h.log.Warn(fmt.Sprintf("%d log file(s) exceeded threshold and were auto-cleaned (no webhook configured)", len(batch)))
		return
	}
	h.log.Warn(fmt.Sprintf("[ALERT] %d log file(s) exceeded threshold and were auto-cleaned", len(batch)))
	if discord.LogCleanupAlert(h.ctx.Webhook, batch, threshold) {
		h.log.Info(fmt.Sprintf("    Discord batched log cleanup alert sent for %d log(s)", len(batch)))
	}
}
