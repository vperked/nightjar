package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"nightjar/internal/accounts"
)

// The admin account: create it once with the setup code, then sign in and out.

const cookieName = "nightjar_session"

// deviceCookieName marks a browser that signed in before (see accounts.NewDevice). It goes only to the
// sign-in routes, and lasts a year: about as long as browsers keep a cookie.
const (
	deviceCookieName = "nightjar_device"
	deviceCookieLife = 365 * 24 * time.Hour
)

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
func (s *Server) account(signedIn bool, who accounts.Who) map[string]any {
	if s.cfg.Accounts == nil {
		return map[string]any{"configured": true}
	}
	// The service name lets the setup screen say which journal holds the setup code.
	out := map[string]any{"configured": s.cfg.Accounts.Configured(), "service": s.ctx.Config().SystemdServiceName, "two_factor": s.cfg.Accounts.TwoFactor()}
	if signedIn {
		out["role"] = cmp.Or(who.Role, accounts.RoleAdmin)
		if who.Admin() {
			out["username"], out["recovery_left"] = s.cfg.Accounts.Username(), s.cfg.Accounts.RecoveryLeft()
		} else {
			// A viewer has no two-factor of its own; the admin's is not its business.
			out["username"], out["two_factor"] = who.Username, false
		}
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

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, who accounts.Who) {
	token, err := s.cfg.Accounts.NewSessionFor(who, r.UserAgent())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	// The cookie lasts as long as the session can at most; the server ends it sooner when it goes
	// unused (accounts.SessionLife), and auth then clears the cookie.
	s.setCookie(w, r, token, int(accounts.SessionMaxAge.Seconds()))
	if who.Admin() {
		who.Username = s.cfg.Accounts.Username()
	}
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookieName, Value: s.cfg.Accounts.NewDevice(who.Username), Path: "/api/auth/", MaxAge: int(deviceCookieLife.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
	})
	writeJSON(w, http.StatusOK, map[string]any{"username": who.Username, "role": who.Role})
}

type credentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	SetupCode string `json:"setup_code"`
	Code      string `json:"code"` // the authenticator's, with two-factor sign-in on
}

// readCredentials reads a sign-in's credentials and starts a try at them (see passwordTry), at the
// setup code or at the account named; finish ends it.
func (s *Server) readCredentials(w http.ResponseWriter, r *http.Request, setup bool) (c credentials, finish func(tryResult), ok bool) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return c, nil, false
	}
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Send a username and password"))
		return c, nil, false
	}
	account := c.Username
	if setup {
		account = setupAccount
	}
	finish, ok = s.passwordTry(w, r, account)
	return c, finish, ok
}

// createAdmin makes the admin account (once, with the setup code) and signs this browser in.
func (s *Server) createAdmin(w http.ResponseWriter, r *http.Request) {
	c, finish, ok := s.readCredentials(w, r, true)
	if !ok {
		return
	}
	err := s.cfg.Accounts.Create(c.Username, c.Password, c.SetupCode)
	finish(guessed(err, accounts.ErrSetupCode))
	switch {
	case errors.Is(err, accounts.ErrExists):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case errors.Is(err, accounts.ErrSetupCode):
		log.Warn("Wrong setup code", "ip", s.clientNames(r))
		writeJSON(w, http.StatusForbidden, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		s.startSession(w, r, accounts.Who{Role: accounts.RoleAdmin})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	c, finish, ok := s.readCredentials(w, r, false)
	if !ok {
		return
	}
	who, err := s.cfg.Accounts.Login(c.Username, c.Password)
	if err != nil {
		wrong := errors.Is(err, accounts.ErrCredentials)
		finish(guessed(err, accounts.ErrCredentials))
		if wrong {
			log.Warn("Failed sign-in", "ip", s.clientNames(r))
		}
		writeJSON(w, http.StatusUnauthorized, errBody(err.Error()))
		return
	}
	// With two-factor sign-in on, the password alone is not enough: the page then asks for a code.
	if who.Admin() && s.cfg.Accounts.TwoFactor() {
		if strings.TrimSpace(c.Code) == "" {
			finish(notAGuess)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Enter the code from your authenticator app", "need_code": true, "fields": map[string]string{"code": "needed"}})
			return
		}
		if err := s.cfg.Accounts.CheckCode(c.Code); err != nil {
			finish(guessed(err, accounts.ErrCode))
			log.Warn("Failed sign-in: wrong code", "ip", s.clientNames(r))
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error(), "need_code": true, "fields": map[string]string{"code": err.Error()}})
			return
		}
	}
	finish(rightGuess)
	log.Info("Signed in", "user", who.Username, "role", who.Role, "ip", s.clientNames(r))
	s.record(r.WithContext(context.WithValue(r.Context(), whoKey{}, who)), "Signed in", "account", "", "from "+s.clientNames(r))
	s.startSession(w, r, who)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts != nil {
		s.cfg.Accounts.EndSession(sessionCookie(r))
	}
	s.setCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// guessed is how a try went, by the error it ended with: right with none, wrong with one of wrong,
// and nothing guessed otherwise (a new password too short, say).
func guessed(err error, wrong ...error) tryResult {
	if err == nil {
		return rightGuess
	}
	for _, e := range wrong {
		if errors.Is(err, e) {
			return wrongGuess
		}
	}
	return notAGuess
}

// accountOf is the name of the account a signed-in request is from, for the per-account limits: the
// admin's for the API token too.
func (s *Server) accountOf(who accounts.Who) string {
	if who.Admin() {
		return s.cfg.Accounts.Username()
	}
	return who.Username
}

// The signed-in account's own changes ask for the password again, and each is a try at it like a
// sign-in (see passwordTry): a session left open or stolen cannot be used to guess the password.

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
	finish, ok := s.passwordTry(w, r, s.accountOf(whoFrom(r)))
	if !ok {
		return
	}
	var err error
	if who := whoFrom(r); who.Role == accounts.RoleViewer {
		err = s.cfg.Accounts.ChangeViewerPassword(who.Username, body.Current, body.New, sessionCookie(r))
	} else {
		err = s.cfg.Accounts.ChangePassword(body.Current, body.New, sessionCookie(r))
	}
	finish(guessed(err, accounts.ErrWrongCurrent))
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, accounts.ErrWrongCurrent) {
			code = http.StatusForbidden
		}
		writeJSON(w, code, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}

// beginTwoFactor offers a new authenticator secret, to confirm with enableTwoFactor.
func (s *Server) beginTwoFactor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	secret, link, err := s.cfg.Accounts.BeginTwoFactor()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret, "link": link})
}

// enableTwoFactor turns two-factor sign-in on, with the password and a code from the new secret.
func (s *Server) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	finish, ok := s.passwordTry(w, r, s.accountOf(whoFrom(r)))
	if !ok {
		return
	}
	codes, err := s.cfg.Accounts.EnableTwoFactor(in.Password, in.Code, sessionCookie(r))
	// A wrong code here is for the new authenticator, not a guess at the account.
	finish(guessed(err, accounts.ErrWrongCurrent))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery": codes})
}

func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	finish, ok := s.passwordTry(w, r, s.accountOf(whoFrom(r)))
	if !ok {
		return
	}
	err := s.cfg.Accounts.DisableTwoFactor(in.Password, in.Code, sessionCookie(r))
	finish(guessed(err, accounts.ErrWrongCurrent, accounts.ErrCode))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- viewers ----------

// Viewers are read-only accounts; only the admin lists, adds and removes them.

func (s *Server) listViewers(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusOK, map[string]any{"viewers": []accounts.Viewer{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": s.cfg.Accounts.Viewers()})
}

func (s *Server) addViewer(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Send a username and password"))
		return
	}
	switch err := s.cfg.Accounts.AddViewer(body.Username, body.Password); {
	case errors.Is(err, accounts.ErrViewerExists):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"viewers": s.cfg.Accounts.Viewers()})
	}
}

func (s *Server) setViewerPassword(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Send the new password"))
		return
	}
	name := r.PathValue("name")
	switch err := s.cfg.Accounts.SetViewerPassword(name, body.Password); {
	case errors.Is(err, accounts.ErrNoViewer):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"viewers": s.cfg.Accounts.Viewers()})
	}
}

func (s *Server) removeViewer(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Accounts == nil {
		writeJSON(w, http.StatusNotFound, errBody("Accounts are not enabled"))
		return
	}
	name := r.PathValue("name")
	if err := s.cfg.Accounts.RemoveViewer(name); err != nil {
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": s.cfg.Accounts.Viewers()})
}
