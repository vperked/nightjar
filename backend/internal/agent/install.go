package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
)

// Where install puts the agent. It runs from its own state folder, the one place its service may
// write, so it can replace itself with a new build; BinPath links to it for running it by hand.
const (
	StateDir = "/var/lib/nightjar-agent"
	ExePath  = StateDir + "/nightjar-agent"
	BinPath  = "/usr/local/bin/nightjar-agent"
	EnvPath  = "/etc/nightjar-agent.env"
	UnitName = "nightjar-agent.service"
	UnitPath = "/etc/systemd/system/" + UnitName
)

// Unit is the agent's systemd service. It runs as root because SMART needs raw access to the drives,
// and is otherwise locked down: it can read the machine but write nothing on it outside its own
// state folder, where its program lives so the dashboard can update it.
func Unit() string {
	return `[Unit]
Description=Nightjar agent (reports this machine's readings to the dashboard)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=` + EnvPath + `
ExecStart=` + ExePath + `
StateDirectory=nightjar-agent
# iptables (the legacy kind) takes this lock to change rules, when defending or setting firewall rules.
ReadWritePaths=-/run/xtables.lock
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true
MemoryMax=128M

[Install]
WantedBy=multi-user.target
`
}

// envHeader opens the agent's settings file.
const envHeader = "# Read by nightjar-agent.service. The token must match the one saved in the dashboard."

// Env is the agent's settings file. firewall is the one install opened its port in, if any, so
// uninstall can close it again.
func Env(listen, token string, plain bool, firewall string) string {
	return Settings{Listen: listen, Token: token, Plain: plain, Firewall: firewall}.Env()
}

// Settings are what an installed agent's EnvPath holds.
type Settings struct {
	Listen, Token string
	Plain         bool
	// Firewall is the firewall install opened the port in, so uninstall can close it again; FirewallOff
	// when install was told to leave the firewall alone (-firewall=false), which a reinstall keeps to.
	Firewall string
	// NoSmart is install having been told not to add smartmontools (-smart=false).
	NoSmart bool
	// Other are the file's other lines, such as AGENT_TRUSTED or AGENT_DEFEND added by hand, kept as
	// they are.
	Other []string
}

// FirewallOff is AGENT_FIREWALL for an agent installed with -firewall=false.
const FirewallOff = "off"

// Env writes the settings as the agent's settings file.
func (s Settings) Env() string {
	env := envHeader + "\nAGENT_LISTEN=" + s.Listen + "\nAGENT_TOKEN=" + s.Token + "\nAGENT_TLS=" + strconv.FormatBool(!s.Plain) + "\n"
	if s.Firewall != "" {
		env += "AGENT_FIREWALL=" + s.Firewall + "\n"
	}
	if s.NoSmart {
		env += "AGENT_SMART=off\n"
	}
	for _, line := range s.Other {
		env += line + "\n"
	}
	return env
}

// Options are what install sets up besides the agent: its port in the firewall (and iptables, to
// defend against attacks), and smartmontools.
type Options struct {
	Firewall bool
	Smart    bool
}

// ListenAddress checks a -listen value: a port, or host:port.
func ListenAddress(listen string) (string, error) {
	if _, err := strconv.Atoi(listen); err == nil {
		listen = ":" + listen
	}
	host, port, err := net.SplitHostPort(listen)
	p, perr := strconv.Atoi(port)
	if err != nil || perr != nil || p < 1 || p > 65535 || strings.ContainsAny(host, " \n") {
		return "", fmt.Errorf("listen on a port or host:port, not %q", listen)
	}
	return listen, nil
}

// Install copies this program to ExePath (linked from BinPath), writes its settings and service, and
// starts it; with opt, it also opens its port in the firewall, adds iptables and smartmontools. plain serves
// HTTP instead of HTTPS.
func Install(listen, token string, plain bool, opt Options, out io.Writer) error {
	saved, installed := savedSettings()
	// With no token, it is a reinstall: the agent's settings on this machine say how it was set up.
	if token == "" {
		if !installed {
			return errors.New("give the token from the dashboard's Add a server (-token); there is no agent on this machine to reinstall from")
		}
		listen, token, plain = saved.Listen, saved.Token, saved.Plain
		opt = saved.Reinstall(opt)
		fmt.Fprintln(out, "Reinstalling with this machine's agent settings.")
	}
	if len(token) < MinToken {
		return ErrShortToken
	}
	if strings.ContainsAny(token, " \t\n\"'\\$") {
		return errors.New("the token may not contain spaces, quotes, backslashes or $")
	}
	listen, err := ListenAddress(listen)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("installing the agent needs root: run it with sudo")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if err := os.MkdirAll(StateDir, 0o755); err != nil {
		return err
	}
	if self != ExePath {
		if err := copyFile(self, ExePath); err != nil {
			return fmt.Errorf("copying the agent to %s: %w", ExePath, err)
		}
	}
	// An agent installed before it ran from its state folder had its program here; it becomes the link.
	os.Remove(BinPath + ".link")
	if err := os.Symlink(ExePath, BinPath+".link"); err != nil {
		return err
	}
	if err := os.Rename(BinPath+".link", BinPath); err != nil {
		return err
	}
	if opt.Smart {
		InstallSmart(out)
	}
	firewall := FirewallOff
	if opt.Firewall {
		firewall = OpenPort(listen, out)
		if firewall == "" && saved.Firewall != FirewallOff {
			// The port an earlier install opened is still open; uninstall should still close it.
			firewall = saved.Firewall
		}
		// Defending against attacks puts rules in iptables chains of the agent's own.
		InstallIPTables(out)
	}
	// Settings added to the file by hand stay.
	env := Settings{Listen: listen, Token: token, Plain: plain, Firewall: firewall, NoSmart: !opt.Smart, Other: saved.Other}
	if err := os.WriteFile(EnvPath, []byte(env.Env()), 0o600); err != nil {
		return err
	}
	os.Chmod(EnvPath, 0o600) // WriteFile keeps an existing file's mode
	if err := os.WriteFile(UnitPath, []byte(Unit()), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", UnitName}, {"restart", UnitName}} {
		if msg, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(msg)))
		}
	}
	scheme := "HTTPS"
	if plain {
		scheme = "plain HTTP"
	}
	fmt.Fprintf(out, "The agent is running as %s, serving %s on %s.\n", UnitName, scheme, listen)
	if !plain {
		fmt.Fprintln(out, "It updates itself to the dashboard's version.")
	}
	if firewall == "" || firewall == FirewallOff {
		fmt.Fprintln(out, "Make sure the dashboard's machine can reach that port; nothing else needs it.")
	}
	fmt.Fprintln(out, "On a cloud server, also allow the port in the provider's firewall (OVH, AWS, Hetzner and others filter before the server sees it).")
	return nil
}

// Uninstall stops the agent and removes everything Install wrote.
func Uninstall(out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("removing the agent needs root: run it with sudo")
	}
	exec.Command("systemctl", "disable", "--now", UnitName).Run()
	if saved, ok := savedSettings(); ok && saved.Firewall != "" && saved.Firewall != FirewallOff {
		ClosePort(saved.Firewall, saved.Listen, out)
	}
	// The agent takes its chains out as it stops; these are for one that was killed or never got
	// to. Otherwise its rules, blocks and a kept default drop would stay until the next reboot.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	(&firewall.Manager{}).Uninstall(ctx)
	(&ddos.Defender{}).Stop(ctx)
	cancel()
	for _, p := range []string{UnitPath, EnvPath, BinPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	os.RemoveAll(StateDir)
	exec.Command("systemctl", "daemon-reload").Run()
	fmt.Fprintln(out, "The agent is removed.")
	return nil
}

// copyFile writes src to dst through a temporary file, so a running agent's binary is replaced whole.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// savedSettings reads the installed agent's settings, if there is one.
func savedSettings() (Settings, bool) {
	env, err := os.ReadFile(EnvPath)
	if err != nil {
		return Settings{}, false
	}
	return ParseSettings(string(env))
}

// ParseSettings reads an agent's settings file; ok is false when it holds no token.
func ParseSettings(env string) (s Settings, ok bool) {
	s.Listen = ":" + strconv.Itoa(DefaultPort)
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		k, v, _ := strings.Cut(line, "=")
		switch {
		case line == "" || line == envHeader:
		case k == "AGENT_LISTEN":
			s.Listen = v
		case k == "AGENT_TOKEN":
			s.Token = v
		case k == "AGENT_TLS":
			s.Plain = v == "false"
		case k == "AGENT_FIREWALL":
			s.Firewall = v
		case k == "AGENT_SMART":
			s.NoSmart = v == "off"
		default:
			s.Other = append(s.Other, line)
		}
	}
	return s, s.Token != ""
}

// Reinstall is what a reinstall sets up, given install's options: what the first install was told
// to leave alone stays alone.
func (s Settings) Reinstall(opt Options) Options {
	if s.Firewall == FirewallOff {
		opt.Firewall = false
	}
	if s.NoSmart {
		opt.Smart = false
	}
	return opt
}
