package agent

// What install sets up beyond the agent itself: its port in the host's firewall, and smartmontools for
// drive readings. Both are best effort; install says what it did and what is left to do by hand.

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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

// SetCommands replaces how setup runs and finds commands, for tests; restore puts them back.
func SetCommands(runner func(name string, args ...string) (string, error), finder func(string) (string, error)) (restore func()) {
	oldRun, oldLook := run, lookPath
	run, lookPath = runner, finder
	return func() { run, lookPath = oldRun, oldLook }
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
// on Debian and Ubuntu when missing), or the rules file a RHEL-like system reads at boot.
func saveIPTables(out io.Writer) {
	if _, err := lookPath("netfilter-persistent"); err != nil {
		if _, err := lookPath("apt-get"); err == nil {
			os.Setenv("DEBIAN_FRONTEND", "noninteractive")
			run("apt-get", "install", "-y", "-q", "iptables-persistent")
		}
	}
	if _, err := lookPath("netfilter-persistent"); err == nil {
		if _, err := run("netfilter-persistent", "save"); err == nil {
			return
		}
	}
	if _, err := os.Stat("/etc/sysconfig"); err == nil {
		if rules, err := run("iptables-save"); err == nil && rules != "" && os.WriteFile("/etc/sysconfig/iptables", []byte(rules+"\n"), 0o600) == nil {
			return
		}
	}
	fmt.Fprintln(out, "The firewall rule lasts until the next reboot; to keep it, install iptables-persistent and run: netfilter-persistent save")
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
