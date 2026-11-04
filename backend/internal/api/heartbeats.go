package api

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// A job pings its heartbeat check with the check's secret token in the URL, so it needs no
// sign-in: /ping/<token> when it finished well, /start when it begins, /fail when it failed, or
// /<exit code>. What it sends in the body (its last lines of output, say) is kept with the ping.

var pingKindRE = regexp.MustCompile(`^(start|fail|\d{1,3})$`)

// maxPingBody is how much of a ping's body is read.
const maxPingBody = 16 << 10

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "" && !pingKindRE.MatchString(kind) {
		http.Error(w, "the end of a ping URL is start, fail or an exit code\n", http.StatusNotFound)
		return
	}
	var body []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		body, _ = io.ReadAll(http.MaxBytesReader(w, r.Body, maxPingBody))
	}
	if s.ctx.Services == nil {
		http.Error(w, "uptime checks are off\n", http.StatusServiceUnavailable)
		return
	}
	if _, ok := s.ctx.Services.Ping(r.PathValue("token"), kind, string(body), nowSeconds()); !ok {
		http.Error(w, "no heartbeat has that token\n", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, "OK\n")
}

// repairs lists the repair attempts, newest first (of one check with ?check=), and the scripts
// a repair can run.
func (s *Server) repairs(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 100
	}
	out := map[string]any{"repairs": s.cfg.Store.Repairs(r.URL.Query().Get("check"), limit), "dir": "", "scripts": []any{}}
	if s.ctx.Repairs != nil {
		out["dir"], out["scripts"] = s.ctx.Repairs.Dir, s.ctx.Repairs.Scripts()
	}
	writeJSON(w, http.StatusOK, out)
}

// repairCheck runs a check's repair once, now, to try it out.
func (s *Server) repairCheck(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.ctx.Services == nil || !s.ctx.Services.Has(name) {
		writeJSON(w, http.StatusNotFound, errBody("no check with that name"))
		return
	}
	st := s.ctx.Services.State(name)
	if st.Spec.Repair == nil {
		writeJSON(w, http.StatusBadRequest, errBody("The check has no repair"))
		return
	}
	if s.ctx.Repairs == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("Repairs are not available"))
		return
	}
	detail := ""
	if st.LastResult != nil {
		detail = st.LastResult.Detail
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	rec := s.ctx.Repairs.Run(ctx, st.Spec, detail, nowSeconds(), false)
	s.ctx.Services.RunSoon(name)
	writeJSON(w, http.StatusOK, map[string]any{"repair": rec})
}
