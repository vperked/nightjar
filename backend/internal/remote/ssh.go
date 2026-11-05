package remote

// SSH is the way in when the agent cannot help: a server can be given a login (a key on this machine,
// a user and a port), and Nightjar can then test it, restart the agent, or reboot the server. The
// private key stays a file on this machine; only its path is saved, and it is never read here.

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SSH is how Nightjar logs in to a host.
type SSH struct {
	// Host is the address to connect to; empty uses the server's own address.
	Host string `json:"host,omitempty"`
	Port int    `json:"port"`
	User string `json:"user"`
	// Key is the private key's path on this machine.
	Key string `json:"key"`
}

// SSHKey is a key pair Nightjar can log in with. The private half is only ever named by its path.
type SSHKey struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment,omitempty"`
	// Public is the line to add to the server's ~/.ssh/authorized_keys.
	Public string `json:"public"`
	// Own is true for keys in Nightjar's own folder, made with NewSSHKey.
	Own bool `json:"own"`
}

// SSHCheck is how the last login with a host's SSH login went, so the dashboard can say whether it
// works without logging in again.
type SSHCheck struct {
	OK bool    `json:"ok"`
	At float64 `json:"at"`
	// As is who it logged in as, user@hostname, from a test.
	As    string `json:"as,omitempty"`
	Error string `json:"error,omitempty"`
	// HostKey is the server's host key Nightjar trusts, as a type and fingerprint
	// ("ED25519 SHA256:…"); a different key later is refused.
	HostKey string `json:"host_key,omitempty"`
}

// SSH actions a host can be asked for.
const (
	SSHTest           = "test"
	SSHRestartAgent   = "restart-agent"
	SSHReinstallAgent = "reinstall-agent"
	SSHReboot         = "reboot"
)

// asRoot runs a command directly as root, or through sudo without a password prompt otherwise, so a
// login that would need a password fails at once rather than waiting.
func asRoot(cmd string) string {
	return fmt.Sprintf(`if [ "$(id -u)" -eq 0 ]; then %s; else sudo -n %s; fi`, cmd, cmd)
}

var sshActions = map[string]string{
	SSHTest:         `echo "$(id -un)@$(hostname)"; uptime`,
	SSHRestartAgent: asRoot("systemctl restart nightjar-agent") + ` && echo "nightjar-agent restarted"`,
	// The dashboard's own build arrives on stdin and installs as the agent was set up before, with
	// the token and settings already on the server, so no secret is on a command line.
	SSHReinstallAgent: `set -e; f=$(mktemp /tmp/nightjar-agent.XXXXXX); trap 'rm -f "$f"' EXIT; cat > "$f"; chmod 700 "$f"; ` +
		asRoot(`"$f" install`),
	SSHReboot: asRoot("systemctl reboot") + ` && echo "rebooting"`,
}

// sshArch asks a server which processor it has, as Go names it.
const sshArch = `uname -m`

var unameArch = map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}

// sshOutputLimit caps what an action's output keeps.
const sshOutputLimit = 16 << 10

var (
	sshUserRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]{0,31}$`)
	sshHostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)
	keyNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
)

// sshDirs are where keys are looked for: Nightjar's own folder first, then the monitor user's ~/.ssh.
func (m *Monitor) sshDirs() []string {
	dirs := []string{}
	if m.opt.SSHDir != "" {
		dirs = append(dirs, m.opt.SSHDir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".ssh"))
	}
	return dirs
}

// SSHKeys lists every key pair found: a private key with its .pub beside it.
func (m *Monitor) SSHKeys(ctx context.Context) []SSHKey {
	keys := []SSHKey{}
	seen := map[string]bool{}
	for i, dir := range m.sshDirs() {
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		pubs, _ := filepath.Glob(filepath.Join(abs, "*.pub"))
		slices.Sort(pubs)
		for _, pub := range pubs {
			priv := strings.TrimSuffix(pub, ".pub")
			if seen[priv] {
				continue
			}
			if st, err := os.Stat(priv); err != nil || !st.Mode().IsRegular() {
				continue
			}
			k, err := m.describeKey(ctx, priv)
			if err != nil {
				continue
			}
			k.Own = i == 0 && m.opt.SSHDir != ""
			seen[priv] = true
			keys = append(keys, k)
		}
	}
	return keys
}

// describeKey reads a key's public half and asks ssh-keygen for its fingerprint.
func (m *Monitor) describeKey(ctx context.Context, priv string) (SSHKey, error) {
	raw, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return SSHKey{}, err
	}
	public := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(public, "ssh-") && !strings.HasPrefix(public, "ecdsa-") && !strings.HasPrefix(public, "sk-") {
		return SSHKey{}, errors.New("not a public key")
	}
	k := SSHKey{Path: priv, Name: filepath.Base(priv), Public: public}
	if f := strings.Fields(public); len(f) > 2 {
		k.Comment = strings.Join(f[2:], " ")
	}
	// "256 SHA256:abc… comment (ED25519)"
	out, _, err := m.exec(ctx, 5*time.Second, "ssh-keygen", "-l", "-f", priv+".pub")
	if err != nil {
		return SSHKey{}, err
	}
	if f := strings.Fields(out); len(f) >= 2 {
		k.Fingerprint = f[1]
		k.Type = strings.Trim(f[len(f)-1], "()")
	}
	return k, nil
}

// NewSSHKey makes an ed25519 key pair with no passphrase in Nightjar's own folder.
func (m *Monitor) NewSSHKey(ctx context.Context, name string) (SSHKey, error) {
	_, path, err := m.newKeyPath(name)
	if err != nil {
		return SSHKey{}, err
	}
	host, _ := os.Hostname()
	if _, stderr, err := m.exec(ctx, 30*time.Second, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nightjar@"+cmp.Or(host, "monitor"), "-f", path); err != nil {
		return SSHKey{}, fmt.Errorf("ssh-keygen failed: %s", cmp.Or(strings.TrimSpace(stderr), err.Error()))
	}
	log.Info("SSH key made", "path", path)
	k, err := m.describeKey(ctx, path)
	k.Own = true
	return k, err
}

// maxPrivateKey is the most an imported private key may be; an RSA 4096 key is about 3.4 KB.
const maxPrivateKey = 16 << 10

// ImportSSHKey keeps a private key someone already has (pasted in) in Nightjar's own folder, readable
// only by its owner, beside the public half ssh-keygen works out from it. A key with a passphrase is
// refused: Nightjar logs in with nobody there to type it.
func (m *Monitor) ImportSSHKey(ctx context.Context, name, private string) (SSHKey, error) {
	dir, path, err := m.newKeyPath(name)
	if err != nil {
		return SSHKey{}, err
	}
	private = strings.TrimSpace(strings.ReplaceAll(private, "\r\n", "\n")) + "\n"
	if len(private) > maxPrivateKey || !strings.HasPrefix(private, "-----BEGIN ") || !strings.Contains(private, "PRIVATE KEY-----") {
		return SSHKey{}, errors.New("paste the whole private key, from -----BEGIN to -----END; it is the file without .pub, such as ~/.ssh/id_ed25519")
	}
	tmp, err := os.CreateTemp(dir, ".import-*")
	if err != nil {
		return SSHKey{}, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return SSHKey{}, err
	}
	if _, err := tmp.WriteString(private); err != nil {
		tmp.Close()
		return SSHKey{}, err
	}
	if err := tmp.Close(); err != nil {
		return SSHKey{}, err
	}
	// -P "" refuses at once a key that needs a passphrase, instead of asking for one.
	public, stderr, err := m.exec(ctx, 10*time.Second, "ssh-keygen", "-y", "-P", "", "-f", tmp.Name())
	if err != nil {
		if strings.Contains(stderr, "passphrase") || strings.Contains(stderr, "incorrect") {
			return SSHKey{}, errors.New("that key has a passphrase; Nightjar logs in unattended, so use a key without one (or make a new key here)")
		}
		return SSHKey{}, errors.New("ssh-keygen could not read that as a private key")
	}
	public = strings.TrimSpace(public)
	host, _ := os.Hostname()
	if len(strings.Fields(public)) == 2 {
		public += " " + filepath.Base(path) + "@nightjar-" + cmp.Or(host, "monitor")
	}
	if err := os.WriteFile(path+".pub", []byte(public+"\n"), 0o644); err != nil {
		return SSHKey{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(path + ".pub")
		return SSHKey{}, err
	}
	log.Info("SSH key added", "path", path)
	k, err := m.describeKey(ctx, path)
	k.Own = true
	return k, err
}

// newKeyPath checks a name for a key in Nightjar's own folder, making the folder if need be.
func (m *Monitor) newKeyPath(name string) (dir, path string, err error) {
	if m.opt.SSHDir == "" {
		return "", "", errors.New("no folder is set for Nightjar's SSH keys")
	}
	name = cmp.Or(strings.TrimSpace(name), "nightjar")
	if !keyNameRe.MatchString(name) || name == "known_hosts" || name == "known_hosts.confirmed" || name == "authorized_keys" || name == "config" {
		return "", "", errors.New("name the key with letters, numbers, dots, dashes or underscores")
	}
	if dir, err = filepath.Abs(m.opt.SSHDir); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("could not make the SSH key folder: %w", err)
	}
	path = filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		return "", "", fmt.Errorf("there is already a key called %s", name)
	}
	return dir, path, nil
}

// NormalizeSSH checks a login before it is saved: a key Nightjar can find, a plain user name, and
// a port. The host may be empty, meaning the server's own address.
func (m *Monitor) NormalizeSSH(ctx context.Context, c SSH) (SSH, error) {
	c.Host, c.User, c.Key = strings.TrimSpace(c.Host), strings.TrimSpace(c.User), strings.TrimSpace(c.Key)
	c.User = cmp.Or(c.User, "root")
	c.Port = cmp.Or(c.Port, 22)
	if !sshUserRe.MatchString(c.User) {
		return c, fmt.Errorf("%q is not a user name", c.User)
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, errors.New("the SSH port is a number from 1 to 65535")
	}
	if c.Host != "" && !sshHostRe.MatchString(c.Host) {
		return c, fmt.Errorf("%q is not a host name or IP address", c.Host)
	}
	if !slices.ContainsFunc(m.SSHKeys(ctx), func(k SSHKey) bool { return k.Path == c.Key }) {
		return c, errors.New("choose one of the keys Nightjar found, or make a new one")
	}
	return c, nil
}

// SetSSH saves how to log in to a host; nil forgets it.
func (m *Monitor) SetSSH(ctx context.Context, id string, c *SSH) (State, error) {
	m.mu.Lock()
	sshOnly := m.find(id) != nil && m.find(id).host.isSSHOnly()
	m.mu.Unlock()
	if c == nil && sshOnly {
		return State{}, errors.New("this server is watched through its SSH login; remove the server instead")
	}
	if c != nil && sshOnly && strings.TrimSpace(c.Host) == "" {
		return State{}, errors.New("enter the server's address")
	}
	if c != nil {
		n, err := m.NormalizeSSH(ctx, *c)
		if err != nil {
			return State{}, err
		}
		c = &n
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil {
		return State{}, ErrNotFound
	}
	if c == nil || e.host.SSH == nil || *c != *e.host.SSH {
		// A different login has not been tried yet.
		e.host.SSHCheck = nil
	}
	e.host.SSH = c
	if sshOnly {
		e.host.Address = c.Host
	}
	if err := m.saveLocked(); err != nil {
		return State{}, err
	}
	return e.state(History), nil
}

// sshTarget is the address an SSH login connects to: its own host, else the server's.
func sshTarget(h Host) string {
	if h.SSH != nil && h.SSH.Host != "" {
		return h.SSH.Host
	}
	if u, err := url.Parse(h.Address); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return h.Address
}

// SSHCommand is the command to log in by hand, as the dashboard shows it.
func SSHCommand(h Host) string {
	if h.SSH == nil {
		return ""
	}
	cmd := "ssh -i " + h.SSH.Key
	if h.SSH.Port != 22 {
		cmd += " -p " + strconv.Itoa(h.SSH.Port)
	}
	return cmd + " " + h.SSH.User + "@" + sshTarget(h)
}

// RunSSH logs in to a host and runs one of the fixed actions, returning what it printed.
func (m *Monitor) RunSSH(ctx context.Context, id, action string) (string, error) {
	command, ok := sshActions[action]
	if !ok {
		return "", fmt.Errorf("unknown SSH action %q", action)
	}
	m.mu.Lock()
	e := m.find(id)
	var h Host
	if e != nil {
		h = e.host
	}
	m.mu.Unlock()
	if e == nil {
		return "", ErrNotFound
	}
	if h.SSH == nil {
		return "", errors.New("set up an SSH login for this server first")
	}
	if (action == SSHRestartAgent || action == SSHReinstallAgent) && h.isSSHOnly() {
		return "", errors.New("this server has no agent")
	}
	if action != SSHTest {
		// An action that changes the server runs to its end even when whoever asked goes away (a
		// closed tab, a request that timed out): a reinstall cut off halfway can leave the agent
		// stopped. Its own timeouts still bound it.
		ctx = context.WithoutCancel(ctx)
	}
	var stdin []byte
	if action == SSHReinstallAgent {
		arch := ""
		m.mu.Lock()
		if e := m.find(id); e != nil && e.readings != nil {
			arch = e.readings.System.Architecture
		}
		m.mu.Unlock()
		if arch == "" {
			// The agent is down, so ask the server itself.
			out, stderr, err := m.ssh(ctx, h, sshArch, nil, 30*time.Second)
			if err != nil {
				return "", sshError(stderr, err)
			}
			arch = unameArch[strings.TrimSpace(out)]
			if arch == "" {
				return "", fmt.Errorf("there is no agent for a %s processor", strings.TrimSpace(out))
			}
		}
		build, _, err := m.loadBuild(arch)
		if err != nil {
			return "", err
		}
		stdin = build
	}
	out, stderr, err := m.ssh(ctx, h, command, stdin, 5*time.Minute)
	out = clip(out)
	if err != nil {
		// A reboot drops the connection it was asked over; that is it working.
		if action == SSHReboot && strings.Contains(stderr, "closed by remote host") {
			log.Info("Server rebooted over SSH", "name", h.Name)
			m.noteLogin(ctx, id, h, SSHCheck{OK: true})
			return "rebooting", nil
		}
		err = sshError(stderr, err)
		log.Warn("SSH action failed", "name", h.Name, "action", action, "err", err)
		if action == SSHTest {
			m.noteLogin(ctx, id, h, SSHCheck{Error: err.Error()})
		}
		return out, err
	}
	log.Info("SSH action", "name", h.Name, "action", action)
	check := SSHCheck{OK: true}
	if action == SSHTest {
		check.As, _, _ = strings.Cut(strings.TrimSpace(out), "\n")
	}
	m.noteLogin(ctx, id, h, check)
	if action == SSHReinstallAgent {
		// It restarts on the dashboard's build; the next poll says how it stands.
		m.mu.Lock()
		if e := m.find(id); e != nil {
			e.update, e.updateTried = AgentUpdate{State: "updated", Detail: "Reinstalled over SSH with this dashboard's build.", At: float64(m.opt.Now().Unix())}, time.Time{}
		}
		m.mu.Unlock()
	}
	return strings.TrimSpace(out), nil
}

// noteLogin keeps how a login with h's SSH login went, unless the login was changed meanwhile. Any
// action that worked shows the login works; only a test that failed is kept as not working, since
// other actions can fail after logging in (a sudo that wants a password).
func (m *Monitor) noteLogin(ctx context.Context, id string, h Host, c SSHCheck) {
	c.At = float64(m.opt.Now().Unix())
	if c.OK {
		c.HostKey = m.hostKey(ctx, h)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil || e.host.SSH == nil || *e.host.SSH != *h.SSH {
		return
	}
	if c.OK && c.As == "" && e.host.SSHCheck != nil {
		c.As = e.host.SSHCheck.As
	}
	if !c.OK && e.host.SSHCheck != nil {
		c.HostKey = e.host.SSHCheck.HostKey
	}
	e.host.SSHCheck = &c
	if err := m.saveLocked(); err != nil {
		log.Warn("Could not save how an SSH login went", "name", h.Name, "err", err)
	}
}

// hostKey is the type and fingerprint of the host key Nightjar keeps for h, from its known_hosts.
func (m *Monitor) hostKey(ctx context.Context, h Host) string {
	if keys := m.hostKeysIn(ctx, h, m.knownHosts()); len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// hostKeysIn are the types and fingerprints of the host keys file keeps for h.
func (m *Monitor) hostKeysIn(ctx context.Context, h Host, file string) []string {
	if file == "" {
		return nil
	}
	name := sshTarget(h)
	if h.SSH.Port != 22 {
		name = "[" + name + "]:" + strconv.Itoa(h.SSH.Port)
	}
	out, _, err := m.execIn(ctx, 10*time.Second, nil, nil, "ssh-keygen", "-l", "-F", name, "-f", file)
	if err != nil {
		return nil
	}
	return parseHostKeys(out)
}

// knownHosts is the file of host keys logins check: Nightjar's own, else the monitor user's.
func (m *Monitor) knownHosts() string {
	if m.opt.SSHDir != "" {
		if dir, err := filepath.Abs(m.opt.SSHDir); err == nil {
			return filepath.Join(dir, "known_hosts")
		}
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ssh", "known_hosts")
	}
	return ""
}

// confirmedHosts is the file of host keys someone checked and confirmed (TrustHostKey), the only ones
// a password goes to. A first login with a key adds whatever key answers to knownHosts by itself, so
// a key there proves nothing about who answered.
func (m *Monitor) confirmedHosts() string {
	file := m.knownHosts()
	switch {
	case file == "":
		return ""
	case m.opt.SSHDir != "":
		return file + ".confirmed"
	}
	return file + ".nightjar-confirmed"
}

// parseHostKeys reads the fingerprints ssh-keygen -l -F prints ("host ED25519 SHA256:…"), in order.
func parseHostKeys(out string) []string {
	var keys []string
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		for i, field := range f {
			if strings.HasPrefix(field, "SHA256:") {
				if i == 2 {
					field = f[1] + " " + field
				}
				keys = append(keys, field)
				break
			}
		}
	}
	return keys
}

// ssh logs in to a host and runs command, with stdin when given.
func (m *Monitor) ssh(ctx context.Context, h Host, command string, stdin []byte, timeout time.Duration) (string, string, error) {
	target := sshTarget(h)
	if !sshHostRe.MatchString(target) {
		return "", "", fmt.Errorf("%q is not an address SSH can use; set the SSH host", target)
	}
	// The first login with a key trusts and remembers the server's host key; a different key later is
	// refused.
	args := append(m.sshArgs(*h.SSH, "accept-new", ""), "-i", h.SSH.Key, "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "--", target, command)
	return m.execIn(ctx, timeout, stdin, nil, "ssh", args...)
}

// sshArgs are the options every login uses: its port and user, short timeouts, and the record of host
// keys in knownHosts (Nightjar's own when empty), checked as hostKeys says (ssh's StrictHostKeyChecking).
func (m *Monitor) sshArgs(c SSH, hostKeys, knownHosts string) []string {
	args := []string{
		"-p", strconv.Itoa(c.Port),
		"-l", c.User,
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
		"-o", "StrictHostKeyChecking=" + hostKeys,
	}
	if knownHosts == "" && m.opt.SSHDir != "" {
		if dir, err := filepath.Abs(m.opt.SSHDir); err == nil && os.MkdirAll(dir, 0o700) == nil {
			knownHosts = filepath.Join(dir, "known_hosts")
		}
	}
	if knownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+knownHosts)
	}
	return args
}

// AuthorizeCommand lets a public key log in as whoever runs it: it makes ~/.ssh and authorized_keys
// with the permissions sshd insists on, and adds the key unless it is there already.
func AuthorizeCommand(publicKey string) string {
	key := "'" + strings.ReplaceAll(strings.TrimSpace(publicKey), "'", `'\''`) + "'"
	return "mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && " +
		"(grep -qxF " + key + " ~/.ssh/authorized_keys || echo " + key + " >> ~/.ssh/authorized_keys)"
}

// AskPassEnv names the environment that makes the nightjar program answer ssh's password prompt
// (see cmd/nightjar): ssh runs it as its askpass program, so the password never touches a command
// line or a file.
const (
	AskPassEnv  = "NIGHTJAR_ASKPASS"
	PasswordEnv = "NIGHTJAR_SSH_PASSWORD"
	maxPassword = 1024
)

// AuthorizeKey puts a login's key on its server: it logs in once with the account's password and adds
// the key's public line to its authorized_keys. The password is used for that one login and kept
// nowhere. id, when given, is the server the login is for, whose address it uses if the login has none.
// The password only goes to a server whose host key someone confirmed: for any other, it answers a
// *HostKeyError naming the key the server presents, to check and TrustHostKey. A key a key login
// remembered by itself does not count, since that login trusted whatever answered first.
func (m *Monitor) AuthorizeKey(ctx context.Context, id string, login SSH, password string) (string, error) {
	c, err := m.NormalizeSSH(ctx, login)
	if err != nil {
		return "", err
	}
	if password == "" || len(password) > maxPassword || strings.ContainsAny(password, "\n\r\x00") {
		return "", errors.New("enter the password for that user on the server")
	}
	h, target, err := m.loginTarget(id, c)
	if err != nil {
		return "", err
	}
	public, err := os.ReadFile(c.Key + ".pub")
	if err != nil {
		return "", errors.New("the key's public half is missing")
	}
	confirmed := m.confirmedHosts()
	if len(m.hostKeysIn(ctx, h, confirmed)) == 0 {
		// Trusting whatever answers first would hand the password to anyone on the way.
		keys, err := m.scanHostKeys(ctx, target, c.Port)
		if err != nil {
			return "", err
		}
		return "", &HostKeyError{HostKey: keys[0].fingerprint}
	}
	askpass := m.opt.AskPass
	if askpass == "" {
		if askpass, err = os.Executable(); err != nil {
			return "", err
		}
	}
	args := append(m.sshArgs(c, "yes", confirmed),
		"-o", "BatchMode=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "PreferredAuthentications=password,keyboard-interactive",
		"-o", "NumberOfPasswordPrompts=1",
		"--", target, AuthorizeCommand(string(public)))
	env := append(os.Environ(), "SSH_ASKPASS="+askpass, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=nightjar:0", AskPassEnv+"=1", PasswordEnv+"="+password)
	out, stderr, err := m.execIn(ctx, time.Minute, nil, env, "ssh", args...)
	if err != nil {
		if strings.Contains(stderr, "Permission denied") {
			err = errors.New("the server turned the password down, or does not take passwords; run the command shown on the server instead")
		} else {
			err = sshError(stderr, err)
		}
		log.Warn("Could not put an SSH key on a server", "target", target, "user", c.User, "err", err)
		return out, err
	}
	log.Info("SSH key put on a server", "target", target, "user", c.User, "key", filepath.Base(c.Key))
	return strings.TrimSpace(out), nil
}

// loginTarget is the server a login is for, and the address it connects to: id's, when given, unless
// the login names its own host.
func (m *Monitor) loginTarget(id string, c SSH) (Host, string, error) {
	h := Host{SSH: &c}
	if id != "" {
		m.mu.Lock()
		e := m.find(id)
		if e != nil {
			h = e.host
			h.SSH = &c
		}
		m.mu.Unlock()
		if e == nil {
			return h, "", ErrNotFound
		}
	}
	target := sshTarget(h)
	if target == "" || !sshHostRe.MatchString(target) {
		return h, "", errors.New("enter the server's address")
	}
	return h, target, nil
}

// HostKeyError is AuthorizeKey declining to send a password to a server whose host key nobody has
// confirmed. HostKey is the key the server presents now ("ED25519 SHA256:…"): once someone has
// checked on the server that it is its own, TrustHostKey keeps it and the password can go.
type HostKeyError struct {
	HostKey string
}

func (e *HostKeyError) Error() string {
	kind, _, _ := strings.Cut(e.HostKey, " ")
	return fmt.Sprintf("Nobody has confirmed this server's host key yet; check that it is %s (on the server: ssh-keygen -lf /etc/ssh/ssh_host_%s_key.pub) and confirm it before the password is sent",
		e.HostKey, strings.ToLower(kind))
}

// TrustHostKey keeps a server's host key once someone has checked it: hostKey is the fingerprint a
// HostKeyError showed them, and the server must still present it. It must not contradict a key Nightjar
// already keeps for the server; logins then refuse any other key.
func (m *Monitor) TrustHostKey(ctx context.Context, id string, login SSH, hostKey string) error {
	c, err := m.NormalizeSSH(ctx, login)
	if err != nil {
		return err
	}
	h, target, err := m.loginTarget(id, c)
	if err != nil {
		return err
	}
	hostKey = strings.TrimSpace(hostKey)
	if hostKey == "" {
		return errors.New("confirm the server's host key")
	}
	file, confirmed := m.knownHosts(), m.confirmedHosts()
	if file == "" || confirmed == "" {
		return errors.New("there is nowhere to keep host keys")
	}
	trusted := m.hostKeysIn(ctx, h, confirmed)
	if slices.Contains(trusted, hostKey) {
		return nil
	}
	// A key login may have kept a key already (without anyone checking it); the confirmed one must be
	// that key, or key logins and the password login would each trust a different server.
	known := m.hostKeysIn(ctx, h, file)
	if len(trusted) > 0 || len(known) > 0 && !slices.Contains(known, hostKey) {
		return fmt.Errorf("the server's host key is not the one Nightjar saw before; if it was reinstalled, remove its lines from %s and %s", file, confirmed)
	}
	keys, err := m.scanHostKeys(ctx, target, c.Port)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k scannedKey) bool { return k.fingerprint == hostKey })
	if i < 0 {
		log.Warn("An SSH host key was not the one confirmed", "target", target, "confirmed", hostKey, "presented", keys[0].fingerprint)
		return fmt.Errorf("the server now presents %s, not the key you confirmed; check it again on the server", keys[0].fingerprint)
	}
	// Key logins check known_hosts too, so they refuse any other key from now on.
	if len(known) == 0 {
		if err := appendHostKey(file, keys[i].line); err != nil {
			return err
		}
	}
	if err := appendHostKey(confirmed, keys[i].line); err != nil {
		return err
	}
	log.Info("SSH host key trusted", "target", target, "key", hostKey)
	return nil
}

// appendHostKey adds a known_hosts line to file, making it (readable only by its owner) if need be.
func appendHostKey(file, line string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// scannedKey is a host key a server presents: its known_hosts line, and its type and fingerprint.
type scannedKey struct {
	line, fingerprint string
}

// hostKeyTypes are the host key types asked for, in the order ssh prefers them.
var hostKeyTypes = []string{"ED25519", "ECDSA", "RSA"}

// scanHostKeys asks a server for its host keys without logging in, the one ssh would use first.
func (m *Monitor) scanHostKeys(ctx context.Context, target string, port int) ([]scannedKey, error) {
	out, stderr, err := m.exec(ctx, 20*time.Second, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port), "-t", "ed25519,ecdsa,rsa", target)
	keys := []scannedKey{}
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "256 SHA256:abc… host (ED25519)"
		fp, _, err := m.execIn(ctx, 5*time.Second, []byte(line+"\n"), nil, "ssh-keygen", "-l", "-f", "-")
		if f := strings.Fields(fp); err == nil && len(f) >= 3 && strings.HasPrefix(f[1], "SHA256:") {
			keys = append(keys, scannedKey{line: line, fingerprint: strings.Trim(f[len(f)-1], "()") + " " + f[1]})
		}
	}
	if len(keys) == 0 {
		// ssh-keyscan notes each server it reached as a comment; what is left is what went wrong.
		var problems []string
		for line := range strings.Lines(stderr) {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				problems = append(problems, line)
			}
		}
		if err == nil || len(problems) == 0 {
			err = errors.New("the server showed no SSH host key; check its address and SSH port")
		}
		return nil, sshError(strings.Join(problems, "\n"), err)
	}
	rank := func(k scannedKey) int {
		kind, _, _ := strings.Cut(k.fingerprint, " ")
		if i := slices.Index(hostKeyTypes, kind); i >= 0 {
			return i
		}
		return len(hostKeyTypes)
	}
	slices.SortStableFunc(keys, func(a, b scannedKey) int { return rank(a) - rank(b) })
	return keys, nil
}

// sshError turns ssh's complaint into what to do about it.
func sshError(stderr string, err error) error {
	s := strings.TrimSpace(stderr)
	switch {
	case strings.Contains(s, "REMOTE HOST IDENTIFICATION HAS CHANGED"), strings.Contains(s, "Host key verification failed"):
		return errors.New("the server's host key is not the one Nightjar saw before; if it was reinstalled, remove its line from known_hosts in Nightjar's SSH folder")
	case strings.Contains(s, "Permission denied"):
		return errors.New("the server refused the key; add the key's public line to ~/.ssh/authorized_keys for that user")
	case strings.Contains(s, "sudo: a password is required"), strings.Contains(s, "sudo: a terminal is required"):
		return errors.New("that user needs a password for sudo; log in as root, or let it run systemctl with sudo without one")
	case strings.Contains(s, "Connection refused"):
		return errors.New("nothing answered SSH on that port")
	case strings.Contains(s, "timed out"), strings.Contains(s, "No route to host"):
		return errors.New("the server did not answer SSH in time")
	case strings.Contains(s, "Could not resolve hostname"):
		return errors.New("that host name does not resolve")
	case s != "":
		return errors.New(lastLine(s))
	}
	return err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func clip(s string) string {
	if len(s) > sshOutputLimit {
		return s[len(s)-sshOutputLimit:]
	}
	return s
}

// exec runs a program with a timeout, through Options.Exec when set (tests).
func (m *Monitor) exec(ctx context.Context, timeout time.Duration, name string, args ...string) (string, string, error) {
	return m.execIn(ctx, timeout, nil, nil, name, args...)
}

// execIn is exec with stdin, and with env in place of the monitor's own environment when given.
func (m *Monitor) execIn(ctx context.Context, timeout time.Duration, stdin []byte, env []string, name string, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if m.opt.Exec != nil {
		return m.opt.Exec(ctx, stdin, env, name, args...)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Env = &stdout, &stderr, env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		// Only running out of time is a timeout; a request given up on is not the server's doing.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.String(), stderr.String() + "\ntimed out", ctx.Err()
		}
		return stdout.String(), stderr.String(), ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}
