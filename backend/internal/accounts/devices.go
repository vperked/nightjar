package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// A device cookie marks a browser that has signed in to an account before. Sign-in limits that hold
// an account back after many wrong tries (which anyone can run up, from any number of addresses)
// then hold back strangers, not the owner's own browser: it is checked against limits of its own.
// It is not a credential: it never signs anyone in, it only says which limits a try counts against.

// deviceKey is the meta key holding the secret that signs device cookies.
const deviceKey = "auth.device_key"

// NewDevice is a device cookie for a browser that just signed in as username.
func (a *Accounts) NewDevice(username string) string {
	b := make([]byte, 16)
	rand.Read(b)
	nonce := base64.RawURLEncoding.EncodeToString(b)
	return nonce + "." + a.deviceMAC(nonce, username)
}

// Device reports whether token is a device cookie given to a browser signed in as username, and an
// id for that browser.
func (a *Accounts) Device(token, username string) (id string, ok bool) {
	nonce, mac, found := strings.Cut(token, ".")
	if !found || nonce == "" || len(nonce) > 64 {
		return "", false
	}
	if !hmac.Equal([]byte(mac), []byte(a.deviceMAC(nonce, username))) {
		return "", false
	}
	return nonce, true
}

func (a *Accounts) deviceMAC(nonce, username string) string {
	m := hmac.New(sha256.New, a.deviceSecret())
	m.Write([]byte(nonce + "\x00" + strings.ToLower(strings.TrimSpace(username))))
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
