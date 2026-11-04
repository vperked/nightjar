package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"nightjar/internal/accounts"
)

// The admin account: create it once with the setup code, then sign in and out.

const cookieName = "nightjar_session"

func sessionCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// account is what /api/config says about the admin account.
// account describes the admin account. Signed out, it says only what the sign-in and setup screens
// need: whether there is an account, whether it asks for a code, and (for the setup code) which
// journal to look in. The username and recovery codes are for whoever is signed in.
func (s *Server) account(signedIn bool) map[string]any {
	if s.cfg.Accounts == nil {
		return map[string]any{"configured": true}
	}
	// The service name lets the setup screen say which journal holds the setup code.
	out := map[string]any{"configured": s.cfg.Accounts.Configured(), "service": s.ctx.Config().SystemdServiceName, "two_factor": s.cfg.Accounts.TwoFactor()}
	if signedIn {
		out["username"], out["recovery_left"] = s.cfg.Accounts.Username(), s.cfg.Accounts.RecoveryLeft()
	}
	return out
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
	Code      string `json:"code"` // the authenticator's, with two-factor sign-in on
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
	// With two-factor sign-in on, the password alone is not enough: the page then asks for a code.
	if s.cfg.Accounts.TwoFactor() {
		if strings.TrimSpace(c.Code) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Enter the code from your authenticator app", "need_code": true, "fields": map[string]string{"code": "needed"}})
			return
		}
		if err := s.cfg.Accounts.CheckCode(c.Code); err != nil {
			s.recordFailure(clientIP(r))
			log.Warn("Failed sign-in: wrong code", "ip", clientIP(r))
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "need_code": true, "fields": map[string]string{"code": err.Error()}})
			return
		}
	}
	log.Info("Signed in", "ip", clientIP(r))
	s.record(r, "Signed in", "account", "", "from "+clientIP(r))
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

// beginTwoFactor offers a new authenticator secret, to confirm with enableTwoFactor.
func (s *Server) beginTwoFactor(w http.ResponseWriter, r *http.Request) {
	secret, link, err := s.cfg.Accounts.BeginTwoFactor()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "link": link})
}

func (s *Server) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	codes, err := s.cfg.Accounts.EnableTwoFactor(in.Code)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery": codes})
}

func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if err := s.cfg.Accounts.DisableTwoFactor(in.Password, in.Code); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
