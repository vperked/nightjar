package ddos

// SSH as it is now, read afresh each time: the ports sshd listens on, and who has logged in. A TCP
// connection to sshd is not a login (anyone can open one and sit on it before the password), so a
// login is a connection held by one of the processes sshd starts for a session once it lets someone
// in, which it names "sshd: user@pts/0" or "sshd: user@notty" ("sshd-session: …" from OpenSSH 9.8).

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Login is an address logged in over SSH, and the port it came in on.
type Login struct {
	Address string
	Port    uint16
}

// sshConfig is sshd's main config file; the files it includes are found from it.
const sshConfig = "/etc/ssh/sshd_config"

// sshSocketUnits are where systemd's ssh.socket, when it listens for sshd, says on which ports: the
// drop-ins Ubuntu generates from sshd's config, and those written by hand.
var sshSocketUnits = []string{"/etc/systemd/system/ssh.socket", "/etc/systemd/system/ssh.socket.d/*.conf", "/run/systemd/generator/ssh.socket.d/*.conf"}

// SSHPorts are the ports sshd listens on here, read now from where /proc is: those its config names
// (with the files it includes and ListenAddress's ports), those systemd listens on for it, and those
// its processes hold open. 22 when none says.
func SSHPorts(proc string) []uint16 {
	ports, _ := sshdSockets(proc)
	return ports
}

// configuredSSHPorts are the ports sshd's config names and those systemd listens on for it.
func configuredSSHPorts() []uint16 {
	ports := SSHConfigPorts(sshConfig)
	for _, pattern := range sshSocketUnits {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			ports = append(ports, socketUnitPorts(f)...)
		}
	}
	return ports
}

// SSHLogins are the addresses logged in over SSH now, from where /proc is.
func SSHLogins(proc string) []Login {
	_, logins := sshdSockets(proc)
	return logins
}

// SSHConfigPorts are the ports an sshd config file names, with the files it includes: each Port (22
// when none does), and the port of each ListenAddress that gives one; nil when it cannot be read.
func SSHConfigPorts(path string) []uint16 {
	var ports, listenPorts []uint16
	bare := false // a ListenAddress without a port, which takes Port's
	listens := false
	var read func(path string, depth int) bool
	read = func(path string, depth int) bool {
		b, err := os.ReadFile(path)
		if err != nil || depth > 16 {
			return false
		}
		for line := range strings.Lines(string(b)) {
			key, args := configLine(line)
			if len(args) == 0 {
				continue
			}
			switch strings.ToLower(key) {
			case "port":
				if n, err := strconv.ParseUint(args[0], 10, 16); err == nil && n > 0 {
					ports = append(ports, uint16(n))
				}
			case "listenaddress":
				listens = true
				if p := addressPort(args[0]); p != 0 {
					listenPorts = append(listenPorts, p)
				} else {
					bare = true
				}
			case "include":
				// Relative names are in the folder of the main config, /etc/ssh.
				for _, pattern := range args {
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(filepath.Dir(path), pattern)
					}
					files, _ := filepath.Glob(pattern)
					slices.Sort(files)
					for _, f := range files {
						read(f, depth+1)
					}
				}
			}
		}
		return true
	}
	if !read(path, 0) {
		return nil
	}
	if len(ports) == 0 {
		ports = []uint16{22}
	}
	if listens && !bare {
		// Every ListenAddress names its port: Port is not used.
		return listenPorts
	}
	return append(ports, listenPorts...)
}

// configLine splits an sshd config line into its keyword and arguments ("Port 2222", "Port=2222"),
// leaving out a comment after them.
func configLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, nil
	}
	key, rest := line[:i], strings.TrimLeft(line[i:], " \t")
	rest = strings.TrimPrefix(rest, "=")
	var args []string
	for _, f := range strings.Fields(rest) {
		if strings.HasPrefix(f, "#") {
			break
		}
		args = append(args, strings.Trim(f, `"`))
	}
	return key, args
}

// addressPort is the port of "host:port", "[v6]:port" or ":port"; 0 for an address alone.
func addressPort(s string) uint16 {
	var p string
	if strings.HasPrefix(s, "[") {
		_, after, ok := strings.Cut(s, "]")
		if !ok || !strings.HasPrefix(after, ":") {
			return 0
		}
		p = after[1:]
	} else if strings.Count(s, ":") == 1 {
		_, p, _ = strings.Cut(s, ":")
	} else {
		return 0
	}
	n, err := strconv.ParseUint(p, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// socketUnitPorts are the ports a systemd socket unit's ListenStream lines listen on; an empty one
// forgets those before it.
func socketUnitPorts(path string) []uint16 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ports []uint16
	for line := range strings.Lines(string(b)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "ListenStream" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			ports = nil
			continue
		}
		if n, err := strconv.ParseUint(value, 10, 16); err == nil {
			ports = append(ports, uint16(n))
		} else if p := addressPort(value); p != 0 {
			ports = append(ports, p)
		} else if i := strings.LastIndexByte(value, ':'); i >= 0 {
			if n, err := strconv.ParseUint(value[i+1:], 10, 16); err == nil {
				ports = append(ports, uint16(n))
			}
		}
	}
	return ports
}

// sessionTitle is the name sshd gives a session's process once it has let someone in. Before that
// its processes are "[listener]", "[accepted]", "user [priv]", "user [net]" or "[preauth]".
var sessionTitle = regexp.MustCompile(`^sshd(-session)?: [^\s@\[\]]+@\S+$`)

// sshdSockets reads sshd's processes: the ports sshd listens on (those configured, with those its
// listening sockets are on; 22 when none says), and the addresses of the connections its logged-in
// sessions hold to one of them.
func sshdSockets(proc string) (ports []uint16, logins []Login) {
	ports = configuredSSHPorts()
	sessions, others := map[string]bool{}, map[string]bool{}
	dirs, _ := os.ReadDir(proc)
	for _, d := range dirs {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		dir := filepath.Join(proc, d.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if name := strings.TrimSpace(string(comm)); err != nil || (name != "sshd" && name != "sshd-session") {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		title := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		into := others
		if sessionTitle.MatchString(title) {
			into = sessions
		}
		fds, _ := os.ReadDir(filepath.Join(dir, "fd"))
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err == nil && strings.HasPrefix(link, "socket:[") {
				into[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
			}
		}
	}
	if len(sessions) == 0 && len(others) == 0 {
		return sshdPorts(ports), nil
	}
	// A session also holds the connections it forwards, and with ssh -R or X11 forwarding those
	// others make to a port it listens on itself: its login is the one to sshd's own port.
	var held []Login
	for _, p := range []string{"/net/tcp", "/net/tcp6"} {
		f, err := os.Open(proc + p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for n := 0; sc.Scan() && n < maxSockets; n++ {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[0] == "sl" {
				continue
			}
			switch {
			case fl[3] == "0A":
				if port := localPort(fl[1]); others[fl[9]] && port != 0 {
					ports = append(ports, port)
				}
			case fl[3] == "01" && sessions[fl[9]]:
				if ip := hexAddr(fl[2]); ip != nil {
					held = append(held, Login{Address: ip.String(), Port: localPort(fl[1])})
				}
			}
		}
		f.Close()
	}
	ports = sshdPorts(ports)
	for _, l := range held {
		if slices.Contains(ports, l.Port) && !slices.Contains(logins, l) {
			logins = append(logins, l)
		}
	}
	return ports, logins
}

// sshdPorts are ports sorted without repeats, 22 when there are none.
func sshdPorts(ports []uint16) []uint16 {
	if len(ports) == 0 {
		return []uint16{22}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}
