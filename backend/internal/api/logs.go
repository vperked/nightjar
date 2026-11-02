package api

import (
	"net/http"
	"strconv"

	"servermonitor/internal/logx"
)

// logs serves the monitor's own recent log for the dashboard's viewer: ?after=<seq> follows it,
// ?level=warn, ?component= and ?q= filter it.
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseUint(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	recent := logx.RecentEntries()
	entries, latest := recent.Entries(logx.Query{After: after, MinLevel: q.Get("level"), Component: q.Get("component"), Search: q.Get("q"), Limit: limit})
	if entries == nil {
		entries = []logx.Entry{}
	}
	cfg := s.ctx.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries, "latest": latest, "components": recent.Components(),
		"level": cfg.LogLevel, "file": cfg.LogFile, "kept": logx.RecentSize,
	})
}
