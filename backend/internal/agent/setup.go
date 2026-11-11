package agent

// What install sets up beyond the agent itself: its port in the host's firewall, and smartmontools for
// drive readings. Both are best effort; install says what it did and what is left to do by hand.

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Firewalls install knows how to open a port in.
const (
	FirewallUFW       = "ufw"
	FirewallFirewalld = "firewalld"
	// FirewallIPTables is plain iptables rules (also nftables through iptables-nft), with no ufw or
	// firewalld managing them.
	FirewallIPTables = "iptables"
)

// run is how setup runs a command: exec, replaced in tests.
var run = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var lookPath = exec.LookPath

// etc is where the saved firewall rules are: /etc, replaced in tests.
var etc = "/etc"

// SetCommands replaces how setup runs and finds commands, and the folder it reads and writes saved
// firewall rules in instead of /etc, for tests; restore puts them back.
func SetCommands(runner func(name string, args ...string) (string, error), finder func(string) (string, error), etcDir string) (restore func()) {
	oldRun, oldLook, oldEtc := run, lookPath, etc
	run, lookPath, etc = runner, finder, etcDir
	return func() { run, lookPath, etc = oldRun, oldLook, oldEtc }
}

// activeFirewall is the firewall in charge on this machine, if install knows it: "" for none.
func activeFirewall() string {
	if _, err := lookPath("ufw"); err == nil {
		if out, err := run("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
			return FirewallUFW
		}
	}
	if _, err := lookPath("firewall-cmd"); err == nil {
		if out, err := run("firewall-cmd", "--state"); err == nil && strings.TrimSpace(out) == "running" {
			return FirewallFirewalld
		}
	}
	// Plain rules only need a hole when they drop or reject what they do not let in.
	if _, err := lookPath("iptables"); err == nil {
		if out, err := run("iptables", "-S", "INPUT"); err == nil && filters(out) {
			return FirewallIPTables
		}
	}
	return ""
}

// filters says whether an iptables chain listing turns traffic away.
func filters(rules string) bool {
	for _, line := range strings.Split(rules, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "-P" && (f[2] == "DROP" || f[2] == "REJECT") {
			return true
		}
		if strings.Contains(line, "-j DROP") || strings.Contains(line, "-j REJECT") {
			return true
		}
	}
	return false
}

// acceptRule is the agent's rule in an iptables chain, tagged so it can be found again.
func acceptRule(port string, tagged bool) []string {
	rule := []string{"INPUT", "-p", "tcp", "--dport", port}
	if tagged {
		rule = append(rule, "-m", "comment", "--comment", "nightjar-agent")
	}
	return append(rule, "-j", "ACCEPT")
}

// openIPTables puts the agent's rule at the top of INPUT for IPv4, and IPv6 where there is one.
func openIPTables(port string) error {
	var first error
	for i, tool := range []string{"iptables", "ip6tables"} {
		if _, err := lookPath(tool); err != nil {
			continue
		}
		if _, err := run(tool, append([]string{"-C"}, acceptRule(port, true)...)...); err == nil {
			continue // already there
		}
		_, err := run(tool, append([]string{"-I"}, acceptRule(port, true)...)...)
		if err != nil {
			// Some kernels lack the comment match.
			_, err = run(tool, append([]string{"-I"}, acceptRule(port, false)...)...)
		}
		if err != nil && i == 0 {
			first = err
		}
	}
	return first
}

func closeIPTables(port string) {
	for _, tool := range []string{"iptables", "ip6tables"} {
		if _, err := lookPath(tool); err != nil {
			continue
		}
		for _, tagged := range []bool{true, false} {
			for {
				if _, err := run(tool, append([]string{"-D"}, acceptRule(port, tagged)...)...); err != nil {
					break
				}
			}
		}
	}
}

// saveIPTables keeps the rules over a reboot the way this system does: netfilter-persistent (added
// on Debian and Ubuntu when missing), or the rules file a RHEL-like system reads at boot. What it keeps
// leaves out Nightjar's own chains and the jumps to them: the agent puts those back as it starts, and
// kept they would come back at boot without it, live attack blocks and a default drop included.
func saveIPTables(out io.Writer) {
	if _, err := lookPath("netfilter-persistent"); err != nil {
		if _, err := lookPath("apt-get"); err == nil && !otherFirewallTool() {
			os.Setenv("DEBIAN_FRONTEND", "noninteractive")
			// --no-remove: should it conflict with something installed, nothing is taken away for it.
			run("apt-get", "install", "-y", "-q", "--no-remove", "iptables-persistent")
		}
	}
	if _, err := lookPath("netfilter-persistent"); err == nil {
		if _, err := run("netfilter-persistent", "save"); err == nil {
			for _, name := range []string{"rules.v4", "rules.v6"} {
				stripNightjar(filepath.Join(etc, "iptables", name))
			}
			return
		}
	}
	if _, err := os.Stat(filepath.Join(etc, "sysconfig")); err == nil {
		if rules, err := run("iptables-save"); err == nil && rules != "" && os.WriteFile(filepath.Join(etc, "sysconfig", "iptables"), []byte(WithoutNightjar(rules)+"\n"), 0o600) == nil {
			return
		}
	}
	fmt.Fprintln(out, "The firewall rule lasts until the next reboot; to keep it, install iptables-persistent and run: netfilter-persistent save")
}

// otherFirewallTool says whether ufw or firewalld is installed, even switched off: Debian's
// iptables-persistent conflicts with them, and installing it would take them away.
func otherFirewallTool() bool {
	for _, tool := range []string{"ufw", "firewall-cmd"} {
		if _, err := lookPath(tool); err == nil {
			return true
		}
	}
	return false
}

// stripNightjar takes Nightjar's chains out of a saved rules file, keeping its mode.
func stripNightjar(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if kept := WithoutNightjar(string(b)); kept != string(b) {
		mode := os.FileMode(0o640)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		tmp := path + ".nightjar"
		if os.WriteFile(tmp, []byte(kept), mode) == nil {
			os.Rename(tmp, path)
		}
	}
}

// WithoutNightjar is iptables-save output without Nightjar's chains (NIGHTJAR, NIGHTJAR-UDP,
// NIGHTJAR-RULES, NIGHTJAR-OUT), their rules and the rules that jump to them. Nothing else changes:
// Nightjar never changes a chain's policy, so the machine's own rules are left as they were.
func WithoutNightjar(rules string) string {
	var b strings.Builder
	for line := range strings.Lines(rules) {
		f := strings.Fields(line)
		// iptables-save -c puts the counters first: "[12:3456] -A INPUT …".
		if len(f) > 0 && strings.HasPrefix(f[0], "[") {
			f = f[1:]
		}
		if len(f) > 0 && strings.HasPrefix(f[0], ":NIGHTJAR") {
			continue
		}
		if len(f) > 1 && f[0] == "-A" && strings.HasPrefix(f[1], "NIGHTJAR") {
			continue
		}
		if i := slices.IndexFunc(f, func(s string) bool { return s == "-j" || s == "-g" || s == "--jump" || s == "--goto" }); i >= 0 && i+1 < len(f) && strings.HasPrefix(f[i+1], "NIGHTJAR") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// OpenPort lets the agent's port through the active firewall, saying what it did. It returns which
// firewall it changed, so uninstall can close the port again.
func OpenPort(listen string, out io.Writer) string {
	_, port, _ := net.SplitHostPort(listen)
	switch fw := activeFirewall(); fw {
	case FirewallUFW:
		if msg, err := run("ufw", "allow", port+"/tcp", "comment", "nightjar-agent"); err != nil {
			fmt.Fprintf(out, "Could not open port %s in ufw: %s\n", port, msg)
			return ""
		}
		fmt.Fprintf(out, "Opened port %s/tcp in ufw.\n", port)
		return fw
	case FirewallFirewalld:
		if msg, err := run("firewall-cmd", "--permanent", "--add-port="+port+"/tcp"); err != nil {
			fmt.Fprintf(out, "Could not open port %s in firewalld: %s\n", port, msg)
			return ""
		}
		run("firewall-cmd", "--reload")
		fmt.Fprintf(out, "Opened port %s/tcp in firewalld.\n", port)
		return fw
	case FirewallIPTables:
		if err := openIPTables(port); err != nil {
			fmt.Fprintf(out, "Could not open port %s in iptables: %v\n", port, err)
			return ""
		}
		saveIPTables(out)
		fmt.Fprintf(out, "Opened port %s/tcp in iptables.\n", port)
		return fw
	}
	return ""
}

// ClosePort undoes OpenPort.
func ClosePort(fw, listen string, out io.Writer) {
	_, port, _ := net.SplitHostPort(listen)
	switch fw {
	case FirewallUFW:
		if _, err := run("ufw", "delete", "allow", port+"/tcp"); err == nil {
			fmt.Fprintf(out, "Closed port %s/tcp in ufw.\n", port)
		}
	case FirewallIPTables:
		closeIPTables(port)
		saveIPTables(out)
		fmt.Fprintf(out, "Closed port %s/tcp in iptables.\n", port)
	case FirewallFirewalld:
		if _, err := run("firewall-cmd", "--permanent", "--remove-port="+port+"/tcp"); err == nil {
			run("firewall-cmd", "--reload")
			fmt.Fprintf(out, "Closed port %s/tcp in firewalld.\n", port)
		}
	}
}

// installers are how each package manager installs a package without asking; the package's name
// goes last.
var installers = [][]string{
	{"apt-get", "install", "-y", "-q"},
	{"dnf", "install", "-y"},
	{"yum", "install", "-y"},
	{"zypper", "--non-interactive", "install"},
	{"pacman", "-S", "--noconfirm", "--needed"},
	{"apk", "add"},
}

// InstallSmart adds smartmontools, which drive readings need, when smartctl is missing.
func InstallSmart(out io.Writer) {
	installPackage(out, "smartmontools", "smartctl", "drive readings", "For drive temperatures and health, also install smartmontools.")
}

// InstallIPTables adds iptables, which defending against attacks needs, when it is missing.
func InstallIPTables(out io.Writer) {
	installPackage(out, "iptables", "iptables-restore", "defending against attacks", "To defend against attacks, also install iptables.")
}

// installPackage installs pkg with the first package manager there, unless its command is already
// installed; without a package manager it says what to install.
func installPackage(out io.Writer, pkg, command, why, otherwise string) {
	if _, err := lookPath(command); err == nil {
		return
	}
	for _, cmd := range installers {
		if _, err := lookPath(cmd[0]); err != nil {
			continue
		}
		args := append(slices.Clone(cmd[1:]), pkg)
		fmt.Fprintf(out, "Installing %s for %s (%s)…\n", pkg, why, cmd[0])
		os.Setenv("DEBIAN_FRONTEND", "noninteractive")
		msg, err := run(cmd[0], args...)
		if err != nil && cmd[0] == "apt-get" {
			// A fresh machine may not have its package lists yet.
			run("apt-get", "update", "-q")
			msg, err = run(cmd[0], args...)
		}
		if err != nil {
			fmt.Fprintf(out, "Could not install %s: %s\n", pkg, lastLine(msg))
			return
		}
		fmt.Fprintf(out, "Installed %s.\n", pkg)
		return
	}
	fmt.Fprintln(out, otherwise)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
