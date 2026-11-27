package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"nightjar/internal/analytics"
	"nightjar/internal/diagnose"
	"nightjar/internal/discord"
	"nightjar/internal/incidents"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// serviceChecks runs due checks in the background, so a slow target never holds up the loop,
// and alerts on down and recovery as each result arrives.
type serviceChecks struct {
	handler
	lastPrune float64
	running   sync.WaitGroup
	slots     chan struct{} // at most maxParallelChecks run at once
	// stop ends with the loop, and cuts short a service or container restart under way for a
	// repair. Nil until the loop runs.
	stop context.Context
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
	for _, run := range m.ClaimRuns(now) {
		h.running.Add(1)
		go func() {
			defer h.running.Done()
			h.slots <- struct{}{}
			var r services.Result
			if run.Spec.Type == "heartbeat" {
				r = m.Heartbeat(run.Spec, now)
			} else {
				r = services.Run(run.Spec)
			}
			<-h.slots
			// The result goes to the check this run was claimed from, under the name it has now:
			// renamed while it ran, its old name may already belong to another check. A result
			// dropped (the check was paused, deleted or changed while it ran) neither alerts nor
			// repairs: the spec it ran with may restart a service the owner has just taken over.
			spec, change, recorded := m.Report(run, r, now)
			if !recorded {
				return
			}
			h.report(spec, r, change, now)
		}()
	}
	if s.Now-h.lastPrune >= pruneInterval {
		h.lastPrune = s.Now
		// The activity log is kept a year.
		h.ctx.Store.PruneActivity(s.Now - 365*86400)
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
			// The outage is over, so the next one alerts at once whatever the cooldown had reached.
			h.ctx.Gates.Clear("service:" + spec.Name)
			var extra []discord.Field
			if fix := h.ctx.Repairs.Recovered(spec.Name); fix != nil {
				extra = append(extra, field("Fixed by", "Nightjar: "+fix.Detail+", "+discord.Span(now-fix.TS)+" before", false))
			}
			if h.ctx.Webhook != nil && h.send(spec, "is back up", r, discord.OK, now, extra...) {
				h.log.Info("    Discord recovery alert sent")
			}
		}
		return
	}
	h.log.Info(fmt.Sprintf("%s: check failed (%s) [%d/%d]", spec.Name, r.Detail, st.ConsecutiveFailures, spec.Retries+1))
	if st.Status != services.StatusDown {
		return
	}
	// A down check is looked at every Interval, which may be far longer than a cycle.
	h.raiseAlertEvery("service:"+spec.Name, now, spec.Interval, fmt.Sprintf("Service down: %s (%s)", spec.Name, r.Detail), h.ctx.Webhook,
		func() bool { return h.send(spec, "is down", r, discord.Critical, now) }, "service_down", true)
	if h.ctx.Repairs.Due(spec, now) {
		h.running.Add(1)
		go func() {
			defer h.running.Done()
			h.repair(spec, r, now)
		}()
	}
}

// repair runs a down check's repair, says what it did, and looks at the check again at once.
func (h *serviceChecks) repair(spec services.Spec, r services.Result, now float64) {
	attempt := h.ctx.Repairs.Attempts(spec.Name)
	h.log.Info(fmt.Sprintf("%s: down, repairing (attempt %d of %d)", spec.Name, attempt, spec.Repair.Max))
	// A restart asked of systemd or Docker is cut short when Nightjar stops (they carry on with it
	// themselves). A script is not: it runs in a scope of its own so that what it starts outlives
	// Nightjar, and killing it now would take that down with it. Stopping just stops waiting for it.
	stop := h.stop
	if stop == nil || spec.Repair == nil || spec.Repair.Action == "script" {
		stop = context.Background()
	}
	rec := h.ctx.Repairs.Run(stop, spec, r.Detail, now, true)
	if stop.Err() != nil {
		// Cut short because Nightjar is stopping: the attempt is kept, but the outage carries on
		// after the restart, which tries again.
		h.log.Info(fmt.Sprintf("%s: repair stopped as Nightjar stops (%s)", spec.Name, rec.Detail))
		return
	}
	what := map[string]string{"restart": "restart " + rec.Target, "container": "restart the " + rec.Target + " container", "script": "run " + rec.Target}[rec.Action]
	fields := []discord.Field{field("Attempt", fmt.Sprintf("%d of %d", attempt, spec.Repair.Max), true)}
	if line := strings.TrimSpace(rec.Output); line != "" {
		fields = append(fields, field("Output", "```\n"+discord.Truncate(line, 900)+"\n```", false))
	}
	if h.ctx.Activity != nil {
		h.ctx.Activity("Nightjar", "repair", spec.Name, fmt.Sprintf("Repaired %s: %s", spec.Name, rec.Detail))
	}
	if rec.OK {
		h.log.Info(fmt.Sprintf("%s: repair done (%s)", spec.Name, rec.Detail))
		discord.About(h.ctx.Webhook, "check:"+spec.Name).SendEmbed("Nightjar repaired "+spec.Name, "It was down, so Nightjar tried to "+what+": "+rec.Detail+". Checking it again now.", discord.Info, fields, "Uptime")
	} else {
		h.log.Warn(fmt.Sprintf("%s: repair failed (%s)", spec.Name, rec.Detail))
		discord.About(h.ctx.Webhook, "check:"+spec.Name).SendEmbed("Repair of "+spec.Name+" failed", "Nightjar tried to "+what+": "+rec.Detail+".", discord.Warning, fields, "Uptime")
	}
	h.ctx.Services.RunSoon(spec.Name)
}

// why is the likely cause of a check going down now, from the alerts and readings of the last minutes.
func (h *serviceChecks) why(spec services.Spec, r services.Result, now float64) *incidents.Cause {
	if !h.ctx.Store.Available() {
		return nil
	}
	notes := h.ctx.Store.NotificationsBetween(now-incidents.Lead, now, 500)
	samples := h.ctx.Store.SampleBuckets(analytics.Local, now-incidents.Lead, 60)
	return incidents.Explain(notes, samples, storage.Notification{TS: now, Area: "uptime", Severity: "critical", Title: spec.Name + " is down", Body: r.Detail, Subject: "check:" + spec.Name}, incidents.LearnedFrom(h.ctx.Store))
}

func (h *serviceChecks) send(spec services.Spec, title string, r services.Result, sev discord.Severity, now float64, extra ...discord.Field) bool {
	// The title says which check and what happened; the description is what the check saw.
	desc := r.Detail
	if first, size := utf8.DecodeRuneInString(desc); size > 0 && first != utf8.RuneError {
		// The first letter, not the first byte: one of several bytes would be cut in two.
		desc = string(unicode.ToUpper(first)) + desc[size:]
	}
	var fields []discord.Field
	if target := alertTarget(spec); target != "" {
		// A long address gets its own line rather than squeezing the numbers beside it.
		fields = append(fields, field("Target", "`"+target+"`", len(target) <= 28))
	}
	if r.LatencyMS != nil && r.Up {
		fields = append(fields, field("Response", fmt.Sprintf("%.0f ms", *r.LatencyMS), true))
	}
	if u := h.ctx.Services.Uptime(spec.Name, now-86400); u != nil {
		fields = append(fields, field("Uptime today", fmt.Sprintf("%.2f%%", *u), true))
	}
	if !r.Up {
		// Why: what else happened on the machine just before, when it points somewhere.
		if c := h.why(spec, r, now); c != nil {
			fields = append(fields, field("Why", c.Text+" ("+c.Confidence+")", false))
		}
		if f, ok := diagnose.Text(r.Detail); ok {
			fields = append(fields, field("Likely cause", "**"+f.Title+"**\n"+f.Fix, false))
		}
	}
	fields = append(fields, extra...)
	return discord.About(h.ctx.Webhook, "check:"+spec.Name).SendEmbed(spec.Name+" "+title, desc, sev, fields, "Uptime")
}

// alertTarget is what an alert may show of a check's target. Alerts reach chat channels and the
// notification history, read by people who are not the dashboard's admins, so it never carries a
// secret (see services.ShownTarget).
func alertTarget(spec services.Spec) string { return services.ShownTarget(spec) }
