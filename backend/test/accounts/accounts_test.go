package accounts_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/storage"
)

func TestAdminAccountLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.db")
	db := storage.Open(path)
	a := accounts.New(db)
	code := a.SetupCode()
	if a.Configured() || len(code) != 11 {
		t.Fatalf("a fresh install has no admin and a setup code, got %v %q", a.Configured(), code)
	}

	if err := a.Create("jay", "correct horse battery", "WRONG-CODE"); !errors.Is(err, accounts.ErrSetupCode) {
		t.Fatalf("a wrong setup code should be refused, got %v", err)
	}
	for _, bad := range [][2]string{{"jay", "short"}, {"has space", "correct horse battery"}, {"administrator", "ADMINISTRATOR"}} {
		if a.Create(bad[0], bad[1], code) == nil {
			t.Fatalf("%q / %q should be refused", bad[0], bad[1])
		}
	}
	if err := a.Create("jay", "correct horse battery", " "+strings.ToLower(code)+" "); err != nil {
		t.Fatalf("the code is accepted in any case with spaces around it: %v", err)
	}
	if a.SetupCode() != "" || a.Create("eve", "another long password", code) == nil {
		t.Fatal("the setup code works once")
	}

	if a.Verify("jay", "wrong password!") == nil || a.Verify("eve", "correct horse battery") == nil {
		t.Fatal("wrong credentials should be refused")
	}
	if err := a.Verify("Jay", "correct horse battery"); err != nil {
		t.Fatalf("usernames are not case sensitive: %v", err)
	}

	token, err := a.NewSession("test")
	if err != nil || !a.Valid(token) || a.Valid(token+"x") || a.Valid("") {
		t.Fatalf("sessions: %v", err)
	}
	other, _ := a.NewSession("phone")

	// Sessions and the account survive a restart; the code is not shown again.
	db.Close()
	db = storage.Open(path)
	defer db.Close()
	a = accounts.New(db)
	if !a.Configured() || a.SetupCode() != "" || !a.Valid(token) || a.Username() != "jay" {
		t.Fatal("the account and its sessions should survive a restart")
	}

	if err := a.ChangePassword("nope", "a brand new password", token); !errors.Is(err, accounts.ErrWrongCurrent) {
		t.Fatalf("the current password is checked, got %v", err)
	}
	if err := a.ChangePassword("correct horse battery", "a brand new password", token); err != nil {
		t.Fatal(err)
	}
	if !a.Valid(token) || a.Valid(other) {
		t.Fatal("changing the password keeps this browser and signs out the others")
	}
	if a.Verify("jay", "a brand new password") != nil {
		t.Fatal("the new password should work")
	}
	a.EndSession(token)
	if a.Valid(token) {
		t.Fatal("signing out ends the session")
	}

	if err := accounts.Reset(db); err != nil {
		t.Fatal(err)
	}
	if a = accounts.New(db); a.Configured() || a.SetupCode() == "" {
		t.Fatal("after a reset the account can be created again")
	}
}

func TestTwoFactorTakesThePasswordAndKeepsThroughAPasswordChange(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	a := accounts.New(db)
	if err := a.Create("jay", "correct horse battery", a.SetupCode()); err != nil {
		t.Fatal(err)
	}
	secret, _, err := a.BeginTwoFactor()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := a.EnableTwoFactor("not the password", accounts.TOTP(secret, now), ""); !errors.Is(err, accounts.ErrWrongCurrent) || a.TwoFactor() {
		t.Fatalf("turned on without the password: %v", err)
	}
	if _, err := a.EnableTwoFactor("correct horse battery", accounts.TOTP(secret, now), ""); err != nil || !a.TwoFactor() {
		t.Fatalf("enable: %v", err)
	}
	// Another authenticator only after turning it off, which takes the password and a code.
	if _, _, err := a.BeginTwoFactor(); !errors.Is(err, accounts.ErrTwoFactorOn) {
		t.Fatalf("a new authenticator while one is on: %v", err)
	}
	// A password change keeps the account's two-factor sign-in, and its codes.
	if err := a.ChangePassword("correct horse battery", "a brand new password", ""); err != nil {
		t.Fatal(err)
	}
	if !a.TwoFactor() || a.RecoveryLeft() != 10 {
		t.Fatalf("the password change undid two-factor sign-in: %v %d", a.TwoFactor(), a.RecoveryLeft())
	}
}

func TestSessionsUnusedForTooLongAreForgotten(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	old := float64(time.Now().Add(-accounts.SessionLife - time.Hour).Unix())
	db.SaveSession(storage.Session{Hash: "stale", Created: old, Seen: old})
	db.SaveSession(storage.Session{Hash: "fresh", Created: old, Seen: float64(time.Now().Unix())})
	accounts.New(db)
	var left []string
	for _, s := range db.Sessions() {
		left = append(left, s.Hash)
	}
	if strings.Join(left, ",") != "fresh" {
		t.Fatalf("sessions left: %v", left)
	}
}
