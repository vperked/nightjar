package agent

// The dashboard manages the agent's machine's Tailscale through these (see package tailscale). They
// take the token like readings do, and change things only over the HTTPS the token pins.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"nightjar/internal/tailscale"
)

// TailscalePath is where the dashboard manages Tailscale.
const TailscalePath = "/v1/tailscale"

func tailscaleRoutes(mux *http.ServeMux, s *Sampler, authorized func(*http.Request) bool, secure bool) {
	guard := func(w http.ResponseWriter, r *http.Request, change bool) bool {
		switch {
		case !authorized(r):
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case change && !secure:
			http.Error(w, "this agent serves plain HTTP; reinstall it with HTTPS to manage its Tailscale from the dashboard", http.StatusForbidden)
		case s.tailscale == nil:
			http.Error(w, "this agent does not manage Tailscale", http.StatusNotFound)
		default:
			// Logging in with a key waits on tailscaled connecting, past the server's write timeout.
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(firewallWait))
			return true
		}
		return false
	}
	view := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(s.tailscale.State(r.Context()))
	}
	change := func(w http.ResponseWriter, r *http.Request, err error) {
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		view(w, r)
	}
	// via is the dashboard as this machine sees it: over the tailnet, disconnecting cuts it off.
	via := func(r *http.Request) string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
	mux.HandleFunc("GET "+TailscalePath, func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, false) {
			view(w, r)
		}
	})
	mux.HandleFunc("POST "+TailscalePath+"/login", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			AuthKey string `json:"auth_key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil && err != io.EOF {
			http.Error(w, `expected {"auth_key": "tskey-…"} or {}`, http.StatusBadRequest)
			return
		}
		change(w, r, s.tailscale.Login(r.Context(), body.AuthKey))
	})
	mux.HandleFunc("POST "+TailscalePath+"/up", func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, true) {
			change(w, r, s.tailscale.Up(r.Context()))
		}
	})
	mux.HandleFunc("POST "+TailscalePath+"/down", func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, true) {
			change(w, r, s.tailscale.Down(r.Context(), via(r)))
		}
	})
	mux.HandleFunc("POST "+TailscalePath+"/logout", func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, true) {
			change(w, r, s.tailscale.Logout(r.Context(), via(r)))
		}
	})
	mux.HandleFunc("PUT "+TailscalePath+"/settings", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body tailscale.Settings
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
			http.Error(w, "expected the settings to change", http.StatusBadRequest)
			return
		}
		change(w, r, s.tailscale.Set(r.Context(), body))
	})
	mux.HandleFunc("POST "+TailscalePath+"/keep", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			Keep *bool `json:"keep"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Keep == nil {
			http.Error(w, `expected {"keep": true} or {"keep": false}`, http.StatusBadRequest)
			return
		}
		change(w, r, s.tailscale.Keep(r.Context(), *body.Keep))
	})
}

// UseTailscale gives a sampler Tailscale to manage, as Serve does; for tests.
func (s *Sampler) UseTailscale(m *tailscale.Manager) { s.tailscale = m }
