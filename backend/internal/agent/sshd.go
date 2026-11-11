package agent

// The dashboard manages who may log in to the agent's machine over SSH through these: the public
// keys each account lets in, and whether sshd still takes passwords (see package sshd). They take the
// token like readings do, and change things only over the HTTPS the token pins.

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"nightjar/internal/sshd"
)

// SSHDPath is where the dashboard manages SSH logins.
const SSHDPath = "/v1/sshd"

func sshdRoutes(mux *http.ServeMux, s *Sampler, authorized func(*http.Request) bool, secure bool) {
	guard := func(w http.ResponseWriter, r *http.Request, change bool) bool {
		switch {
		case !authorized(r):
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case change && !secure:
			http.Error(w, "this agent serves plain HTTP; reinstall it with HTTPS to manage its SSH logins from the dashboard", http.StatusForbidden)
		case s.sshd == nil:
			http.Error(w, "this agent does not manage SSH logins", http.StatusNotFound)
		default:
			// sshd checking its config and reloading may take longer than the server's write timeout.
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(firewallWait))
			return true
		}
		return false
	}
	view := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(s.sshd.State(r.Context()))
	}
	// change answers with the logins after a change, or the reason it was turned down.
	change := func(w http.ResponseWriter, r *http.Request, err error) {
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		view(w, r)
	}
	mux.HandleFunc("GET "+SSHDPath, func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, false) {
			view(w, r)
		}
	})
	mux.HandleFunc("POST "+SSHDPath+"/keys", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			User string `json:"user"`
			Key  string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.User == "" || body.Key == "" {
			http.Error(w, `expected {"user": "root", "key": "ssh-ed25519 AAAA…"}`, http.StatusBadRequest)
			return
		}
		change(w, r, s.sshd.AddKey(r.Context(), body.User, body.Key))
	})
	mux.HandleFunc("PUT "+SSHDPath+"/keys-only", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body struct {
			On   *bool  `json:"on"`
			User string `json:"user"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.On == nil {
			http.Error(w, `expected {"on": true, "user": "root"} or {"on": false}`, http.StatusBadRequest)
			return
		}
		change(w, r, s.sshd.SetKeysOnly(r.Context(), *body.On, body.User))
	})
	mux.HandleFunc("POST "+SSHDPath+"/keys-only/keep", func(w http.ResponseWriter, r *http.Request) {
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
		change(w, r, s.sshd.Keep(r.Context(), *body.Keep))
	})
}

// UseSSHD gives a sampler SSH logins to manage, as Serve does; for tests.
func (s *Sampler) UseSSHD(m *sshd.Manager) { s.sshd = m }
