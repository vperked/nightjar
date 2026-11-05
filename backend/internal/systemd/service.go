// Package systemd installs the monitor as a systemd service.
package systemd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var serviceNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]*$`)

// Available reports whether systemctl is present and systemd is PID 1.
func Available() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	_, err := os.Stat("/run/systemd/system")
	return err == nil
}

// UnitContent renders the unit for this binary. Nightjar runs as root; a unit for another user
// drops NoNewPrivileges and RestrictSUIDSGID, since that user reaches root through sudo, which
// they turn off.
func UnitContent(binary, workingDir, user, envFile string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[Unit]
Description=Nightjar - System resource and uptime monitoring with Discord alerts
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
`)
	// Root is the default. Naming it anyway costs CAP_SETUID: systemd 255 starts a service with
	// User=root, NoNewPrivileges and a sandbox option such as ProtectKernelModules without it, and
	// apt and git as a checkout's owner then cannot switch users.
	asRoot := user == "" || user == "root"
	if !asRoot {
		fmt.Fprintf(&b, "User=%s\nGroup=%s\n", user, user)
	}
	fmt.Fprintf(&b, `WorkingDirectory=%s
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
`, workingDir)
	if envFile != "" {
		if _, err := os.Stat(envFile); err == nil {
			fmt.Fprintf(&b, "EnvironmentFile=%s\n", envFile)
		}
	}
	// Stopping waits up to 3s for the API, a minute for a check under way, 5s for the rest, then
	// saves the minute in progress and closes the database; SIGKILL before then loses that minute.
	fmt.Fprintf(&b, `ExecStart=%s
Restart=always
RestartSec=10
TimeoutStopSec=90
KillMode=control-group
KillSignal=SIGTERM
StandardOutput=journal
StandardError=journal

`, binary)
	if asRoot {
		b.WriteString("NoNewPrivileges=true\n")
	}
	b.WriteString("ProtectKernelModules=true\nProtectControlGroups=true\n")
	if asRoot {
		b.WriteString("RestrictSUIDSGID=true\n")
	}
	b.WriteString(`LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
`)
	return b.String()
}

// Install writes /etc/systemd/system/<name>.service and enables it. It needs root.
func Install(name, workingDir, envFile string, force bool) error {
	if !serviceNameRE.MatchString(name) {
		return fmt.Errorf("invalid service name %q", name)
	}
	if os.Geteuid() != 0 {
		return errors.New("installing a systemd service requires root (run with sudo)")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return err
	}
	// Root, even through sudo: updates, the firewall and the app store need it, and the unit's
	// NoNewPrivileges would stop another user getting it with sudo.
	path := filepath.Join("/etc/systemd/system", name+".service")
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	if err := os.WriteFile(path, []byte(UnitContent(binary, workingDir, "root", envFile)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", name + ".service"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
