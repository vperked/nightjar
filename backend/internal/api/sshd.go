package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"nightjar/internal/sshd"
)

// sshdBudget is the longest a change of SSH logins takes: sshd checking its config, a reload, and
// putting it back when sshd still takes passwords.
const sshdBudget = time.Minute

// readSSHKey reads {"user": "root", "key": "ssh-ed25519 AAAA… comment"}.
func readSSHKey(w http.ResponseWriter, r *http.Request) (user, key string, ok bool) {
	var body struct {
		User string `json:"user"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.User == "" || body.Key == "" {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"user": "root", "key": "ssh-ed25519 AAAA…"}`))
		return "", "", false
	}
	return body.User, body.Key, true
}

// readKeysOnly reads {"on": true, "user": "root"} or {"on": false}.
func readKeysOnly(w http.ResponseWriter, r *http.Request) (on bool, user string, ok bool) {
	var body struct {
		On   *bool  `json:"on"`
		User string `json:"user"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.On == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"on": true, "user": "root"} or {"on": false}`))
		return false, "", false
	}
	return *body.On, body.User, true
}

// ---------- this machine's SSH logins ----------

func (s *Server) localSSHD(w http.ResponseWriter, r *http.Request) {
	if s.ctx.SSHD == nil {
		writeJSON(w, http.StatusOK, sshd.Access{Ports: []uint16{}, Users: []sshd.User{}, Problem: "This dashboard does not manage SSH logins."})
		return
	}
	writeJSON(w, http.StatusOK, s.ctx.SSHD.State(r.Context()))
}

// sshdChange runs a change of this machine's SSH logins and answers with them after it.
func (s *Server) sshdChange(w http.ResponseWriter, r *http.Request, change func() error) {
	if s.ctx.SSHD == nil {
		writeJSON(w, http.StatusConflict, errBody("This dashboard does not manage SSH logins"))
		return
	}
	if err := change(); err != nil {
		writeJSON(w, statusRefused, errBody(err.Error()))
		return
	}
	s.localSSHD(w, r)
}

// addLocalSSHKey adds a public key to an account here.
func (s *Server) addLocalSSHKey(w http.ResponseWriter, r *http.Request) {
	user, key, ok := readSSHKey(w, r)
	if !ok {
		return
	}
	s.sshdChange(w, r, func() error { return s.ctx.SSHD.AddKey(r.Context(), user, key) })
}

// setLocalKeysOnly turns password logins off or on. Off goes back by itself within two minutes
// unless kept, so a key that does not work cannot lock the owner out.
func (s *Server) setLocalKeysOnly(w http.ResponseWriter, r *http.Request) {
	on, user, ok := readKeysOnly(w, r)
	if !ok {
		return
	}
	s.sshdChange(w, r, func() error { return s.ctx.SSHD.SetKeysOnly(r.Context(), on, user) })
}

func (s *Server) keepLocalKeysOnly(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	s.sshdChange(w, r, func() error { return s.ctx.SSHD.Keep(r.Context(), keep) })
}

// ---------- a server's SSH logins, through its agent ----------

func (s *Server) hostSSHD(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.SSHD(r.Context(), r.PathValue("id"))
	answerAgent(w, v, err)
}

func (s *Server) addHostSSHKey(w http.ResponseWriter, r *http.Request) {
	user, key, ok := readSSHKey(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.AddSSHKey(r.Context(), r.PathValue("id"), user, key)
	answerAgent(w, v, err)
}

func (s *Server) setHostKeysOnly(w http.ResponseWriter, r *http.Request) {
	on, user, ok := readKeysOnly(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.SetKeysOnly(r.Context(), r.PathValue("id"), on, user)
	answerAgent(w, v, err)
}

func (s *Server) keepHostKeysOnly(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.KeepKeysOnly(r.Context(), r.PathValue("id"), keep)
	answerAgent(w, v, err)
}
