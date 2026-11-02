package monitor

import (
	"fmt"
	"sync"

	"servermonitor/internal/discord"
	"servermonitor/internal/services"
)

// serviceChecks runs due checks in the background, so a slow target never holds up the loop,
// and alerts on down and recovery as each result arrives.
type serviceChecks struct {
	handler
	lastPrune float64
	running   sync.WaitGroup
	slots     chan struct{} // at most maxParallelChecks run at once
}

const (
	maxParallelChecks = 8
	pruneInterval     = 3600.0
)

func (h *serviceChecks) check(s *Snapshot) {
	m := h.ctx.Services
	if m == nil {
		return
	}
	now := s.Now
	for _, spec := range m.Claim(now) {
		h.running.Add(1)
		go func() {
			defer h.running.Done()
			h.slots <- struct{}{}
			r := services.Run(spec)
			<-h.slots
			h.report(spec, r, m.Record(spec.Name, r, now), now)
		}()
	}
	if s.Now-h.lastPrune >= pruneInterval {
		h.lastPrune = s.Now
		if n := m.Prune(s.Now - float64(h.ctx.Cfg.HistoryRetentionDays)*86400); n > 0 {
			h.log.Info(fmt.Sprintf("Pruned %d service check(s) older than %d days", n, h.ctx.Cfg.HistoryRetentionDays))
		}
	}
}

// wait blocks until every running check has reported.
func (h *serviceChecks) wait() { h.running.Wait() }

func (h *serviceChecks) report(spec services.Spec, r services.Result, change string, now float64) {
	st := h.ctx.Services.State(spec.Name)
	if r.Up {
		h.log.Debug(fmt.Sprintf("%s: up (%s)", spec.Name, r.Detail))
		if change == services.StatusUp {
			h.log.Info(fmt.Sprintf("[RECOVERED] Service up: %s (%s)", spec.Name, r.Detail))
			if h.ctx.Webhook != nil && h.send(spec, "Service Recovered", r, discord.OK, now) {
				h.log.Info("    Discord recovery alert sent")
			}
		}
		return
	}
	h.log.Info(fmt.Sprintf("%s: check failed (%s) [%d/%d]", spec.Name, r.Detail, st.ConsecutiveFailures, spec.Retries+1))
	if st.Status != services.StatusDown {
		return
	}
	h.raiseAlert("service:"+spec.Name, now, fmt.Sprintf("Service down: %s (%s)", spec.Name, r.Detail), h.ctx.Webhook,
		func() bool { return h.send(spec, "Service Down", r, discord.Critical, now) }, "service_down", true)
}

func (h *serviceChecks) send(spec services.Spec, title string, r services.Result, sev discord.Severity, now float64) bool {
	detail := r.Detail
	if detail == "" {
		detail = "n/a"
	}
	fields := []discord.Field{
		field("Service", spec.Name, true), field("Type", spec.Type, true),
		field("Target", spec.Target, false), field("Detail", detail, false),
	}
	if r.LatencyMS != nil {
		fields = append(fields, field("Response time", fmt.Sprintf("%.0f ms", *r.LatencyMS), true))
	}
	if u := h.ctx.Services.Uptime(spec.Name, now-86400); u != nil {
		fields = append(fields, field("Uptime (24h)", fmt.Sprintf("%.2f%%", *u), true))
	}
	return discord.About(h.ctx.Webhook, "check:"+spec.Name).SendEmbed(fmt.Sprintf("%s: %s", title, spec.Name), r.Detail, sev, fields, "Uptime")
}
