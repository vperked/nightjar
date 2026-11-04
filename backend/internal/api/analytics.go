package api

import (
	"net/http"
	"os"
	"time"

	"nightjar/internal/analytics"
	"nightjar/internal/remote"
)

// Analytics: every server's minutes, this one's included, over an hour, a day, a week or a month.

type hostAnalytics struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // local or agent
	Status string `json:"status"`
	analytics.History
}

func (s *Server) hostsAnalytics(rng analytics.Range, now time.Time) []hostAnalytics {
	name, _ := os.Hostname()
	out := []hostAnalytics{{ID: analytics.Local, Name: name, Kind: "local", Status: "online", History: analytics.Read(s.cfg.Store, analytics.Local, rng, now, false)}}
	if s.ctx.Remote != nil {
		for _, h := range s.ctx.Remote.List(0) {
			if h.Kind == remote.KindSSH {
				continue // nothing is recorded for a server watched through its SSH login alone
			}
			a := hostAnalytics{ID: h.ID, Name: h.Name, Kind: h.Kind, Status: h.Status, History: analytics.Read(s.cfg.Store, h.ID, rng, now, true)}
			// The last minute of an outage reads as ongoing until the next is stored; the poll knows.
			if h.Status != "offline" && len(a.Incidents) > 0 {
				a.Incidents[0].Ongoing = false
			}
			out = append(out, a)
		}
	}
	return out
}

// analyticsAll is every server over ?range= (1h, 24h, 7d or 30d).
func (s *Server) analyticsAll(w http.ResponseWriter, r *http.Request) {
	rng := analytics.RangeNamed(r.URL.Query().Get("range"))
	writeJSON(w, http.StatusOK, map[string]any{
		"range": rng, "ranges": analytics.Ranges, "step": rng.Step.Seconds(), "hosts": s.hostsAnalytics(rng, time.Now()), "kept": s.cfg.Store.Available(),
	})
}

// analyticsHost is one server ("local" for this one) over ?range=.
func (s *Server) analyticsHost(w http.ResponseWriter, r *http.Request) {
	rng := analytics.RangeNamed(r.URL.Query().Get("range"))
	for _, h := range s.hostsAnalytics(rng, time.Now()) {
		if h.ID == r.PathValue("id") {
			writeJSON(w, http.StatusOK, map[string]any{"range": rng, "step": rng.Step.Seconds(), "host": h})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errBody("No such server"))
}
