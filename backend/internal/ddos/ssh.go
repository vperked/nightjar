package ddos

// SSH as it is now, read afresh each time: the ports sshd listens on, and who has logged in. A TCP
// connection to sshd is not a login (anyone can open one and sit on it before the password), so a
// login is a connection held by one of the processes sshd starts for a session once it lets someone
// in, which it names "sshd: user@pts/0" or "sshd: user@notty" ("sshd-session: …" from OpenSSH 9.8).

import (
	"bufio"
	"errors"
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
	base := filepath.Dir(path)
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
				// Relative names are in the folder of the main config, /etc/ssh, even inside an
				// included file, as sshd reads them.
				for _, pattern := range args {
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(base, pattern)
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

// sshdDirs are the folders a packaged sshd runs from: sshd itself in sbin or bin, and OpenSSH 9.8's
// sshd-session (and 10's sshd-auth) where each distribution keeps its helpers.
var sshdDirs = []string{
	"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/sbin", "/usr/local/bin",
	"/usr/lib/openssh", "/usr/libexec/openssh", "/usr/lib/ssh", "/usr/libexec", "/usr/local/libexec",
	"/usr/lib64/misc", "/usr/lib/misc",
}

// sshdBinary says whether a program path is sshd's own: a process can call itself anything, in its
// name and its command line, but not change the program it runs. A program replaced by an upgrade
// since it started reads "(deleted)", and is still sshd.
func sshdBinary(exe string) bool {
	exe = strings.TrimSuffix(exe, " (deleted)")
	dir, name := filepath.Split(exe)
	if name != "sshd" && name != "sshd-session" && name != "sshd-auth" {
		return false
	}
	dir = strings.TrimSuffix(dir, "/")
	// NixOS keeps it in the store, which only root writes to.
	if strings.HasPrefix(dir, "/nix/store/") && strings.Count(dir, "/") == 4 && strings.Contains(dir, "-openssh-") &&
		(strings.HasSuffix(dir, "/bin") || strings.HasSuffix(dir, "/libexec")) {
		return true
	}
	return slices.Contains(sshdDirs, dir)
}

// sshdProcess is what is known of one process that may be sshd's.
type sshdProcess struct {
	sshd bool // it runs sshd's program
	root bool // its real user is root
	ppid string
}

// readSSHDProcess reads a process's program, user and parent from its folder in /proc. A process
// whose program cannot be read (it can always be by root, which Nightjar runs as) is judged by its
// name instead, still only trusted when root runs it, or its parent for a session.
func readSSHDProcess(dir string) sshdProcess {
	var p sshdProcess
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		p.sshd = sshdBinary(exe)
	} else if errors.Is(err, os.ErrPermission) {
		comm, _ := os.ReadFile(filepath.Join(dir, "comm"))
		name := strings.TrimSpace(string(comm))
		p.sshd = name == "sshd" || name == "sshd-session"
	}
	status, _ := os.ReadFile(filepath.Join(dir, "status"))
	for line := range strings.Lines(string(status)) {
		key, value, _ := strings.Cut(line, ":")
		f := strings.Fields(value)
		switch {
		case key == "Uid" && len(f) > 0:
			p.root = f[0] == "0"
		case key == "PPid" && len(f) > 0:
			p.ppid = f[0]
		}
	}
	return p
}

// sessionTitle is the name sshd gives a session's process once it has let someone in. Before that
// its processes are "[listener]", "[accepted]", "user [priv]", "user [net]" or "[preauth]".
var sessionTitle = regexp.MustCompile(`^sshd(-session)?: [^\s@\[\]]+@\S+$`)

// sshdSockets reads sshd's processes: the ports sshd listens on (those configured, with those its
// listening sockets are on; 22 when none says), and the addresses of the connections its logged-in
// sessions hold to one of them.
func sshdSockets(proc string) (ports []uint16, logins []Login) {
	s := scanSSHD(proc)
	if s.holds() {
		for _, p := range []string{"/net/tcp", "/net/tcp6"} {
			f, err := os.Open(proc + p)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			var fl []string
			for n := 0; sc.Scan() && n < maxSockets; n++ {
				fl = fields(sc.Text(), fl)
				s.add(fl)
			}
			f.Close()
		}
	}
	return s.result()
}

// sshdScan is what sshd's processes hold: the sockets of its logged-in sessions and of its other
// processes (by inode), with the ports its config names, and the connections to them found so far
// in the connection tables.
//
// Only sshd's own processes count: any user can start a program named sshd, with a title like a
// session's, and must not get its port kept open under a default drop, or its address spared from
// blocks. A listener must run sshd's program as root. A session runs sshd's program too, as root or
// as the user it let in (OpenSSH's privilege separation), and then its parent is sshd's root process
// for that connection: a user cannot make that parent theirs.
type sshdScan struct {
	ports            []uint16
	sessions, others map[string]bool
	held             []Login
}

// scanSSHD reads sshd's processes from where /proc is.
func scanSSHD(proc string) *sshdScan {
	s := &sshdScan{ports: configuredSSHPorts(), sessions: map[string]bool{}, others: map[string]bool{}}
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
		p := readSSHDProcess(dir)
		if !p.sshd {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		title := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		into := s.others
		if sessionTitle.MatchString(title) {
			into = s.sessions
			if !p.root {
				parent := readSSHDProcess(filepath.Join(proc, p.ppid))
				if p.ppid == "" || !parent.sshd || !parent.root {
					continue
				}
			}
		} else if !p.root {
			continue
		}
		fds, _ := os.ReadDir(filepath.Join(dir, "fd"))
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err == nil && strings.HasPrefix(link, "socket:[") {
				into[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
			}
		}
	}
	return s
}

// holds says whether sshd holds any socket to look for in the connection tables.
func (s *sshdScan) holds() bool { return len(s.sessions) > 0 || len(s.others) > 0 }

// add takes in the fields of one line of /proc/net/tcp or tcp6.
func (s *sshdScan) add(fl []string) {
	if len(fl) < 10 || fl[0] == "sl" {
		return
	}
	switch {
	case fl[3] == "0A":
		if port := localPort(fl[1]); s.others[fl[9]] && port != 0 {
			s.ports = append(s.ports, port)
		}
	case fl[3] == "01" && s.sessions[fl[9]]:
		if ip := hexAddr(fl[2]); ip != nil {
			s.held = append(s.held, Login{Address: ip.String(), Port: localPort(fl[1])})
		}
	}
}

// result is the ports sshd listens on, and its logins. A session also holds the connections it
// forwards, and with ssh -R or X11 forwarding those others make to a port it listens on itself: its
// login is the one to sshd's own port.
func (s *sshdScan) result() (ports []uint16, logins []Login) {
	ports = sshdPorts(slices.Clone(s.ports))
	if !s.holds() {
		return ports, nil
	}
	for _, l := range s.held {
		if slices.Contains(ports, l.Port) && !slices.Contains(logins, l) {
			logins = append(logins, l)
		}
	}
	return ports, logins
}

// fields is strings.Fields for the connection tables' ASCII lines, into buf's room, so a table of
// many lines is split without a new slice for each.
func fields(line string, buf []string) []string {
	buf = buf[:0]
	for i := 0; i < len(line); {
		for i < len(line) && asciiSpace(line[i]) {
			i++
		}
		j := i
		for j < len(line) && !asciiSpace(line[j]) {
			j++
		}
		if j > i {
			buf = append(buf, line[i:j])
		}
		i = j
	}
	return buf
}

func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// sshdPorts are ports sorted without repeats, 22 when there are none.
func sshdPorts(ports []uint16) []uint16 {
	if len(ports) == 0 {
		return []uint16{22}
	}
	slices.Sort(ports)
	return slices.Compact(ports)
}
