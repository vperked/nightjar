package monitor

import "time"

// updateScheduler hands the update manager the clock: it decides itself when checks are due and
// which updates to install, and does the work on its own goroutine.
type updateScheduler struct {
	handler
}

func (h *updateScheduler) check(s *Snapshot) {
	cfg := h.ctx.Cfg
	if h.ctx.Updates == nil || !cfg.SystemUpdateEnabled {
		return
	}
	h.ctx.Updates.Tick(s.Time, time.Duration(cfg.SystemUpdateIntervalSeconds*float64(time.Second)), UpdateWindow(cfg))
}
