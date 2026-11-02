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

// UnitContent renders the unit for this binary.
func UnitContent(binary, workingDir, user, envFile string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[Unit]
Description=Server Monitor - System resource and uptime monitoring with Discord alerts
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
`, user, user, workingDir)
	if envFile != "" {
		if _, err := os.Stat(envFile); err == nil {
			fmt.Fprintf(&b, "EnvironmentFile=%s\n", envFile)
		}
	}
	fmt.Fprintf(&b, `ExecStart=%s
Restart=always
RestartSec=10
TimeoutStopSec=20
KillMode=control-group
KillSignal=SIGTERM
StandardOutput=journal
StandardError=journal

NoNewPrivileges=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
`, binary)
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
	user := os.Getenv("SUDO_USER")
	if user == "" {
		user = "root"
	}
	path := filepath.Join("/etc/systemd/system", name+".service")
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	if err := os.WriteFile(path, []byte(UnitContent(binary, workingDir, user, envFile)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", name + ".service"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
