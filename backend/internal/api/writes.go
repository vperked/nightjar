package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 64 << 10

// writable guards changes: a signed-in admin (via auth; a viewer is turned away), then fromDashboard.
func (s *Server) writable(next http.HandlerFunc) http.Handler {
	return s.writableUpTo(maxBody, next)
}

// writableUpTo is writable for a change whose body may be larger than most.
func (s *Server) writableUpTo(limit int64, next http.HandlerFunc) http.Handler {
	return s.auth(adminOnly(s.fromDashboardUpTo(limit, s.logged(next))))
}

// fromDashboard accepts only a JSON body and, when the browser names one, a same-origin Origin. A
// cross-site form cannot send JSON, and a cross-site fetch cannot pass the Origin check, so another
// site cannot make changes, or sign anyone in or out, through a visitor's browser.
func (s *Server) fromDashboard(next http.HandlerFunc) http.HandlerFunc {
	return s.fromDashboardUpTo(maxBody, next)
}

func (s *Server) fromDashboardUpTo(limit int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(w, r) {
			return
		}
		if r.Method != http.MethodDelete && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, errBody("Send JSON"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next(w, r)
	}
}

// sameOrigin checks the Origin a browser names, if it names one, and answers when it is another
// site's. The dashboard's own origin is the Host the request came with; behind a reverse proxy on
// this machine that may be the proxy's own (nginx sends 127.0.0.1:8080 unless told otherwise), so
// the host the proxy says it was asked for (X-Forwarded-Host), and the one in DASHBOARD_URL, count
// too. A cross-site fetch cannot set X-Forwarded-Host itself: a header of its own needs the server's
// leave first, which only the site named in WEB_ALLOWED_ORIGIN gets, and that only for reads.
//
// A page rebound by DNS to this machine is its own origin, Host and all, and would pass; headers turns
// those away first (see knownHost).
func (s *Server) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && u.Host != "" {
		if u.Host == r.Host {
			return true
		}
		if behindProxy(r) {
			if fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ","); strings.TrimSpace(fwd) == u.Host {
				return true
			}
		}
		if d, err := url.Parse(s.ctx.Config().DashboardURL); err == nil && d.Host != "" && d.Host == u.Host {
			return true
		}
	}
	// The browser's own word: it sets Sec-Fetch-Site, which no page can set or change, and says
	// same-origin only for a request from a page of the address it is sending to. Behind a proxy that
	// passes on neither the Host nor X-Forwarded-Host, that is the one sign the page is ours.
	if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	msg := "Cross-origin changes are not allowed"
	if behindProxy(r) {
		msg += ". Behind a reverse proxy, have it pass on the Host it was asked for (nginx: proxy_set_header Host $host), or set DASHBOARD_URL"
	}
	writeJSON(w, http.StatusForbidden, errBody(msg))
	return false
}

// behindProxy reports whether the request came through a reverse proxy on this machine.
func behindProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
