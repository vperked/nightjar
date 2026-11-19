package updates_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/updates"
)

// aptHost answers like a Debian machine with nginx 1.0 installed and 1.1 on offer, whose first
// `apt-get update` finds the lock held by another program (unless lockOut is empty), which lets go
// after polls looks at it (never when polls is 0), and which runs `failWith` for every later
// apt-get update.
type aptHost struct {
	mu       sync.Mutex
	lockOut  string
	failWith string
	alive    bool // the lock holder is still running
	polls    int  // looks at the holder before it finishes on its own; 0 is never
	ran      []string
}

func (h *aptHost) run(_ context.Context, c updates.Command) (updates.Output, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := strings.Join(c.Argv, " ")
	h.ran = append(h.ran, line)
	switch {
	case line == "apt-get update -qq":
		if h.lockOut != "" && (h.alive || len(h.ran) == 1) {
			return updates.Output{Code: 100, Stderr: h.lockOut}, nil
		}
		if h.failWith != "" {
			return updates.Output{Code: 100, Stderr: h.failWith}, nil
		}
		return updates.Output{}, nil
	case strings.HasPrefix(line, "kill -0 "):
		if h.alive && h.polls > 0 {
			if h.polls--; h.polls == 0 {
				h.alive = false
			}
		}
		if h.alive {
			return updates.Output{}, nil
		}
		return updates.Output{Code: 1}, nil
	case strings.HasPrefix(line, "dpkg-query"):
		return updates.Output{Stdout: "1.0"}, nil
	case strings.HasPrefix(line, "apt-cache policy"):
		return updates.Output{Stdout: "nginx:\n  Installed: 1.0\n  Candidate: 1.1\n"}, nil
	case strings.HasPrefix(line, "dpkg --compare-versions"):
		return updates.Output{}, nil
	}
	return updates.Output{Code: 127, Stderr: "unexpected command: " + line}, nil
}

func (h *aptHost) ranLine(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.ran {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// signalled says whether anything was sent a signal that could stop it (kill -0 only asks).
func (h *aptHost) signalled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.ran {
		if strings.HasPrefix(l, "kill ") && !strings.HasPrefix(l, "kill -0 ") {
			return true
		}
	}
	return false
}

var nginx = updates.Spec{ID: "nginx", Name: "nginx", Kind: updates.KindApt, Package: "nginx"}

const (
	listsLocked = "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1041484 (apt-get)\nE: Unable to lock directory /var/lib/apt/lists/"
	dpkgLocked  = "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 2200 (unattended-upgr)\nE: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?"
)

func checkNginx(t *testing.T, h *aptHost) updates.Status {
	t.Helper()
	m := newManager(t, openStore(t), h.run, "", nil)
	if _, err := m.Save("", nginx); err != nil {
		t.Fatal(err)
	}
	return settle(t, m, "nginx", func(s updates.Status) bool { return s.Checked > 0 })
}

// Whatever holds apt's lock is waited for, never stopped: unattended-upgrades in the middle of a
// kernel, or an admin at apt's prompt, stopped leaves dpkg half-done with the lock still held.
func TestAptLockHolderIsWaitedForAndTheCheckRetried(t *testing.T) {
	for name, out := range map[string]string{"package lists": listsLocked, "dpkg": dpkgLocked} {
		h := &aptHost{lockOut: out, alive: true, polls: 20}
		st := checkNginx(t, h)
		if st.Error != "" || !st.Available || st.Latest != "1.1" {
			t.Fatalf("%s: the check goes through once the lock is free: %+v", name, st.State)
		}
		if h.signalled() {
			t.Fatalf("%s: the program holding the lock is never signalled: %v", name, h.ran)
		}
		if h.ranLine("dpkg --configure") {
			t.Fatalf("%s: a holder that finished on its own leaves nothing to repair", name)
		}
	}
}

func TestAptLockThatWillNotGoSaysSoPlainly(t *testing.T) {
	h := &aptHost{lockOut: dpkgLocked, alive: true}
	st := checkNginx(t, h)
	if h.signalled() {
		t.Fatalf("a holder that takes too long is still not signalled: %v", h.ran[len(h.ran)-3:])
	}
	want := "apt-get update failed: unattended-upgr (process 2200) was using the package manager"
	if !strings.HasPrefix(st.Error, want) || strings.Contains(st.Error, "exit 100") || strings.Contains(st.Error, "E: ") {
		t.Fatalf("error = %q, want it to start %q without apt's raw output", st.Error, want)
	}
}

func TestKnownAptFailuresAreExplained(t *testing.T) {
	for out, want := range map[string]string{
		"W: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/noble/InRelease  Temporary failure resolving 'archive.ubuntu.com'": "addresses could not be looked up",
		"E: The repository 'http://ppa.launchpad.net/x/y/ubuntu jammy Release' does not have a Release file.":                         "no longer at its address",
		"W: GPG error: https://download.docker.com noble InRelease: NO_PUBKEY 7EA0A9C3F273FCD8":                                       "signing key is missing",
		"E: Write error - write (28: No space left on device)":                                                                        "the disk is full",
	} {
		st := checkNginx(t, &aptHost{failWith: out})
		if !strings.Contains(st.Error, want) || strings.Contains(st.Error, "exit 100") {
			t.Errorf("for %q the error is %q, want it to say %q", out, st.Error, want)
		}
	}
}

func TestUnknownFailuresKeepTheirOutput(t *testing.T) {
	st := checkNginx(t, &aptHost{failWith: "E: something new and strange"})
	if !strings.Contains(st.Error, "something new and strange") {
		t.Fatalf("an error Nightjar does not know is shown as printed: %q", st.Error)
	}
}

// A target's own command that fails is shown as it failed: a curl that got a 404 did not find a
// package source gone, and a git remote that would not resolve is not the package servers.
func TestCustomCommandsKeepTheirOwnErrors(t *testing.T) {
	run := func(_ context.Context, c updates.Command) (updates.Output, error) {
		return updates.Output{Code: 22, Stderr: "curl: (22) The requested URL returned error: 404 Not Found"}, nil
	}
	m := newManager(t, openStore(t), run, "", nil)
	if _, err := m.Save("", app); err != nil {
		t.Fatal(err)
	}
	st := settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	if !strings.Contains(st.Error, "returned error: 404") || strings.Contains(st.Error, "package source") {
		t.Fatalf("error = %q, want curl's own words", st.Error)
	}
	// A full disk is a full disk, whoever says it.
	full := func(_ context.Context, c updates.Command) (updates.Output, error) {
		return updates.Output{Code: 1, Stderr: "cp: error writing '/opt/app': No space left on device"}, nil
	}
	m = newManager(t, openStore(t), full, "", nil)
	m.Save("", app)
	if st := settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 }); !strings.Contains(st.Error, "the disk is full") {
		t.Fatalf("error = %q", st.Error)
	}
}
