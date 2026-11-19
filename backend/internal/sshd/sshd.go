// Package sshd manages who may log in to this machine over SSH: the public keys each account lets in
// (~/.ssh/authorized_keys), and whether sshd still takes passwords. Keys only is a file of Nightjar's
// own in sshd_config.d, named to sort first because sshd keeps the first value it reads for each
// setting; sshd is reloaded, not restarted, so nobody logged in is dropped.
//
// Turning passwords off could lock the owner out if the key they added does not work, so like the
// firewall's default drop it goes back by itself unless it is kept within two minutes. The deadline is
// written in the file itself, so a restart in the meantime still puts passwords back.
package sshd

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"nightjar/internal/ddos"
	"nightjar/internal/logx"
)

var log = logx.Component("SSH")

// DropIn is the name of Nightjar's file in sshd_config.d.
const DropIn = "00-nightjar-keys-only.conf"

// pendingMark starts the line in DropIn saying when keys only goes back unless kept.
const pendingMark = "# nightjar-pending-until: "

// Access is who may log in over SSH, as the dashboard shows it.
type Access struct {
	// Ports are the ports sshd listens on.
	Ports []uint16 `json:"ports"`
	// PasswordLogin is sshd taking passwords now, as sshd -T reads its config.
	PasswordLogin bool `json:"password_login"`
	// KeysOnly is Nightjar's file turning passwords off being in place.
	KeysOnly bool `json:"keys_only"`
	// Pending is keys only waiting to be kept; at Until it goes back by itself.
	Pending *Pending `json:"pending,omitempty"`
	Users   []User   `json:"users"`
	// Problem says why logins cannot be managed here.
	Problem string `json:"problem,omitempty"`
	// Now is this machine's clock as it answered, unix seconds: Pending.Until is on it, and a
	// browser on another clock counts down against it.
	Now int64 `json:"now"`
}

// Pending is keys only waiting to be kept.
type Pending struct {
	Until int64 `json:"until"`
}

// Manager manages SSH logins. Its methods are safe for concurrent use.
type Manager struct {
	// Dir is sshd's config folder; empty is /etc/ssh.
	Dir string
	// Passwd is the accounts file; empty is /etc/passwd.
	Passwd string
	// Proc is where /proc is, for the ports sshd listens on; empty is /proc.
	Proc string
	// Exec runs sshd and systemctl; nil runs them. For tests.
	Exec func(ctx context.Context, name string, args ...string) (string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// KeepWithin is how long keys only waits to be kept; zero is two minutes.
	KeepWithin time.Duration
	// CheckEvery is how often Run looks for keys only not kept in time; zero is five seconds.
	CheckEvery time.Duration

	mu sync.Mutex
}

func (m *Manager) dir() string    { return cmp.Or(m.Dir, "/etc/ssh") }
func (m *Manager) passwd() string { return cmp.Or(m.Passwd, "/etc/passwd") }
func (m *Manager) config() string { return filepath.Join(m.dir(), "sshd_config") }
func (m *Manager) dropDir() string {
	return filepath.Join(m.dir(), "sshd_config.d")
}
func (m *Manager) dropIn() string { return filepath.Join(m.dropDir(), DropIn) }

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// run runs a command and gives its output; an error carries the last line it printed.
func (m *Manager) run(ctx context.Context, name string, args ...string) (string, error) {
	if m.Exec != nil {
		return m.Exec(ctx, name, args...)
	}
	// sshd lives in sbin, which a service's PATH may leave out.
	if _, err := exec.LookPath(name); err != nil && name == "sshd" {
		if _, err := os.Stat("/usr/sbin/sshd"); err == nil {
			name = "/usr/sbin/sshd"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			lines := strings.Split(msg, "\n")
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(lines[len(lines)-1]))
		}
	}
	return string(out), err
}

// effective is sshd's config as sshd -T reads it: each setting, lower-case, and its value.
func (m *Manager) effective(ctx context.Context) (map[string]string, error) {
	out, err := m.run(ctx, "sshd", "-T")
	// sshd refuses to check its config without the folder its service makes when it starts; one
	// started on demand by ssh.socket may not have run yet.
	if err != nil && m.Exec == nil {
		if _, dir, ok := strings.Cut(out, "Missing privilege separation directory: "); ok {
			dir = strings.TrimSpace(strings.SplitN(dir, "\n", 2)[0])
			if strings.HasPrefix(dir, "/run/") || strings.HasPrefix(dir, "/var/run/") {
				if os.MkdirAll(dir, 0o755) == nil {
					out, err = m.run(ctx, "sshd", "-T")
				}
			}
		}
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("OpenSSH's server (sshd) is not installed here")
		}
		return nil, fmt.Errorf("sshd could not read its config: %v", err)
	}
	settings := map[string]string{}
	for line := range strings.Lines(out) {
		key, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		if key != "" {
			if _, seen := settings[strings.ToLower(key)]; !seen {
				settings[strings.ToLower(key)] = strings.TrimSpace(value)
			}
		}
	}
	return settings, nil
}

// passwords is sshd taking passwords: by the password method, or by keyboard-interactive (which is
// how PAM asks for one; challengeresponseauthentication on OpenSSH before 8.7).
func passwords(settings map[string]string) bool {
	return settings["passwordauthentication"] == "yes" || settings["kbdinteractiveauthentication"] == "yes" || settings["challengeresponseauthentication"] == "yes"
}

// pendingLocked is when keys only goes back unless kept; zero when it is kept or off.
func (m *Manager) pendingLocked() time.Time {
	b, err := os.ReadFile(m.dropIn())
	if err != nil {
		return time.Time{}
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), pendingMark); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

func (m *Manager) keysOnlyLocked() bool {
	_, err := os.Lstat(m.dropIn())
	return err == nil
}

// State is who may log in over SSH now.
func (m *Manager) State(ctx context.Context) Access {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked(ctx)
}

func (m *Manager) stateLocked(ctx context.Context) Access {
	a := Access{Ports: m.ports(), Users: m.users(), KeysOnly: m.keysOnlyLocked(), Now: m.now().Unix()}
	if until := m.pendingLocked(); !until.IsZero() {
		a.Pending = &Pending{Until: until.Unix()}
	}
	if settings, err := m.effective(ctx); err != nil {
		a.Problem = err.Error()
	} else {
		a.PasswordLogin = passwords(settings)
	}
	if p := m.problemLocked(); p != "" {
		a.Problem = p
	}
	return a
}

// ports are the ports sshd listens on: read live for the machine's own config, else from the
// config in Dir.
func (m *Manager) ports() []uint16 {
	if m.Dir == "" {
		return ddos.SSHPorts(cmp.Or(m.Proc, "/proc"))
	}
	ports := ddos.SSHConfigPorts(m.config())
	if len(ports) == 0 {
		return []uint16{22}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}

// problemLocked says why passwords cannot be turned off here; empty when they can.
func (m *Manager) problemLocked() string {
	if _, err := os.Stat(m.config()); err != nil {
		return fmt.Sprintf("There is no SSH server config at %s, so SSH logins can't be managed here.", m.config())
	}
	inc := m.includes()
	switch {
	case !inc.found:
		return fmt.Sprintf("%s doesn't read %s/*.conf, so Nightjar can't turn password logins off without editing it. Add the line \"Include %s/*.conf\" near the top of it.", m.config(), m.dropDir(), m.dropDir())
	case inc.inMatch:
		return fmt.Sprintf("%s reads %s/*.conf inside a Match block (%s), so Nightjar's setting would only apply to some logins. Move the Include line above every Match block.", m.config(), m.dropDir(), inc.where)
	}
	// Nightjar must be able to write the folder: as root, and not in a sandbox that made it read-only
	// (as an agent installed before this could manage SSH logins is).
	dir := m.dropDir()
	if _, err := os.Stat(dir); err != nil {
		dir = m.dir()
	}
	switch err := unix.Access(dir, unix.W_OK); {
	case errors.Is(err, unix.EROFS) && dir != m.dropDir():
		return "This service can't make " + m.dropDir() + " (its sandbox makes " + dir + " read-only). Make the folder (mkdir " + m.dropDir() + ") and restart the agent."
	case errors.Is(err, unix.EROFS):
		return "This service can't write " + dir + " (its sandbox makes it read-only). Reinstall the agent from the Servers page so it can manage SSH logins."
	case err != nil:
		return "Nightjar can't write " + dir + " (" + err.Error() + "); it must run as root to manage SSH logins."
	}
	return ""
}

// AddKey adds a public key to an account's authorized_keys; a key already there is left as it is.
func (m *Manager) AddKey(ctx context.Context, user, line string) error {
	k, err := ParseKey(line)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.account(user)
	if err != nil {
		return err
	}
	if err := writeKey(a, k); err != nil {
		if errors.Is(err, unix.EROFS) {
			return fmt.Errorf("%s's home folder is read-only to this service; reinstall the agent from the Servers page so it can manage SSH logins", a.name)
		}
		return err
	}
	log.Info("SSH key added", "user", a.name, "key", k.Fingerprint())
	return nil
}

// SetKeysOnly turns keys only on (password logins off) or off (passwords back). Turning it on needs
// the account the owner logs in as (user) to have a key, since they would otherwise be shut out; it
// is in place at once and goes back by itself unless Keep is called within KeepWithin. Turning it off
// happens at once.
func (m *Manager) SetKeysOnly(ctx context.Context, on bool, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx = context.WithoutCancel(ctx) // half a change is worse than a slow one
	if !on {
		if !m.keysOnlyLocked() {
			return nil
		}
		log.Info("Password logins allowed again")
		return m.revertLocked(ctx)
	}
	if user == "" {
		return errors.New("say which account you log in as; it must have a key before passwords go off")
	}
	a, err := m.account(user)
	if err != nil {
		return err
	}
	if len(readKeys(a.home)) == 0 {
		return fmt.Errorf("add a key for %s first: with passwords off, an account without one can't log in", a.name)
	}
	if p := m.problemLocked(); p != "" {
		return errors.New(p)
	}
	if m.keysOnlyLocked() {
		return nil
	}
	until := m.now().Add(cmp.Or(m.KeepWithin, 2*time.Minute))
	if err := m.writeDropIn(until); err != nil {
		return err
	}
	if out, err := m.run(ctx, "sshd", "-t"); err != nil {
		os.Remove(m.dropIn())
		return fmt.Errorf("sshd turned the setting down, so passwords stay on: %s", lastLine(out, err))
	}
	if err := m.reload(ctx); err != nil {
		os.Remove(m.dropIn())
		return fmt.Errorf("could not reload sshd, so passwords stay on: %v", err)
	}
	settings, err := m.effective(ctx)
	if err == nil && passwords(settings) {
		err = fmt.Errorf("sshd still takes passwords with Nightjar's setting in place, so it was taken out again: %s", m.overriddenBy())
	}
	if err != nil {
		os.Remove(m.dropIn())
		m.reload(ctx)
		return err
	}
	log.Info("Password logins off; waiting to be kept", "within", until.Sub(m.now()).Round(time.Second))
	return nil
}

// Keep keeps keys only (keep), or puts passwords back now.
func (m *Manager) Keep(ctx context.Context, keep bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingLocked().IsZero() {
		return errors.New("there is no change waiting; it may have gone back already")
	}
	if !keep {
		log.Info("Password logins put back")
		return m.revertLocked(context.WithoutCancel(ctx))
	}
	// The same settings without the deadline: nothing for sshd to reload.
	if err := m.writeDropIn(time.Time{}); err != nil {
		return err
	}
	log.Info("Password logins off; kept")
	return nil
}

// Lapse puts passwords back when keys only was not kept in time. Run calls it every CheckEvery.
func (m *Manager) Lapse(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until := m.pendingLocked()
	if until.IsZero() || m.now().Before(until) {
		return
	}
	log.Warn("Keys-only SSH was not kept in time; password logins put back")
	if err := m.revertLocked(context.WithoutCancel(ctx)); err != nil {
		log.Warn("Could not put password logins back", "err", err)
	}
}

// Run puts passwords back if keys only was left waiting by the last run (whoever turned it on can no
// longer keep it), then until ctx ends puts it back when not kept in time. Keys only still waiting
// as it stops goes back too: nothing would be left to undo it.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if !m.pendingLocked().IsZero() {
		log.Warn("Keys-only SSH was waiting to be kept when Nightjar stopped; password logins put back")
		if err := m.revertLocked(ctx); err != nil {
			log.Warn("Could not put password logins back", "err", err)
		}
	}
	m.mu.Unlock()
	t := time.NewTicker(cmp.Or(m.CheckEvery, 5*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if !m.pendingLocked().IsZero() {
				log.Warn("Stopping while keys-only SSH waits to be kept; password logins put back")
				stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				m.revertLocked(stop)
				cancel()
			}
			m.mu.Unlock()
			return
		case <-t.C:
			m.Lapse(ctx)
		}
	}
}

// revertLocked takes Nightjar's file out and reloads sshd.
func (m *Manager) revertLocked(ctx context.Context) error {
	if err := os.Remove(m.dropIn()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not remove %s: %w", m.dropIn(), err)
	}
	if err := m.reload(ctx); err != nil {
		return fmt.Errorf("removed Nightjar's setting, but sshd did not reload, so it still applies until sshd restarts: %v", err)
	}
	return nil
}

// writeDropIn puts Nightjar's file in place, whole, with the deadline to keep it by (zero: kept).
// ChallengeResponseAuthentication is the older name of KbdInteractiveAuthentication, which OpenSSH
// before 8.7 reads apart; newer ones take it as the same setting, without a warning.
func (m *Manager) writeDropIn(until time.Time) error {
	var b strings.Builder
	b.WriteString("# Written by Nightjar: SSH logins with keys only, no passwords. Turn it off in Nightjar,\n")
	b.WriteString("# or delete this file and reload sshd, to allow passwords again.\n")
	if !until.IsZero() {
		fmt.Fprintf(&b, "%s%d\n", pendingMark, until.Unix())
	}
	b.WriteString("PasswordAuthentication no\nKbdInteractiveAuthentication no\nChallengeResponseAuthentication no\n")
	if err := os.MkdirAll(m.dropDir(), 0o755); err != nil {
		return err
	}
	// The temporary name does not end in .conf, so sshd never reads it half-written.
	tmp := m.dropIn() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.dropIn()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// reload has sshd read its config again, keeping the sessions open. Ubuntu and Debian call its
// service ssh, others sshd. One not running (ssh.socket starts it on the first login) reads the new
// config when it starts.
func (m *Manager) reload(ctx context.Context) error {
	var last error
	for _, unit := range []string{"ssh", "sshd"} {
		out, err := m.run(ctx, "systemctl", "reload", unit)
		if err == nil {
			return nil
		}
		msg := lastLine(out, err)
		if strings.Contains(msg, "not active") {
			return nil
		}
		last = errors.New(msg)
	}
	return last
}

// lastLine is the last line a command printed, else its error.
func lastLine(out string, err error) string {
	if msg := strings.TrimSpace(out); msg != "" {
		lines := strings.Split(msg, "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return err.Error()
}

// ---------- reading sshd's config ----------

// directive is one line of sshd's config, as sshd reads it: where it is, and whether a Match block
// holds it.
type directive struct {
	file  string
	line  int
	key   string // lower-case
	args  []string
	match bool
}

// walk reads sshd's config in the order sshd does, with each Include read where it is.
func (m *Manager) walk() []directive {
	var out []directive
	var read func(path string, inMatch bool, depth int)
	read = func(path string, inMatch bool, depth int) {
		b, err := os.ReadFile(path)
		if err != nil || depth > 16 {
			return
		}
		match := inMatch
		n := 0
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			n++
			key, args := configLine(sc.Text())
			if key == "" {
				continue
			}
			key = strings.ToLower(key)
			if key == "match" {
				match = inMatch || !(len(args) == 1 && strings.EqualFold(args[0], "all"))
			}
			out = append(out, directive{file: path, line: n, key: key, args: args, match: match})
			if key == "include" {
				for _, pattern := range args {
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(m.dir(), pattern)
					}
					files, _ := filepath.Glob(pattern)
					slices.Sort(files)
					for _, f := range files {
						read(f, match, depth+1)
					}
				}
			}
		}
	}
	read(m.config(), false, 0)
	return out
}

// included is whether sshd's config reads Nightjar's file, and where.
type included struct {
	found, inMatch bool
	where          string
}

// includes finds the Include that would read Nightjar's file, there or not; one outside any Match
// block wins.
func (m *Manager) includes() included {
	var inc included
	for _, d := range m.walk() {
		if d.key != "include" {
			continue
		}
		for _, pattern := range d.args {
			if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(m.dir(), pattern)
			}
			if ok, _ := filepath.Match(pattern, m.dropIn()); !ok {
				continue
			}
			if !d.match {
				return included{found: true}
			}
			inc = included{found: true, inMatch: true, where: fmt.Sprintf("%s line %d", d.file, d.line)}
		}
	}
	return inc
}

// overriddenBy names what most likely keeps passwords on over Nightjar's file: sshd keeps the first
// value it reads, so a setting read before it wins.
func (m *Manager) overriddenBy() string {
	names := map[string]string{"passwordauthentication": "PasswordAuthentication", "kbdinteractiveauthentication": "KbdInteractiveAuthentication", "challengeresponseauthentication": "ChallengeResponseAuthentication"}
	for _, d := range m.walk() {
		if d.file == m.dropIn() {
			break
		}
		if name, ok := names[d.key]; ok && !d.match && len(d.args) > 0 && strings.EqualFold(d.args[0], "yes") {
			return fmt.Sprintf("%s line %d sets %s yes before sshd reads %s, and sshd keeps the first value it reads. Change that line to no (or remove it) and try again.", d.file, d.line, name, DropIn)
		}
	}
	return "something in sshd's config turns them back on after Nightjar's file; look for PasswordAuthentication or KbdInteractiveAuthentication set to yes"
}

// configLine splits an sshd config line into its keyword and arguments ("Port 2222", "Port=2222"),
// leaving out a comment.
func configLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	if i := strings.Index(line, " #"); i >= 0 {
		line = line[:i]
	}
	key, rest, _ := strings.Cut(line, "=")
	if f := strings.Fields(key); len(f) > 1 {
		// The = was in a value, or there was none: the keyword ends at the first space.
		key, rest, _ = strings.Cut(strings.Join(strings.Fields(line), " "), " ")
		rest = strings.TrimPrefix(strings.TrimSpace(rest), "=")
	}
	return strings.TrimSpace(key), strings.Fields(rest)
}
