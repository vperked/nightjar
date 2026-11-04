package api

import (
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 64 << 10

// writable guards changes: a signed-in admin (via auth), then fromDashboard.
func (s *Server) writable(next http.HandlerFunc) http.Handler {
	return s.auth(fromDashboard(s.logged(next)))
}

// fromDashboard accepts only a JSON body and, when the browser names one, a same-origin Origin. A
// cross-site form cannot send JSON, and a cross-site fetch cannot pass the Origin check, so another
// site cannot make changes, or sign anyone in or out, through a visitor's browser.
func fromDashboard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, errBody("Cross-origin changes are not allowed"))
				return
			}
		}
		if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, errBody("Send JSON"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next(w, r)
	}
}
