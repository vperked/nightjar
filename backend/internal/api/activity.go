package api

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"nightjar/internal/storage"
)

// The activity log: every change made through the dashboard or the API, each sign-in, each
// Discord button pressed, and what Nightjar did on its own (repairs, cleanup removals).

// who is whoever sent the request: the admin by name, or the API token.
func (s *Server) who(r *http.Request) string {
	if s.cfg.AuthToken != "" && subtle.ConstantTimeCompare([]byte(presentedToken(r)), []byte(s.cfg.AuthToken)) == 1 {
		return "API token"
	}
	if s.cfg.Accounts != nil && s.cfg.Accounts.Username() != "" {
		return s.cfg.Accounts.Username()
	}
	return "dashboard"
}

func (s *Server) record(r *http.Request, action, kind, target, detail string) {
	s.cfg.Store.AddActivity(storage.Activity{TS: nowSeconds(), Who: s.who(r), Kind: kind, Action: action, Target: target, Detail: detail})
}

// actions words each change by its route; {x} takes the request's path value.
var actions = map[string][2]string{
	"POST /api/checks":                  {"check", "Added a check"},
	"PUT /api/checks/{name}":            {"check", "Changed the check {name}"},
	"DELETE /api/checks/{name}":         {"check", "Deleted the check {name}"},
	"POST /api/checks/{name}/repair":    {"repair", "Ran the repair of {name}"},
	"PUT /api/settings":                 {"settings", "Changed settings"},
	"PUT /api/appearance":               {"settings", "Changed the appearance"},
	"PUT /api/statuspage":               {"status page", "Changed the status page"},
	"PUT /api/incidents/{id}/feedback":  {"incident", "Gave feedback on an incident"},
	"PUT /api/incidents/{id}/notice":    {"status page", "Saved an incident notice"},
	"DELETE /api/incidents/{id}/notice": {"status page", "Took down an incident notice"},
	"POST /api/maintenance":             {"maintenance", "Planned maintenance"},
	"PUT /api/maintenance/{id}":         {"maintenance", "Changed a maintenance window"},
	"DELETE /api/maintenance/{id}":      {"maintenance", "Removed a maintenance window"},
	"POST /api/cleanup/scan":            {"cleanup", "Ran the media cleanup"},
	"PUT /api/cleanup/{key}/keep":       {"cleanup", "Kept a title from the cleanup"},
	"DELETE /api/cleanup/{key}/keep":    {"cleanup", "Let the cleanup list a title again"},
	"POST /api/auth/password":           {"account", "Changed the password"},
	"POST /api/auth/2fa/enable":         {"account", "Turned on two-factor sign-in"},
	"POST /api/auth/2fa/disable":        {"account", "Turned off two-factor sign-in"},
	"POST /api/report/send":             {"report", "Sent the weekly report"},
	"POST /api/backup/restore":          {"settings", "Restored a backup"},
	"POST /api/units/{name}/{action}":   {"service", "{action} {name}"},
	"POST /api/plex/streams/{id}/stop":  {"plex", "Stopped a Plex stream"},
	"POST /api/qbittorrent/action":      {"torrents", "Acted on torrents"},
	"POST /api/qbittorrent/add":         {"torrents", "Added torrents"},
	"POST /api/apps/{id}/install":       {"apps", "Installed {id}"},
	"POST /api/apps/{id}/remove":        {"apps", "Removed {id}"},
	"PUT /api/plugins/{id}":             {"plugins", "Changed the plugin {id}"},
	"PUT /api/notifications/rules":      {"alerts", "Changed who hears which alerts"},
	"POST /api/notifications/mute":      {"alerts", "Muted or unmuted alerts"},
	"POST /api/hosts":                   {"servers", "Added a server"},
	"PUT /api/hosts/{id}":               {"servers", "Changed a server"},
	"DELETE /api/hosts/{id}":            {"servers", "Removed a server"},
	"PUT /api/hosts/{id}/ssh":           {"servers", "Set a server's SSH login"},
	"DELETE /api/hosts/{id}/ssh":        {"servers", "Removed a server's SSH login"},
	"POST /api/hosts/{id}/ssh/{action}": {"servers", "Ran {action} over SSH"},
	"POST /api/ssh/keys":                {"servers", "Added an SSH key"},
	"POST /api/ssh/authorize":           {"servers", "Put an SSH key on a server"},
	"POST /api/updates/{id}/update":     {"updates", "Updated {id}"},
	"PUT /api/updates/{id}":             {"updates", "Changed how {id} is updated"},
	"DELETE /api/updates/{id}":          {"updates", "Stopped watching {id} for updates"},
}

// skipped changes record nothing: trying things out, and the reads that use POST.
var skipped = []string{"/test", "/try", "/2fa/begin", "/api/auth/login", "/api/auth/logout", "/api/notifications/read", "/api/updates/check", "/detect", "/rescan", "/api/hosts/token"}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logged records a change once it has worked.
func (s *Server) logged(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next(sw, r)
		if sw.status >= 400 || r.Method == http.MethodGet {
			return
		}
		for _, p := range skipped {
			if strings.HasSuffix(r.URL.Path, p) {
				return
			}
		}
		// Checking one update target for news changes nothing either.
		if strings.HasPrefix(r.URL.Path, "/api/updates/") && strings.HasSuffix(r.URL.Path, "/check") {
			return
		}
		kind, action := "change", r.Method+" "+r.URL.Path
		if a, ok := actions[r.Pattern]; ok {
			kind, action = a[0], a[1]
			for _, name := range []string{"name", "id", "key", "action"} {
				if v := r.PathValue(name); v != "" {
					action = strings.ReplaceAll(action, "{"+name+"}", v)
				}
			}
			if action != "" {
				action = strings.ToUpper(action[:1]) + action[1:]
			}
		}
		s.record(r, action, kind, r.PathValue("name"), "")
	}
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 100
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	writeJSON(w, http.StatusOK, map[string]any{"activity": s.cfg.Store.Activities(limit, before)})
}
