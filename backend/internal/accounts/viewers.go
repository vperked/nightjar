package accounts

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// standIn is a hash no password matches, checked when no viewer has the name signed in with.
var standIn = sync.OnceValue(func() string { return hashPassword("\x00stand-in") })

// Viewers are read-only accounts the admin makes: they sign in like the admin and see everything a
// signed-in browser sees, except what holds secrets, but cannot change anything. They have no
// two-factor sign-in of their own; the admin removes one to lock it out.

const viewersKey = "auth.viewers"

// MaxViewers is how many viewer accounts there can be.
const MaxViewers = 50

var (
	ErrViewerExists = errors.New("there is already an account with that name")
	ErrNoViewer     = errors.New("no viewer with that name")
)

// Role is what a signed-in account may do.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// Who is a signed-in account.
type Who struct {
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

// Admin reports whether the account may change things.
func (w Who) Admin() bool { return w.Role == RoleAdmin }

type viewer struct {
	Username string  `json:"username"`
	Hash     string  `json:"hash"`
	Created  float64 `json:"created"`
}

// Viewer is a viewer account as the admin sees it.
type Viewer struct {
	Username string  `json:"username"`
	Created  float64 `json:"created"`
	// Sessions is how many browsers it is signed in on.
	Sessions int `json:"sessions"`
}

func (a *Accounts) loadViewers() {
	raw := a.store.Meta(viewersKey)
	if raw == "" {
		return
	}
	var vs []viewer
	if err := json.Unmarshal([]byte(raw), &vs); err != nil {
		log.Warn("Viewer accounts unreadable", "err", err)
		return
	}
	a.viewers = vs
}

func (a *Accounts) saveViewersLocked(vs []viewer) error {
	if vs == nil {
		vs = []viewer{}
	}
	raw, _ := json.Marshal(vs)
	if err := a.store.SetMeta(viewersKey, string(raw)); err != nil {
		return fmt.Errorf("could not save the viewers: %w", err)
	}
	a.viewers = vs
	return nil
}

// Viewers lists the viewer accounts, oldest first.
func (a *Accounts) Viewers() []Viewer {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Viewer, 0, len(a.viewers))
	for _, v := range a.viewers {
		n := 0
		for _, s := range a.sessions {
			if strings.EqualFold(s.User, v.Username) {
				n++
			}
		}
		out = append(out, Viewer{Username: v.Username, Created: v.Created, Sessions: n})
	}
	return out
}

// AddViewer makes a viewer account. Its name may not be the admin's or another viewer's.
func (a *Accounts) AddViewer(username, password string) error {
	username = strings.TrimSpace(username)
	if err := validate(username, password); err != nil {
		return err
	}
	hash := hashPassword(password)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin == nil {
		return ErrNoAdmin
	}
	if strings.EqualFold(username, a.admin.Username) || a.viewerIndexLocked(username) >= 0 {
		return ErrViewerExists
	}
	if len(a.viewers) >= MaxViewers {
		return fmt.Errorf("there can be at most %d viewers", MaxViewers)
	}
	vs := append(slices.Clone(a.viewers), viewer{Username: username, Hash: hash, Created: float64(a.now().Unix())})
	if err := a.saveViewersLocked(vs); err != nil {
		return err
	}
	log.Info("Viewer account added", "username", username)
	return nil
}

// ChangeViewerPassword is a viewer changing its own password: it needs the current one, and signs out
// the viewer's other browsers, keeping the one with keepToken.
func (a *Accounts) ChangeViewerPassword(username, current, next, keepToken string) error {
	a.mu.Lock()
	i := a.viewerIndexLocked(username)
	var v viewer
	if i >= 0 {
		v = a.viewers[i]
	}
	a.mu.Unlock()
	if i < 0 {
		return ErrNoViewer
	}
	if !checkPassword(v.Hash, current) {
		return ErrWrongCurrent
	}
	if err := validate(v.Username, next); err != nil {
		return err
	}
	hash := hashPassword(next)
	a.mu.Lock()
	defer a.mu.Unlock()
	// Removed, or its password changed, while the passwords were hashed: nothing to change.
	if i = a.viewerIndexLocked(username); i < 0 || a.viewers[i].Hash != v.Hash {
		return ErrWrongCurrent
	}
	vs := slices.Clone(a.viewers)
	vs[i].Hash = hash
	if err := a.saveViewersLocked(vs); err != nil {
		return err
	}
	keep := tokenHash(keepToken)
	a.endSessionsLocked(func(h, user string) bool { return h != keep && strings.EqualFold(user, v.Username) })
	return nil
}

// SetViewerPassword gives a viewer a new password and signs it out everywhere.
func (a *Accounts) SetViewerPassword(username, password string) error {
	if err := validate(strings.TrimSpace(username), password); err != nil {
		return err
	}
	hash := hashPassword(password)
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.viewerIndexLocked(username)
	if i < 0 {
		return ErrNoViewer
	}
	vs := slices.Clone(a.viewers)
	vs[i].Hash = hash
	if err := a.saveViewersLocked(vs); err != nil {
		return err
	}
	a.endSessionsLocked(func(_, user string) bool { return strings.EqualFold(user, vs[i].Username) })
	return nil
}

// RemoveViewer deletes a viewer account and signs it out everywhere.
func (a *Accounts) RemoveViewer(username string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.viewerIndexLocked(username)
	if i < 0 {
		return ErrNoViewer
	}
	name := a.viewers[i].Username
	if err := a.saveViewersLocked(slices.Delete(slices.Clone(a.viewers), i, i+1)); err != nil {
		return err
	}
	a.endSessionsLocked(func(_, user string) bool { return strings.EqualFold(user, name) })
	log.Info("Viewer account removed", "username", name)
	return nil
}

func (a *Accounts) viewerIndexLocked(username string) int {
	username = strings.TrimSpace(username)
	return slices.IndexFunc(a.viewers, func(v viewer) bool { return strings.EqualFold(v.Username, username) })
}

// endSessionsLocked signs out every session whose user matches.
func (a *Accounts) endSessionsLocked(match func(hash, user string) bool) {
	var gone []string
	for h, s := range a.sessions {
		if match(h, s.User) {
			gone = append(gone, h)
			delete(a.sessions, h)
		}
	}
	if len(gone) > 0 {
		a.store.DeleteSessions(gone...)
	}
}

// Login checks a username and password against the admin and every viewer, and says who it is.
// Every account's password is checked whatever the name, so timing reveals neither which names
// exist nor whether one is a viewer's.
func (a *Accounts) Login(username, password string) (Who, error) {
	a.mu.Lock()
	ad := a.admin
	vs := slices.Clone(a.viewers)
	a.mu.Unlock()
	if ad == nil {
		return Who{}, ErrNoAdmin
	}
	name := strings.ToLower(strings.TrimSpace(username))
	var who Who
	found := false
	try := func(account, hash string, role Role) {
		okPass := checkPassword(hash, password)
		okName := subtle.ConstantTimeCompare([]byte(name), []byte(strings.ToLower(account))) == 1
		if okPass && okName && !found {
			who, found = Who{Username: account, Role: role}, true
		}
	}
	try(ad.Username, ad.Hash, RoleAdmin)
	// One viewer hash is checked whatever the name: the matching viewer's, or a stand-in's.
	if i := slices.IndexFunc(vs, func(v viewer) bool { return strings.EqualFold(v.Username, name) }); i >= 0 {
		try(vs[i].Username, vs[i].Hash, RoleViewer)
	} else {
		checkPassword(standIn(), password)
	}
	if !found {
		return Who{}, ErrCredentials
	}
	return who, nil
}

// SessionWho is who a cookie value is signed in as, keeping the session alive; ok is false for no
// live session, or one whose viewer has been removed.
func (a *Accounts) SessionWho(token string) (who Who, ok bool) {
	user, ok := a.valid(token)
	if !ok {
		return Who{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if user == "" {
		if a.admin == nil {
			return Who{}, false
		}
		return Who{Username: a.admin.Username, Role: RoleAdmin}, true
	}
	i := a.viewerIndexLocked(user)
	if i < 0 {
		return Who{}, false
	}
	return Who{Username: a.viewers[i].Username, Role: RoleViewer}, true
}
