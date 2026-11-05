package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"nightjar/internal/agent"
	"nightjar/internal/agentbin"
	"nightjar/internal/remote"
)

// Other servers, watched through the agent on each. Tokens go in and are never sent back out.

func (s *Server) needRemote(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ctx == nil || s.ctx.Remote == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("Watching other servers is not running"))
			return
		}
		next(w, r)
	}
}

// hostListPoints is how much history the list sends for each server's sparklines: ten minutes.
const hostListPoints = 60

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"hosts": s.ctx.Remote.List(hostListPoints), "port": agent.DefaultPort, "agents": agentbin.Builds()})
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request) {
	st, ok := s.ctx.Remote.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, errBody(remote.ErrNotFound.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": st})
}

// downloadAgent hands out the agent built into this monitor, for ?arch=amd64 or arm64.
func (s *Server) downloadAgent(w http.ResponseWriter, r *http.Request) {
	rc, b, err := agentbin.Open(r.URL.Query().Get("arch"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+b.File+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(b.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, rc); err != nil {
		log.Warn("Agent download cut short", "arch", b.Arch, "err", err)
	}
}

// newHostToken makes a token for the next agent, shown in the install command.
func (s *Server) newHostToken(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"token": remote.NewToken(), "port": agent.DefaultPort})
}

// hostBody adds or changes a server: its agent's address and token, or with kind "ssh", a server
// watched through its SSH login alone (renaming one needs only the name).
type hostBody struct {
	Kind    string      `json:"kind"`
	Name    string      `json:"name"`
	Address string      `json:"address"`
	Token   string      `json:"token"`
	SSH     *remote.SSH `json:"ssh"`
}

// errNoAgent answers a request to watch a server with nothing installed: that is an uptime check.
var errNoAgent = errors.New("a server with nothing installed is watched with an uptime check: add one under Services, and pick its server there")

func (s *Server) saveHost(w http.ResponseWriter, r *http.Request) {
	var body hostBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a name, address and token"))
		return
	}
	var st remote.State
	var err error
	id := r.PathValue("id")
	switch {
	case body.Kind == remote.KindCheck:
		err = errNoAgent
	case body.Kind == remote.KindSSH && id != "":
		st, err = s.ctx.Remote.Rename(id, body.Name)
	case body.Kind == remote.KindSSH && body.SSH == nil:
		err = errors.New("give the server's address and its SSH login")
	case body.Kind == remote.KindSSH:
		st, err = s.ctx.Remote.AddSSHServer(r.Context(), body.Name, *body.SSH)
		if err == nil {
			s.savedLogin(w, r, st)
			return
		}
	case id != "":
		st, err = s.ctx.Remote.Update(r.Context(), id, body.Name, body.Address, body.Token)
	default:
		st, err = s.ctx.Remote.Add(r.Context(), body.Name, body.Address, body.Token)
	}
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case errors.Is(err, remote.ErrExists):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case err != nil:
		// Mostly the agent not answering; the message says why.
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"host": st})
	}
}

// deleteHost stops watching a server. Checks that belonged to it stay, belonging to none.
func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.ctx.Remote.Remove(id); err != nil {
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	}
	if s.ctx.Services != nil {
		for _, spec := range s.ctx.Services.Specs() {
			if spec.Server == id {
				spec.Server = ""
				if err := s.ctx.Services.Save(spec.Name, spec); err != nil {
					log.Warn("Could not unlink a check from its removed server", "check", spec.Name, "err", err)
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- SSH ----------

// setHostSSH saves how to log in to a server ({"key", "user", "port", "host"}), or with DELETE forgets it.
func (s *Server) setHostSSH(w http.ResponseWriter, r *http.Request) {
	var c *remote.SSH
	if r.Method != http.MethodDelete {
		c = &remote.SSH{}
		if err := json.NewDecoder(r.Body).Decode(c); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("Expected a key, user and port"))
			return
		}
	}
	st, err := s.ctx.Remote.SetSSH(r.Context(), r.PathValue("id"), c)
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	case c == nil:
		writeJSON(w, http.StatusOK, map[string]any{"host": st})
	default:
		s.savedLogin(w, r, st)
	}
}

// savedLogin answers a login just saved: it is tried at once, so a key the server refuses shows now,
// not later, and the server comes back with how that went.
func (s *Server) savedLogin(w http.ResponseWriter, r *http.Request, st remote.State) {
	test := s.testLogin(r, st.ID)
	if now, ok := s.ctx.Remote.Get(st.ID); ok {
		st = now
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": st, "test": test})
}

// loginTest is how trying a server's SSH login went.
type loginTest struct {
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// testLogin logs in to a server once, the way Test login does.
func (s *Server) testLogin(r *http.Request, id string) loginTest {
	out, err := s.ctx.Remote.RunSSH(r.Context(), id, remote.SSHTest)
	if err != nil {
		return loginTest{Error: err.Error(), Output: out}
	}
	return loginTest{OK: true, Output: out}
}

// runHostSSH logs in to a server and runs test, restart-agent or reboot. The action runs to its end
// (it has its own time limit) even if the browser goes away: cut off halfway, a reinstall could leave
// the agent stopped.
func (s *Server) runHostSSH(w http.ResponseWriter, r *http.Request) {
	out, err := s.ctx.Remote.RunSSH(context.WithoutCancel(r.Context()), r.PathValue("id"), r.PathValue("action"))
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case err != nil:
		writeJSON(w, statusRefused, map[string]any{"error": err.Error(), "output": out})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"output": out})
	}
}

// sshKeys lists the key pairs Nightjar can log in with. Only public halves and paths leave here.
func (s *Server) sshKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"keys": s.ctx.Remote.SSHKeys(r.Context())})
}

// newSSHKey makes a key pair in Nightjar's SSH folder ({"name": "nightjar"}), or with "private",
// keeps a private key someone already has there. The private key never comes back out.
func (s *Server) newSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Private string `json:"private"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a name"))
		return
	}
	var k remote.SSHKey
	var err error
	if strings.TrimSpace(body.Private) != "" {
		k, err = s.ctx.Remote.ImportSSHKey(r.Context(), body.Name, body.Private)
	} else {
		k, err = s.ctx.Remote.NewSSHKey(r.Context(), body.Name)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": k})
}

// authorizeSSHKey puts a login's key on its server with the account's password, used for that one
// login and kept nowhere: {"id": "<server, if it has one yet>", "login": {...}, "password": "..."}.
func (s *Server) authorizeSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string     `json:"id"`
		Login    remote.SSH `json:"login"`
		Password string     `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a login and a password"))
		return
	}
	// Like runHostSSH, it finishes even if the browser goes away, within its own time limit.
	out, err := s.ctx.Remote.AuthorizeKey(context.WithoutCancel(r.Context()), body.ID, body.Login, body.Password)
	var hk *remote.HostKeyError
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case errors.As(err, &hk):
		// Nobody has confirmed the server's host key: the browser shows it, to check and trust.
		writeJSON(w, statusRefused, map[string]string{"error": err.Error(), "host_key": hk.HostKey})
	case err != nil:
		writeJSON(w, statusRefused, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
	}
}

// trustSSHHostKey keeps a server's host key once the admin has checked the fingerprint that
// authorizeSSHKey showed them, so the password can go to that server.
func (s *Server) trustSSHHostKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string     `json:"id"`
		Login   remote.SSH `json:"login"`
		HostKey string     `json:"host_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a login and a host key"))
		return
	}
	err := s.ctx.Remote.TrustHostKey(r.Context(), body.ID, body.Login, body.HostKey)
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case err != nil:
		writeJSON(w, statusRefused, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
