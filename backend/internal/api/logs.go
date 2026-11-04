package api

import (
	"net/http"
	"strconv"

	"nightjar/internal/diagnose"
	"nightjar/internal/logx"
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
		"findings": logFindings(recent),
	})
}

// logFindings diagnoses the warnings and errors among everything the log keeps, filters aside.
func logFindings(recent *logx.Recent) []diagnose.Finding {
	entries, _ := recent.Entries(logx.Query{MinLevel: "warn", Limit: logx.RecentSize})
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		line := e.Message
		for _, kv := range e.Attrs {
			line += " " + kv.Key + "=" + kv.Value
		}
		lines = append(lines, line)
	}
	return diagnose.Lines(lines, diagnose.Context{})
}
