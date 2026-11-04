package agent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// Env is the agent's settings file. firewall is the one install opened its port in, if any, so
// uninstall can close it again.
func Env(listen, token string, plain bool, firewall string) string {
	env := "# Read by nightjar-agent.service. The token must match the one saved in the dashboard.\n" +
		"AGENT_LISTEN=" + listen + "\nAGENT_TOKEN=" + token + "\nAGENT_TLS=" + strconv.FormatBool(!plain) + "\n"
	if firewall != "" {
		env += "AGENT_FIREWALL=" + firewall + "\n"
	}
	return env
}

// Options are what install sets up besides the agent: its port in the firewall, and smartmontools.
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
// starts it; with opt, it also opens its port in the firewall and adds smartmontools. plain serves
// HTTP instead of HTTPS.
func Install(listen, token string, plain bool, opt Options, out io.Writer) error {
	// With no token, it is a reinstall: the agent's settings on this machine say how it was set up.
	if token == "" {
		saved, ok := savedSettings()
		if !ok {
			return errors.New("give the token from the dashboard's Add a server (-token); there is no agent on this machine to reinstall from")
		}
		listen, token, plain = saved.listen, saved.token, saved.plain
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
	firewall := ""
	if opt.Firewall {
		firewall = OpenPort(listen, out)
	}
	if err := os.WriteFile(EnvPath, []byte(Env(listen, token, plain, firewall)), 0o600); err != nil {
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
	if firewall == "" {
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
	if saved, ok := savedSettings(); ok && saved.firewall != "" {
		ClosePort(saved.firewall, saved.listen, out)
	}
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

// settings are what an installed agent's EnvPath holds.
type settings struct {
	listen, token, firewall string
	plain                   bool
}

// savedSettings reads the installed agent's settings, if there is one.
func savedSettings() (settings, bool) {
	env, err := os.ReadFile(EnvPath)
	if err != nil {
		return settings{}, false
	}
	return parseSettings(string(env))
}

func parseSettings(env string) (settings, bool) {
	s := settings{listen: ":" + strconv.Itoa(DefaultPort)}
	for _, line := range strings.Split(env, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "AGENT_LISTEN":
			s.listen = v
		case "AGENT_TOKEN":
			s.token = v
		case "AGENT_TLS":
			s.plain = v == "false"
		case "AGENT_FIREWALL":
			s.firewall = v
		}
	}
	return s, s.token != ""
}
