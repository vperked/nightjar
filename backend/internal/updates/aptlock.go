package updates

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// apt refuses to run while another apt or dpkg holds its lock, and says so in one of these ways:
//
//	E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1041484 (apt-get)
//	E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?
var (
	aptLockPathRE   = regexp.MustCompile(`(?:Could not get lock|Unable to acquire the dpkg frontend lock) \(?(/[^\s(),]+)`)
	aptLockHolderRE = regexp.MustCompile(`held by process (\d+)(?: \(([^)]*)\))?`)
	dpkgInterrupted = regexp.MustCompile(`dpkg was interrupted, you must manually run`)
)

// How long Nightjar waits, in polls, for another program to let go of apt's lock: 10 minutes at
// the usual 2s. Whatever holds it is never stopped. It may be unattended-upgrades half-way through
// a kernel, an admin at apt's [Y/n], or an install of the dashboard's own app store, and stopping
// any of them leaves dpkg half-done with the lock still held.
const aptWaitPolls = 300

type aptLock struct {
	path   string
	pid    int
	holder string // the holder's program name, when apt says
}

func findAptLock(out Output) (aptLock, bool) {
	text := out.Stderr + "\n" + out.Stdout
	m := aptLockPathRE.FindStringSubmatch(text)
	if m == nil {
		return aptLock{}, false
	}
	l := aptLock{path: strings.TrimRight(m[1], ".")}
	if h := aptLockHolderRE.FindStringSubmatch(text); h != nil {
		l.pid, _ = strconv.Atoi(h[1])
		l.holder = h[2]
	}
	return l, true
}

func (l aptLock) who() string {
	if l.holder != "" {
		return fmt.Sprintf("%s (process %d)", l.holder, l.pid)
	}
	if l.pid > 0 {
		return fmt.Sprintf("process %d", l.pid)
	}
	return "another program"
}

func isAptCommand(argv []string) bool {
	return len(argv) > 0 && (argv[0] == "apt-get" || argv[0] == "apt")
}

// recoverApt waits out or clears what stopped an apt command, so it can be run again: another
// program holding apt's lock, or an install interrupted earlier. ok says whether it is worth
// running again.
func (m *Manager) recoverApt(ctx context.Context, out Output) (ok bool, err error) {
	if l, found := findAptLock(out); found {
		return true, m.waitAptLock(ctx, l)
	}
	if dpkgInterrupted.MatchString(out.Stderr + out.Stdout) {
		noteStep(ctx, "Finishing an install that was interrupted earlier")
		return true, m.dpkgConfigure(ctx)
	}
	return false, nil
}

// waitAptLock waits for the program holding apt's lock to finish on its own. It is never
// signalled: Nightjar runs one command at a time, so whatever holds the lock is another program's,
// or Nightjar's own left running by a restart, and either is better finished than cut off.
func (m *Manager) waitAptLock(ctx context.Context, l aptLock) error {
	if l.pid == 0 {
		l.pid = m.lockHolder(ctx, l.path)
	}
	if l.pid <= 1 {
		return fmt.Errorf("%s is locked and Nightjar could not tell which program holds it", l.path)
	}
	noteStep(ctx, fmt.Sprintf("Waiting up to %s for %s to finish with the package manager", aptWaitPolls*m.opt.Poll, l.who()))
	if !m.waitExit(ctx, l.pid, aptWaitPolls) {
		return fmt.Errorf("the package manager is busy: %s still holds %s", l.who(), l.path)
	}
	return nil
}

func (m *Manager) dpkgConfigure(ctx context.Context) error {
	_, err := m.run(ctx, Command{Argv: []string{"dpkg", "--configure", "-a"}, Env: aptEnv, Root: true, Timeout: 10 * time.Minute})
	return err
}

// lockHolder asks fuser which process has path open; 0 when it cannot tell.
func (m *Manager) lockHolder(ctx context.Context, path string) int {
	out, err := m.run(ctx, Command{Argv: []string{"fuser", path}, Root: true}, 1)
	if err != nil {
		return 0
	}
	for _, f := range strings.Fields(out.Stdout) {
		if pid, err := strconv.Atoi(strings.TrimRight(f, "cefFrm")); err == nil && pid > 1 {
			return pid
		}
	}
	return 0
}

// waitExit reports whether pid is gone within the given number of polls.
func (m *Manager) waitExit(ctx context.Context, pid, polls int) bool {
	for i := 0; ; i++ {
		out, err := m.opt.Run(ctx, Command{Argv: []string{"kill", "-0", strconv.Itoa(pid)}, Root: true, Timeout: 10 * time.Second})
		if err == nil && out.Code != 0 {
			return true
		}
		if i == polls || !sleep(ctx, m.opt.Poll) {
			return false
		}
	}
}
