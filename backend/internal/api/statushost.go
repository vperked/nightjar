package api

import (
	"net"
	"net/http"
	"strings"
)

// The public status page can have an address of its own. On STATUS_PAGE_DOMAIN it is the whole
// site: the page at /, with only what it needs to draw itself, and none of the dashboard. On the
// dashboard's own address it is at STATUS_PAGE_PATH as well as at /status.

// requestHost is the host the visitor asked for, in lower case and without a port: the one a reverse
// proxy on this machine says it was asked for (X-Forwarded-Host), else the request's Host.
func requestHost(r *http.Request) string {
	h := r.Host
	if behindProxy(r) {
		if fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); strings.TrimSpace(fwd) != "" {
			h = strings.TrimSpace(fwd)
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// onStatusHost reports whether r was made to the status page's own domain.
func (s *Server) onStatusHost(r *http.Request) bool {
	d := s.ctx.Config().StatusPageDomain
	return d != "" && requestHost(r) == d
}

// isStatusPage reports whether r asks for the status page itself (its HTML), wherever it is.
func (s *Server) isStatusPage(r *http.Request) bool {
	if s.onStatusHost(r) {
		return r.URL.Path == "/"
	}
	p := r.URL.Path
	return p == "/status" || p == s.ctx.Config().StatusPagePath
}

// statusHostOnly keeps the status page's own domain to the status page: the page, its scripts,
// styles and fonts, and the public status data. Anything else asked for there is not found (the
// API, so nothing of the dashboard can be reached through that domain) or sent back to the page.
func (s *Server) statusHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.onStatusHost(r) {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		read := r.Method == http.MethodGet || r.Method == http.MethodHead
		switch {
		case read && (p == "/" || p == "/api/status" || p == "/boot.js" || strings.HasPrefix(p, "/assets/")):
			next.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/") || p == "/health" || p == "/metrics":
			writeJSON(w, http.StatusNotFound, errBody("Not found"))
		case read:
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, errBody("Method not allowed"))
		}
	})
}

// markStatusPage tells the app, in the page's own HTML, that this address shows the status page,
// since the path alone does not say so on the status domain or at a path of the owner's choosing.
// The early script and the app both read it before anything is drawn.
func markStatusPage(index []byte) []byte {
	return []byte(strings.Replace(string(index), "<html", `<html data-page="status"`, 1))
}
