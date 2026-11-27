package api

import (
	"net/http"
	"strconv"
	"sync"

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
		"findings": s.findings.get(recent),
	})
}

// findingCache keeps the log's findings until its warnings change: the viewer polls while it
// follows the log, and diagnosing a few thousand warnings can take a quarter of a second.
type findingCache struct {
	mu          sync.Mutex
	first, last uint64 // the sequence numbers of the warnings diagnosed, oldest and newest
	out         []diagnose.Finding
}

func (c *findingCache) get(recent *logx.Recent) []diagnose.Finding {
	entries, _ := recent.Entries(logx.Query{MinLevel: "warn", Limit: logx.RecentSize})
	var first, last uint64
	if n := len(entries); n > 0 {
		first, last = entries[0].Seq, entries[n-1].Seq
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out == nil || first != c.first || last != c.last {
		c.out, c.first, c.last = diagnoseEntries(entries), first, last
	}
	return c.out
}

// diagnoseEntries diagnoses the warnings and errors among everything the log keeps, filters aside.
func diagnoseEntries(entries []logx.Entry) []diagnose.Finding {
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
