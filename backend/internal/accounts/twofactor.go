package accounts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Two-factor sign-in asks for a code from an authenticator app (TOTP, RFC 6238: six digits, a new
// one every 30 seconds) after the password. Ten one-time recovery codes stand in for a lost phone.

var (
	ErrCode        = errors.New("that code is not right; use the current one from your authenticator app")
	ErrNoPending   = errors.New("start setting up two-factor sign-in first")
	ErrTwoFactorOn = errors.New("two-factor sign-in is already on; turn it off first to use another authenticator")
)

const (
	totpStep      = 30
	recoveryCodes = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// totp is the code for a time step.
func totp(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000)
}

// TOTP is the code an authenticator shows for secret at unix time t; tests use it.
func TOTP(secret string, t int64) string {
	key, _ := b32.DecodeString(strings.ToUpper(secret))
	return totp(key, t/totpStep)
}

// matchStep is the time step (now, or one either side for a slow clock) a code is for, or -1.
func matchStep(secret, code string, now int64) int64 {
	key, err := b32.DecodeString(secret)
	if err != nil || len(code) != 6 {
		return -1
	}
	for _, d := range []int64{0, -1, 1} {
		step := now/totpStep + d
		if subtle.ConstantTimeCompare([]byte(totp(key, step)), []byte(code)) == 1 {
			return step
		}
	}
	return -1
}

func codeHash(code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.ReplaceAll(code, "-", ""))))
	return hex.EncodeToString(sum[:])
}

// TwoFactor reports whether sign-in asks for a code.
func (a *Accounts) TwoFactor() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admin != nil && a.admin.TOTP != ""
}

// RecoveryLeft is how many recovery codes are unused.
func (a *Accounts) RecoveryLeft() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin == nil {
		return 0
	}
	return len(a.admin.Recovery)
}

// BeginTwoFactor makes a new secret to add to an authenticator: as text, and as the otpauth://
// link authenticator apps open or read from a QR code. While two-factor sign-in is on, the
// authenticator can only be replaced by turning it off first, which takes the password and a code.
func (a *Accounts) BeginTwoFactor() (secret, link string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin == nil {
		return "", "", ErrNoAdmin
	}
	if a.admin.TOTP != "" {
		return "", "", ErrTwoFactorOn
	}
	raw := make([]byte, 20)
	rand.Read(raw)
	a.pending = b32.EncodeToString(raw)
	label := url.PathEscape("Nightjar:" + a.admin.Username)
	link = "otpauth://totp/" + label + "?" + url.Values{"secret": {a.pending}, "issuer": {"Nightjar"}}.Encode()
	return a.pending, link, nil
}

// EnableTwoFactor turns it on with the password, so a session left open cannot tie the account to
// someone else's authenticator, once a code from the new secret proves the authenticator has it. It
// returns the recovery codes, shown this once.
func (a *Accounts) EnableTwoFactor(password, code string) ([]string, error) {
	a.mu.Lock()
	ad := a.admin
	a.mu.Unlock()
	if ad == nil {
		return nil, ErrNoAdmin
	}
	if !checkPassword(ad.Hash, password) {
		return nil, ErrWrongCurrent
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.admin == nil:
		return nil, ErrNoAdmin
	case a.admin.Hash != ad.Hash: // changed while it was checked
		return nil, ErrWrongCurrent
	case a.admin.TOTP != "":
		return nil, ErrTwoFactorOn
	}
	if a.pending == "" {
		return nil, ErrNoPending
	}
	step := matchStep(a.pending, strings.TrimSpace(code), a.now().Unix())
	if step < 0 {
		return nil, ErrCode
	}
	codes := make([]string, recoveryCodes)
	hashes := make([]string, recoveryCodes)
	for i := range codes {
		b := make([]byte, 5)
		rand.Read(b)
		c := strings.ToLower(hex.EncodeToString(b))
		codes[i] = c[:5] + "-" + c[5:]
		hashes[i] = codeHash(codes[i])
	}
	next := *a.admin
	next.TOTP, next.Recovery, next.LastStep = a.pending, hashes, step
	if err := a.saveLocked(&next); err != nil {
		return nil, err
	}
	a.admin, a.pending = &next, ""
	log.Info("Two-factor sign-in turned on")
	return codes, nil
}

// CheckCode accepts the current authenticator code (once) or an unused recovery code (used up).
func (a *Accounts) CheckCode(code string) error {
	code = strings.TrimSpace(code)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.admin == nil || a.admin.TOTP == "" {
		return nil
	}
	next := *a.admin
	if step := matchStep(next.TOTP, code, a.now().Unix()); step >= 0 {
		if step <= next.LastStep {
			return ErrCode
		}
		next.LastStep = step
	} else if i := slices.Index(next.Recovery, codeHash(code)); i >= 0 && len(code) >= 10 {
		next.Recovery = slices.Delete(slices.Clone(next.Recovery), i, i+1)
		log.Warn("A recovery code was used to sign in", "left", len(next.Recovery))
	} else {
		return ErrCode
	}
	if err := a.saveLocked(&next); err != nil {
		return err
	}
	a.admin = &next
	return nil
}

// DisableTwoFactor turns it off, with the password and a current code.
func (a *Accounts) DisableTwoFactor(password, code string) error {
	a.mu.Lock()
	ad := a.admin
	a.mu.Unlock()
	if ad == nil {
		return ErrNoAdmin
	}
	if !checkPassword(ad.Hash, password) {
		return ErrWrongCurrent
	}
	if err := a.CheckCode(code); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := *a.admin
	next.TOTP, next.Recovery, next.LastStep = "", nil, 0
	if err := a.saveLocked(&next); err != nil {
		return err
	}
	a.admin = &next
	log.Info("Two-factor sign-in turned off")
	return nil
}
