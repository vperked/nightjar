package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"nightjar/internal/tailscale"
)

// tailscaleBudget is the longest a Tailscale change takes: logging in with a key waits up to twenty
// seconds for tailscaled to connect.
const tailscaleBudget = time.Minute

// readTailscaleLogin reads {"auth_key": "tskey-…"}, or {} to log in with a link.
func readTailscaleLogin(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		AuthKey string `json:"auth_key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"auth_key": "tskey-…"} or {}`))
		return "", false
	}
	return body.AuthKey, true
}

func readTailscaleSettings(w http.ResponseWriter, r *http.Request) (tailscale.Settings, bool) {
	var s tailscale.Settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&s); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected the settings to change"))
		return s, false
	}
	return s, true
}

// readTailscaleAction is the {action} in the path: up, down or logout.
func readTailscaleAction(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch a := r.PathValue("action"); a {
	case "up", "down", "logout":
		return a, true
	}
	writeJSON(w, http.StatusNotFound, errBody("Unknown action; it is up, down or logout"))
	return "", false
}

// ---------- this machine's Tailscale ----------

func (s *Server) localTailscale(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Tailscale == nil {
		writeJSON(w, http.StatusOK, tailscale.State{Peers: []tailscale.Node{}, Health: []string{}, Settings: tailscale.Current{AdvertiseRoutes: []string{}}, Problem: "This dashboard does not manage Tailscale."})
		return
	}
	writeJSON(w, http.StatusOK, s.ctx.Tailscale.State(r.Context()))
}

// tailscaleChange runs a change of this machine's Tailscale and answers with it after.
func (s *Server) tailscaleChange(w http.ResponseWriter, r *http.Request, change func() error) {
	if s.ctx.Tailscale == nil {
		writeJSON(w, http.StatusConflict, errBody("This dashboard does not manage Tailscale"))
		return
	}
	if err := change(); err != nil {
		writeJSON(w, statusRefused, errBody(err.Error()))
		return
	}
	s.localTailscale(w, r)
}

func (s *Server) localTailscaleLogin(w http.ResponseWriter, r *http.Request) {
	key, ok := readTailscaleLogin(w, r)
	if !ok {
		return
	}
	s.tailscaleChange(w, r, func() error { return s.ctx.Tailscale.Login(r.Context(), key) })
}

// localTailscaleAction connects, disconnects or logs out. Those last two are refused when the
// browser reaches this machine over Tailscale: they would cut it off.
func (s *Server) localTailscaleAction(w http.ResponseWriter, r *http.Request) {
	action, ok := readTailscaleAction(w, r)
	if !ok {
		return
	}
	via := s.clientIP(r)
	s.tailscaleChange(w, r, func() error {
		switch action {
		case "down":
			return s.ctx.Tailscale.Down(r.Context(), via)
		case "logout":
			return s.ctx.Tailscale.Logout(r.Context(), via)
		}
		return s.ctx.Tailscale.Up(r.Context())
	})
}

func (s *Server) setLocalTailscale(w http.ResponseWriter, r *http.Request) {
	settings, ok := readTailscaleSettings(w, r)
	if !ok {
		return
	}
	s.tailscaleChange(w, r, func() error { return s.ctx.Tailscale.Set(r.Context(), settings) })
}

func (s *Server) keepLocalTailscale(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	s.tailscaleChange(w, r, func() error { return s.ctx.Tailscale.Keep(r.Context(), keep) })
}

// ---------- a server's Tailscale, through its agent ----------

func (s *Server) hostTailscale(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.Tailscale(r.Context(), r.PathValue("id"))
	answerAgent(w, v, err)
}

func (s *Server) hostTailscaleLogin(w http.ResponseWriter, r *http.Request) {
	key, ok := readTailscaleLogin(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.TailscaleLogin(r.Context(), r.PathValue("id"), key)
	answerAgent(w, v, err)
}

func (s *Server) hostTailscaleAction(w http.ResponseWriter, r *http.Request) {
	action, ok := readTailscaleAction(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.TailscaleAction(r.Context(), r.PathValue("id"), action)
	answerAgent(w, v, err)
}

func (s *Server) setHostTailscale(w http.ResponseWriter, r *http.Request) {
	settings, ok := readTailscaleSettings(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.SetTailscale(r.Context(), r.PathValue("id"), settings)
	answerAgent(w, v, err)
}

func (s *Server) keepHostTailscale(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.KeepTailscale(r.Context(), r.PathValue("id"), keep)
	answerAgent(w, v, err)
}
