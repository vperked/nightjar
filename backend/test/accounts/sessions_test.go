package accounts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/storage"
)

// Turning two-factor sign-in on or off signs out the admin's other browsers, as a password change
// does; the browser that did it, and viewers, stay signed in.
func TestTwoFactorChangesSignOutOtherBrowsers(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	a := accounts.New(db)
	if err := a.Create("jay", "correct horse battery", a.SetupCode()); err != nil {
		t.Fatal(err)
	}
	if err := a.AddViewer("sam", "sam's long password"); err != nil {
		t.Fatal(err)
	}
	session := func(who accounts.Who) string {
		t.Helper()
		tok, err := a.NewSessionFor(who, "test")
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	admin := accounts.Who{Role: accounts.RoleAdmin}
	here, other, viewer := session(admin), session(admin), session(accounts.Who{Username: "sam", Role: accounts.RoleViewer})

	secret, _, err := a.BeginTwoFactor()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := a.EnableTwoFactor("correct horse battery", accounts.TOTP(secret, now), here); err != nil {
		t.Fatal(err)
	}
	if !a.Valid(here) || a.Valid(other) || !a.Valid(viewer) {
		t.Fatalf("after turning it on: here %v, other %v, viewer %v", a.Valid(here), a.Valid(other), a.Valid(viewer))
	}

	other = session(admin)
	// The next time step's code: the one that turned it on cannot be used again.
	if err := a.DisableTwoFactor("correct horse battery", accounts.TOTP(secret, now+30), here); err != nil {
		t.Fatal(err)
	}
	if !a.Valid(here) || a.Valid(other) || !a.Valid(viewer) {
		t.Fatalf("after turning it off: here %v, other %v, viewer %v", a.Valid(here), a.Valid(other), a.Valid(viewer))
	}
}

// However often it is used, a session ends SessionMaxAge after it began.
func TestSessionsEndAfterTheirMaxAge(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	hash := func(tok string) string {
		sum := sha256.Sum256([]byte(tok))
		return hex.EncodeToString(sum[:])
	}
	a := accounts.New(db)
	if err := a.Create("jay", "correct horse battery", a.SetupCode()); err != nil {
		t.Fatal(err)
	}
	now := float64(time.Now().Unix())
	old := float64(time.Now().Add(-accounts.SessionMaxAge - time.Hour).Unix())
	young := float64(time.Now().Add(-accounts.SessionMaxAge + 24*time.Hour).Unix())
	db.SaveSession(storage.Session{Hash: hash("too-old"), Created: old, Seen: now})
	db.SaveSession(storage.Session{Hash: hash("old-enough"), Created: young, Seen: now})

	a = accounts.New(db)
	if a.Valid("too-old") {
		t.Fatal("a session past its max age, though in use, is still good")
	}
	if !a.Valid("old-enough") {
		t.Fatal("a session within its max age was ended")
	}
	if accounts.SessionMaxAge < accounts.SessionLife {
		t.Fatal("the max age is shorter than the time a session lasts unused")
	}
}

// A device cookie is good only for the account it was given for, survives a restart, and is let go
// when the account is reset.
func TestDeviceCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	db := storage.Open(path)
	t.Cleanup(db.Close)
	a := accounts.New(db)
	tok := a.NewDevice("Jay")
	id, ok := a.Device(tok, " jay ")
	if !ok || id == "" {
		t.Fatal("a device cookie is not good for its own account")
	}
	if _, ok := a.Device(tok, "sam"); ok {
		t.Fatal("a device cookie is good for another account")
	}
	for _, bad := range []string{"", "x", tok + "x", "." + tok, id + ".AAAA"} {
		if _, ok := a.Device(bad, "jay"); ok {
			t.Fatalf("%q was taken as a device cookie", bad)
		}
	}
	if again, ok := accounts.New(db).Device(tok, "jay"); !ok || again != id {
		t.Fatal("a device cookie did not survive a restart")
	}
	if err := accounts.Reset(db); err != nil {
		t.Fatal(err)
	}
	if _, ok := accounts.New(db).Device(tok, "jay"); ok {
		t.Fatal("a device cookie outlived a reset of the account")
	}
}
