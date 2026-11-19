package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// A device cookie marks a browser that has signed in to an account before. Sign-in limits that hold
// an account back after many wrong tries (which anyone can run up, from any number of addresses)
// then hold back strangers, not the owner's own browser: it is checked against limits of its own.
// It is not a credential: it never signs anyone in, it only says which limits a try counts against.
//
// It is signed for the account itself, not just its name: its role and when it was made. A viewer
// removed and made again, or an admin made after a reset, is a new account, and the old account's
// browsers are strangers to it; a viewer's browser never counts as the admin's.

// deviceKey is the meta key holding the secret that signs device cookies.
const deviceKey = "auth.device_key"

// NewDevice is a device cookie for a browser that just signed in as who.
func (a *Accounts) NewDevice(who Who) string {
	b := make([]byte, 16)
	rand.Read(b)
	nonce := base64.RawURLEncoding.EncodeToString(b)
	return nonce + "." + a.deviceMAC(nonce, a.accountTag(who.Role, who.Username))
}

// Device reports whether token is a device cookie given to a browser signed in to the account that
// signing in as username reaches, and an id for that browser. A name the admin has is the admin's.
func (a *Accounts) Device(token, username string) (id string, ok bool) {
	nonce, mac, found := strings.Cut(token, ".")
	if !found || nonce == "" || len(nonce) > 64 {
		return "", false
	}
	role := RoleViewer
	if a.isAdminName(username) {
		role = RoleAdmin
	}
	if !hmac.Equal([]byte(mac), []byte(a.deviceMAC(nonce, a.accountTag(role, username)))) {
		return "", false
	}
	return nonce, true
}

func (a *Accounts) isAdminName(username string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admin != nil && strings.EqualFold(strings.TrimSpace(username), a.admin.Username)
}

// accountTag names the account with role and username for device cookies: the role, when the
// account was made and its name. A name no account of that role has gets a tag of its own.
func (a *Accounts) accountTag(role Role, username string) string {
	name := strings.ToLower(strings.TrimSpace(username))
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case role == RoleAdmin && a.admin != nil && strings.EqualFold(name, a.admin.Username):
		return fmt.Sprintf("admin\x00%.0f\x00%s", a.admin.Created, name)
	case role == RoleViewer:
		if i := a.viewerIndexLocked(name); i >= 0 {
			return fmt.Sprintf("viewer\x00%.0f\x00%s", a.viewers[i].Created, name)
		}
	}
	return "none\x00" + name
}

func (a *Accounts) deviceMAC(nonce, tag string) string {
	m := hmac.New(sha256.New, a.deviceSecret())
	m.Write([]byte(nonce + "\x00" + tag))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// deviceSecret is the key device cookies are signed with, made the first time it is needed and kept,
// so browsers stay known across restarts.
func (a *Accounts) deviceSecret() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.device != nil {
		return a.device
	}
	if raw, err := hex.DecodeString(a.store.Meta(deviceKey)); err == nil && len(raw) == 32 {
		a.device = raw
		return raw
	}
	b := make([]byte, 32)
	rand.Read(b)
	// Not kept, browsers are only known until the next restart: nothing worse.
	if err := a.store.SetMeta(deviceKey, hex.EncodeToString(b)); err != nil {
		log.Warn("Could not keep the device cookie key", "err", err)
	}
	a.device = b
	return b
}
