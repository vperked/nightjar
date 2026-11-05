package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"nightjar/internal/analytics"
	"nightjar/internal/incidents"
)

// listIncidents is the incidents of the last days, newest first, each with its timeline, likely
// cause and the machine's readings, what the owner said about it, and its status page notice
// (or a draft of one).
func (s *Server) listIncidents(days int) []incidents.Incident {
	now := float64(time.Now().Unix())
	since := now - float64(days)*86400
	notes := s.cfg.Store.NotificationsBetween(since-incidents.Lead, now, 20000)
	samples := s.cfg.Store.SampleBuckets(analytics.Local, since-incidents.Lead-1800, 60)
	list := incidents.Build(notes, samples, now, incidents.LearnedFrom(s.cfg.Store))
	feedback, notices, public := incidents.AllFeedback(s.cfg.Store), incidents.AllNotices(s.cfg.Store), s.publicNames()
	// An incident that began before the range is left to the longer one.
	out := list[:0]
	for _, inc := range list {
		if inc.Started < since {
			continue
		}
		if f, ok := feedback[inc.ID]; ok {
			inc.Feedback = &f
		}
		if n, ok := notices[inc.ID]; ok {
			inc.Notice = &n
		}
		if d, ok := incidents.Draft(inc, public, now); ok {
			inc.Draft = &d
		}
		out = append(out, inc)
	}
	return out
}

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	days := 14
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil {
		days = min(90, max(1, d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": s.listIncidents(days), "days": days})
}

// incident finds one incident of the last 90 days by its ID.
func (s *Server) incident(w http.ResponseWriter, r *http.Request) (incidents.Incident, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		list := s.listIncidents(90)
		if i := slices.IndexFunc(list, func(x incidents.Incident) bool { return x.ID == id }); i >= 0 {
			return list[i], true
		}
	}
	writeJSON(w, http.StatusNotFound, errBody("no incident with that id in the last 90 days"))
	return incidents.Incident{}, false
}

// saveFeedback keeps what the owner said about an incident's cause, which weighs the causes of
// later incidents of the same things. DELETE takes it back.
func (s *Server) saveFeedback(w http.ResponseWriter, r *http.Request) {
	inc, ok := s.incident(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		incidents.SetFeedback(s.cfg.Store, inc.ID, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var f incidents.Feedback
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected feedback"))
		return
	}
	if err := f.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	f.Kind, f.Subjects, f.At = "", inc.Subjects(), nowSeconds()
	if inc.Cause != nil {
		f.Kind = inc.Cause.Kind
	}
	if err := incidents.SetFeedback(s.cfg.Store, inc.ID, &f); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": f})
}

// saveNotice keeps an incident's notice for the status page, shown there once published.
// DELETE takes it off.
func (s *Server) saveNotice(w http.ResponseWriter, r *http.Request) {
	inc, ok := s.incident(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		incidents.SetNotice(s.cfg.Store, inc.ID, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var n incidents.Notice
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a notice"))
		return
	}
	if err := n.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	n.ID, n.Started, n.Ended, n.Updated = inc.ID, inc.Started, inc.Ended, nowSeconds()
	if err := incidents.SetNotice(s.cfg.Store, inc.ID, &n); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notice": n})
}

// publicNames is each check on the status page and the name visitors see for it.
func (s *Server) publicNames() map[string]string {
	out := map[string]string{}
	if s.ctx.Services == nil {
		return out
	}
	d := s.design()
	if d == nil {
		for _, spec := range s.ctx.Services.Specs() {
			out[spec.Name] = spec.Name
		}
		return out
	}
	for _, g := range d.Groups {
		for _, it := range g.Items {
			out[it.Check] = it.Label
			if it.Label == "" {
				out[it.Check] = it.Check
			}
		}
	}
	return out
}

// publicNotices are the published notices of the last 30 days, newest first, for visitors.
// An open incident's notice says when it ended once it has.
func (s *Server) publicNotices() []incidents.Notice {
	cut := nowSeconds() - 30*86400
	out := []incidents.Notice{}
	for _, n := range incidents.AllNotices(s.cfg.Store) {
		if !n.Published || n.Started < cut {
			continue
		}
		n.Updated = 0
		out = append(out, n)
	}
	if len(out) == 0 {
		return out
	}
	ended := s.incidentEnds()
	for i := range out {
		if e, ok := ended[out[i].ID]; ok {
			out[i].Ended = e
		}
	}
	slices.SortFunc(out, func(a, b incidents.Notice) int { return int(b.Started - a.Started) })
	return out
}

// endedCache keeps when the last 30 days' incidents ended, for the notices on the public status page.
// Working that out reads a month of alerts and readings, and anyone may poll the page.
type endedCache struct {
	mu    sync.Mutex
	at    time.Time
	ended map[int64]*float64
}

const endedCacheTTL = time.Minute

// incidentEnds is when each incident of the last 30 days ended, by ID (nil while it goes on).
func (s *Server) incidentEnds() map[int64]*float64 {
	c := &s.ends
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended != nil && time.Since(c.at) < endedCacheTTL {
		return c.ended
	}
	ended := map[int64]*float64{}
	for _, inc := range s.listIncidents(30) {
		ended[inc.ID] = inc.Ended
	}
	c.ended, c.at = ended, time.Now()
	return ended
}
