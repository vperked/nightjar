package selfcheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/selfcheck"
)

func TestOnlyWhatOthersCouldChangeIsReported(t *testing.T) {
	base := t.TempDir()
	os.Chmod(base, 0o700)
	safe := filepath.Join(base, "safe")
	os.MkdirAll(filepath.Join(safe, "bin"), 0o755)
	os.WriteFile(filepath.Join(safe, "bin", "nightjar"), []byte("x"), 0o755)

	// A program in a folder anyone may write to can be swapped out.
	open := filepath.Join(base, "open")
	os.MkdirAll(open, 0o755)
	os.Chmod(open, 0o777)
	os.WriteFile(filepath.Join(open, "nightjar"), []byte("x"), 0o755)

	// A settings file anyone may write to.
	os.WriteFile(filepath.Join(safe, ".env"), []byte("x"), 0o600)
	os.Chmod(filepath.Join(safe, ".env"), 0o666)

	// A sticky folder like /tmp is fine for a file root owns.
	sticky := filepath.Join(base, "tmp")
	os.MkdirAll(sticky, 0o755)
	os.Chmod(sticky, 0o1777)
	os.WriteFile(filepath.Join(sticky, "web"), []byte("x"), 0o644)

	// A link that looks safe but points into an open folder.
	os.Symlink(filepath.Join(open, "nightjar"), filepath.Join(safe, "current"))

	got := selfcheck.Exposed([]string{filepath.Join(safe, "bin", "nightjar"), filepath.Join(safe, ".env"), filepath.Join(sticky, "web"), filepath.Join(safe, "current"), filepath.Join(base, "missing")})
	joined := strings.Join(got, "\n")
	for _, want := range []string{open + " can be changed by other accounts", filepath.Join(safe, ".env") + " can be changed by other accounts"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	for _, fine := range []string{filepath.Join(safe, "bin"), sticky + " ", filepath.Join(sticky, "web")} {
		if strings.Contains(joined+" ", fine+" can") || strings.Contains(joined, fine+" belongs") {
			t.Errorf("%s reported:\n%s", fine, joined)
		}
	}

	if os.Geteuid() == 0 {
		// Owned by someone else, even if only they may write it.
		theirs := filepath.Join(safe, "plugins")
		os.MkdirAll(theirs, 0o755)
		os.Chown(theirs, 65534, 65534)
		if got := strings.Join(selfcheck.Exposed([]string{theirs}), "\n"); !strings.Contains(got, theirs+" belongs to user") {
			t.Fatalf("another account's folder: %s", got)
		}
	}
}

func TestCapabilitiesAreReadAndNamed(t *testing.T) {
	// Nightjar's own set the day it lost cap_setuid: everything but cap_setuid and cap_sys_module.
	c, ok := selfcheck.Caps("Name:\tnightjar\nCapPrm:\t000001fffffeff7f\nCapEff:\t000001fffffeff7f\n")
	if !ok || c != 0x1fffffeff7f {
		t.Fatalf("caps: %x %v", c, ok)
	}
	full, _ := selfcheck.Caps("CapEff:\t000001ffffffffff\n")
	if got := strings.Join(selfcheck.CapNames(full&^c), ","); got != "cap_setuid,cap_sys_module" {
		t.Fatalf("missing: %s", got)
	}
	if _, ok := selfcheck.Caps("Name:\tx\n"); ok {
		t.Fatal("no CapEff line")
	}
	// The tests themselves run as root with everything, or as a user without the right to switch.
	if got := selfcheck.HasCap(selfcheck.CapSetuid); got != (os.Geteuid() == 0) {
		t.Fatalf("HasCap(cap_setuid) = %v as uid %d", got, os.Geteuid())
	}
}
