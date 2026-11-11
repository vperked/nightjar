// Package accounts keeps the dashboard's admin account, its read-only viewers, and who is signed in. The dashboard does
// nothing until the admin account exists, and creating it needs a one-time setup code from the
// monitor's log, so whoever reaches a fresh install first cannot claim it.
package accounts

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"nightjar/internal/logx"
	"nightjar/internal/storage"
)

var log = logx.Component("Accounts")

const (
	adminKey = "auth.admin"
	// iterations follows OWASP's advice for PBKDF2-SHA256.
	iterations = 600_000
	// SessionLife is how long a browser stays signed in without being used.
	SessionLife = 30 * 24 * time.Hour
	// SessionMaxAge is how long a browser stays signed in at most, however often it is used, so a
	// stolen cookie that is kept busy does not work for ever.
	SessionMaxAge = 90 * 24 * time.Hour
	// MinPassword is the shortest password accepted.
	MinPassword = 10
)

var (
	ErrExists       = errors.New("an admin account already exists")
	ErrNoAdmin      = errors.New("no admin account yet")
	ErrSetupCode    = errors.New("that setup code is not right; it is in the monitor's log")
	ErrCredentials  = errors.New("wrong username or password")
	ErrWrongCurrent = errors.New("the current password is not right")
	usernameRE      = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
)

// admin is the stored account. Hash is "pbkdf2-sha256$<iterations>$<salt>$<key>".
type admin struct {
	Username string  `json:"username"`
	Hash     string  `json:"hash"`
	Created  float64 `json:"created"`
	// TOTP is the authenticator secret (base32) once two-factor sign-in is on; Recovery holds the
	// hashes of the one-time recovery codes left, and LastStep the last code's time step, so a
	// code cannot be used twice.
	TOTP     string   `json:"totp,omitempty"`
	Recovery []string `json:"recovery,omitempty"`
	LastStep int64    `json:"last_step,omitempty"`
}

// Accounts is safe for concurrent use.
type Accounts struct {
	store *storage.Store
	now   func() time.Time

	mu        sync.Mutex
	admin     *admin
	viewers   []viewer
	setupCode string
	sessions  map[string]*storage.Session // by hash of the cookie value
	pending   string                      // a TOTP secret offered, not yet confirmed
	device    []byte                      // signs device cookies (see NewDevice), loaded when first needed
}

// New loads the admin account and sessions. Without an account it makes a setup code and logs it.
func New(store *storage.Store) *Accounts {
	a := &Accounts{store: store, now: time.Now, sessions: map[string]*storage.Session{}}
	if raw := store.Meta(adminKey); raw != "" {
		var ad admin
		if err := json.Unmarshal([]byte(raw), &ad); err == nil && ad.Username != "" {
			a.admin = &ad
		}
	}
	a.loadViewers()
	for _, s := range store.Sessions() {
		a.sessions[s.Hash] = &s
	}
	a.mu.Lock()
	a.pruneLocked()
	a.mu.Unlock()
	if a.admin == nil {
		a.setupCode = newSetupCode()
		log.Warn("No admin account yet. Open the dashboard to create one with this setup code", "setup_code", a.setupCode)
	}
	return a
}

func newSetupCode() string {
	b := make([]byte, 5)
	rand.Read(b)
	s := strings.ToUpper(hex.EncodeToString(b))
	return s[:5] + "-" + s[5:]
}

// Configured reports whether the admin account exists.
func (a *Accounts) Configured() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admin != nil
}

// Username is the admin's name ("" before the account exists).
func (a *Accounts) Username() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin == nil {
		return ""
	}
	return a.admin.Username
}

// SetupCode is the code that creating the admin account needs ("" once it exists).
func (a *Accounts) SetupCode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupCode
}

func validate(username, password string) error {
	switch {
	case !usernameRE.MatchString(username):
		return errors.New("a username is up to 64 letters, digits and . _ @ -")
	case utf8.RuneCountInString(password) < MinPassword:
		return fmt.Errorf("use a password of at least %d characters", MinPassword)
	case len(password) > 1024:
		return errors.New("that password is too long")
	case strings.EqualFold(password, username):
		return errors.New("the password cannot be the username")
	}
	return nil
}

// Create makes the admin account. It needs the setup code from the log.
func (a *Accounts) Create(username, password, code string) error {
	username = strings.TrimSpace(username)
	if err := validate(username, password); err != nil {
		return err
	}
	hash := hashPassword(password)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin != nil {
		return ErrExists
	}
	given := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	if subtle.ConstantTimeCompare([]byte(given), []byte(a.setupCode)) != 1 {
		return ErrSetupCode
	}
	ad := &admin{Username: username, Hash: hash, Created: float64(a.now().Unix())}
	if err := a.saveLocked(ad); err != nil {
		return err
	}
	a.admin, a.setupCode = ad, ""
	log.Info("Admin account created", "username", username)
	return nil
}

func (a *Accounts) saveLocked(ad *admin) error {
	raw, _ := json.Marshal(ad)
	if err := a.store.SetMeta(adminKey, string(raw)); err != nil {
		return fmt.Errorf("could not save the account: %w", err)
	}
	return nil
}

// Verify checks the admin's username and password.
func (a *Accounts) Verify(username, password string) error {
	a.mu.Lock()
	ad := a.admin
	a.mu.Unlock()
	if ad == nil {
		return ErrNoAdmin
	}
	// The password is checked even for a wrong name, so timing does not reveal the username.
	okPass := checkPassword(ad.Hash, password)
	okName := subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(username))), []byte(strings.ToLower(ad.Username))) == 1
	if !okPass || !okName {
		return ErrCredentials
	}
	return nil
}

// ChangePassword replaces the password and signs out every other browser.
func (a *Accounts) ChangePassword(current, next, keepToken string) error {
	a.mu.Lock()
	ad := a.admin
	a.mu.Unlock()
	if ad == nil {
		return ErrNoAdmin
	}
	if !checkPassword(ad.Hash, current) {
		return ErrWrongCurrent
	}
	if err := validate(ad.Username, next); err != nil {
		return err
	}
	hash := hashPassword(next)
	a.mu.Lock()
	defer a.mu.Unlock()
	// The account may have changed while the passwords were hashed (two-factor turned on, a recovery
	// code used up): the change goes onto it as it is now, unless the password itself changed.
	if a.admin == nil || a.admin.Hash != ad.Hash {
		return ErrWrongCurrent
	}
	updated := *a.admin
	updated.Hash = hash
	if err := a.saveLocked(&updated); err != nil {
		return err
	}
	a.admin = &updated
	a.endOthersLocked(keepToken)
	return nil
}

// Reset deletes the admin account and every session, for a forgotten password. Device cookies are
// let go too, so a new account of the same name does not trust the old one's browsers.
func Reset(store *storage.Store) error {
	if err := store.SetMeta(adminKey, ""); err != nil {
		return err
	}
	if err := store.SetMeta(deviceKey, ""); err != nil {
		return err
	}
	return store.DeleteSessions()
}

// endOthersLocked signs out the admin's other browsers, keeping the one with keepToken. Viewers'
// sessions are theirs, and stay.
func (a *Accounts) endOthersLocked(keepToken string) {
	keep := tokenHash(keepToken)
	a.endSessionsLocked(func(h, user string) bool { return h != keep && user == "" })
}

// ---------- sessions ----------

// NewSession signs a browser in as the admin and returns its cookie value.
func (a *Accounts) NewSession(agent string) (string, error) {
	return a.NewSessionFor(Who{Role: RoleAdmin}, agent)
}

// NewSessionFor signs a browser in as who and returns its cookie value.
func (a *Accounts) NewSessionFor(who Who, agent string) (string, error) {
	user := ""
	if !who.Admin() {
		user = who.Username
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	now := float64(a.now().Unix())
	s := &storage.Session{Hash: tokenHash(token), Created: now, Seen: now, Agent: truncate(agent, 200), User: user}
	if err := a.store.SaveSession(*s); err != nil {
		return "", fmt.Errorf("could not sign in: %w", err)
	}
	a.mu.Lock()
	a.sessions[s.Hash] = s
	a.pruneLocked()
	a.mu.Unlock()
	return token, nil
}

// expired reports whether a session has been unused for longer than SessionLife, or is older than
// SessionMaxAge.
func expired(s *storage.Session, now float64) bool {
	return now-s.Seen > SessionLife.Seconds() || (s.Created > 0 && now-s.Created > SessionMaxAge.Seconds())
}

// pruneLocked forgets the sessions that have expired. Valid forgets one when its cookie comes back;
// this catches the browsers that never come back.
func (a *Accounts) pruneLocked() {
	now := float64(a.now().Unix())
	var gone []string
	for h, s := range a.sessions {
		if expired(s, now) {
			gone = append(gone, h)
			delete(a.sessions, h)
		}
	}
	if len(gone) > 0 {
		a.store.DeleteSessions(gone...)
	}
}

// Valid reports whether a cookie value is a live session, the admin's or a viewer's, and keeps it alive.
func (a *Accounts) Valid(token string) bool {
	_, ok := a.SessionWho(token)
	return ok
}

// valid is the user of a live session ("" for the admin), keeping it alive.
func (a *Accounts) valid(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	h := tokenHash(token)
	now := float64(a.now().Unix())
	a.mu.Lock()
	s, ok := a.sessions[h]
	if !ok || a.admin == nil {
		a.mu.Unlock()
		return "", false
	}
	if expired(s, now) {
		delete(a.sessions, h)
		a.mu.Unlock()
		a.store.DeleteSessions(h)
		return "", false
	}
	// Written back at most hourly, not on every request. The write is made holding the lock, so a
	// sign-out or password change cannot delete the session in between and have it saved back.
	if now-s.Seen > 3600 {
		s.Seen = now
		a.store.SaveSession(*s)
	}
	user := s.User
	a.mu.Unlock()
	return user, true
}

// EndSession signs a browser out.
func (a *Accounts) EndSession(token string) {
	if token == "" {
		return
	}
	h := tokenHash(token)
	a.mu.Lock()
	delete(a.sessions, h)
	a.mu.Unlock()
	a.store.DeleteSessions(h)
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------- passwords ----------

func hashPassword(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, _ := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func checkPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	n, err1 := strconv.Atoi(parts[1])
	salt, err2 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err3 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || n < 1 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, n, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}
