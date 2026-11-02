package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"servermonitor/internal/accounts"
)

// The admin account: create it once with the setup code, then sign in and out.

const cookieName = "servermonitor_session"

func sessionCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// account is what /api/config says about the admin account.
func (s *Server) account() map[string]any {
	if s.cfg.Accounts == nil {
		return map[string]any{"configured": true}
	}
	// The service name lets the setup screen say which journal holds the setup code.
	return map[string]any{"configured": s.cfg.Accounts.Configured(), "username": s.cfg.Accounts.Username(), "service": s.ctx.Config().SystemdServiceName}
}

// setCookie signs the browser in. The cookie is HttpOnly, so scripts on the page never see it, and
// Secure when the dashboard is reached over HTTPS (directly or through a proxy that says so).
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	token, err := s.cfg.Accounts.NewSession(r.UserAgent())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	s.setCookie(w, r, token, int(accounts.SessionLife.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"username": s.cfg.Accounts.Username()})
}

type credentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	SetupCode string `json:"setup_code"`
}

func (s *Server) readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var c credentials
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return c, false
	}
	if s.throttled(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, errBody("Too many tries. Wait a minute and try again."))
		return c, false
	}
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Send a username and password"))
		return c, false
	}
	return c, true
}

// createAdmin makes the admin account (once, with the setup code) and signs this browser in.
func (s *Server) createAdmin(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readCredentials(w, r)
	if !ok {
		return
	}
	switch err := s.cfg.Accounts.Create(c.Username, c.Password, c.SetupCode); {
	case errors.Is(err, accounts.ErrExists):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case errors.Is(err, accounts.ErrSetupCode):
		s.recordFailure(clientIP(r))
		log.Warn("Wrong setup code", "ip", clientIP(r))
		writeJSON(w, http.StatusForbidden, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		s.startSession(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	c, ok := s.readCredentials(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Accounts.Verify(c.Username, c.Password); err != nil {
		if errors.Is(err, accounts.ErrCredentials) {
			s.recordFailure(clientIP(r))
			log.Warn("Failed sign-in", "ip", clientIP(r))
		}
		writeJSON(w, http.StatusUnauthorized, errBody(err.Error()))
		return
	}
	log.Info("Signed in", "ip", clientIP(r))
	s.startSession(w, r)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts != nil {
		s.cfg.Accounts.EndSession(sessionCookie(r))
	}
	s.setCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Send the current and new password"))
		return
	}
	if err := s.cfg.Accounts.ChangePassword(body.Current, body.New, sessionCookie(r)); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, accounts.ErrWrongCurrent) {
			s.recordFailure(clientIP(r))
			code = http.StatusForbidden
		}
		writeJSON(w, code, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}
