package api

import (
	"net/http"
	"time"

	"nightjar/internal/report"
)

// weeklyReport is the week so far as the Monday report would sum it up, and when the next goes out.
func (s *Server) weeklyReport(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	out := map[string]any{"report": report.Make(s.ctx, time.Now()), "enabled": cfg.WeeklyReport, "at": cfg.WeeklyReportAt}
	if when, err := report.ParseWhen(cfg.WeeklyReportAt); err == nil {
		out["next"] = when.Next(time.Now()).Unix()
	}
	writeJSON(w, http.StatusOK, out)
}

// sendReport sends the report now, to see it in Discord.
func (s *Server) sendReport(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Webhook == nil {
		writeJSON(w, http.StatusBadRequest, errBody("No alert channel is set up"))
		return
	}
	if !report.Send(s.ctx, report.Make(s.ctx, time.Now())) {
		writeJSON(w, statusRefused, errBody("The report could not be sent"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}
