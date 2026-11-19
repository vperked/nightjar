package firewall

import (
	"bufio"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Service is what holds a port: the program's name, the executable it runs, and the systemd service
// it runs in (empty outside one, or in a scope such as a login session or a container, which come and
// go).
type Service struct {
	Program string `json:"program,omitempty"`
	Exe     string `json:"exe,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

// Name is a service in a word: its systemd service, else its program.
func (s Service) Name() string {
	if s.Unit != "" {
		return strings.TrimSuffix(s.Unit, ".service")
	}
	if s.Program != "" {
		return s.Program
	}
	return filepath.Base(s.Exe)
}

// holder is a process holding a listening socket: its parent too, since a server can hand its
// socket to helpers of its own (Postfix's master to smtpd), and whether it holds the port on loopback
// alone, where the network cannot reach it.
type holder struct {
	pid, ppid int
	local     bool
	Service
}

// listener is a socket listening on a port, from /proc/net.
type listener struct {
	port  uint16
	proto string
	inode string
	local bool
}

func portKey(proto string, port uint16) string { return proto + "/" + strconv.Itoa(int(port)) }

// listeners are the sockets listening on a port, from /proc/net: a port open on both IPv4 and IPv6,
// or on several addresses, has one for each.
func listeners(proc string) []listener {
	var out []listener
	for _, t := range []struct{ file, proto, state string }{{"/net/tcp", "tcp", "0A"}, {"/net/tcp6", "tcp", "0A"}, {"/net/udp", "udp", "07"}, {"/net/udp6", "udp", "07"}} {
		f, err := os.Open(proc + t.file)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[0] == "sl" || fl[3] != t.state {
				continue
			}
			// UDP sockets talking to one peer are not listening.
			if t.proto == "udp" && !strings.HasSuffix(fl[2], ":0000") {
				continue
			}
			addr, port, ok := strings.Cut(fl[1], ":")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(port, 16, 16)
			if err != nil || n == 0 {
				continue
			}
			out = append(out, listener{port: uint16(n), proto: t.proto, inode: fl[9], local: isLoopback(addr)})
		}
		f.Close()
	}
	return out
}

// Listening are the ports something listens on, from /proc/net, with the service holding each.
func Listening(proc string) []Port {
	ports, _ := scan(proc)
	return ports
}

// scan reads the ports something listens on, and every process holding each, by "tcp/22".
func scan(proc string) ([]Port, map[string][]holder) {
	ls := listeners(proc)
	inodes := map[string]bool{}
	for _, l := range ls {
		inodes[l.inode] = true
	}
	byInode := socketHolders(proc, inodes)
	holders := map[string][]holder{}
	seen := map[string]int{}
	var out []Port
	for _, l := range ls {
		key := portKey(l.proto, l.port)
		for _, h := range byInode[l.inode] {
			// A process is local to the port only if every socket it holds there is on loopback.
			if i := slices.IndexFunc(holders[key], func(o holder) bool { return o.pid == h.pid }); i >= 0 {
				holders[key][i].local = holders[key][i].local && l.local
			} else {
				h.local = l.local
				holders[key] = append(holders[key], h)
			}
		}
		if i, ok := seen[key]; ok {
			// Open on another address too: it is only local if it is local everywhere.
			out[i].Local = out[i].Local && l.local
			continue
		}
		seen[key] = len(out)
		out = append(out, Port{Port: l.port, Proto: l.proto, Local: l.local})
	}
	for i := range out {
		if hs := holders[portKey(out[i].Proto, out[i].Port)]; len(hs) > 0 {
			// The service itself: not systemd holding the socket for it, nor a helper it handed it to.
			h := hs[0]
			if roots := roots(hs); len(roots) > 0 {
				h = roots[0]
			}
			out[i].Program, out[i].Exe, out[i].Unit = h.Program, h.Exe, h.Unit
		}
	}
	slices.SortFunc(out, func(a, b Port) int {
		if a.Port != b.Port {
			return int(a.Port) - int(b.Port)
		}
		return strings.Compare(a.Proto, b.Proto)
	})
	if out == nil {
		out = []Port{}
	}
	return out, holders
}

// isLoopback reads a /proc/net address in hex: 127.x.x.x, or ::1.
func isLoopback(hexAddr string) bool {
	b, err := hex.DecodeString(hexAddr)
	if err != nil {
		return false
	}
	switch len(b) {
	case 4:
		return b[3] == 127 // little-endian
	case 16:
		return hexAddr == "00000000000000000000000001000000"
	}
	return false
}

// socketHolders maps the socket inodes asked for to the processes holding each, in the order of their
// PIDs. Only the processes holding one are read further.
func socketHolders(proc string, inodes map[string]bool) map[string][]holder {
	out := map[string][]holder{}
	if len(inodes) == 0 {
		return out
	}
	dirs, _ := os.ReadDir(proc)
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(proc, d.Name())
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		var h *holder
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if !inodes[inode] {
				continue
			}
			if h == nil {
				h = &holder{pid: pid, ppid: readPPid(dir), Service: readService(dir)}
			}
			if !slices.ContainsFunc(out[inode], func(o holder) bool { return o.pid == pid }) {
				out[inode] = append(out[inode], *h)
			}
		}
	}
	return out
}

// roots are the processes holding a port that are neither systemd (PID 1), nor gone (a process
// exiting has no executable left to read), nor started by another of them: the service, not the
// helpers it handed its socket to.
func roots(hs []holder) []holder {
	pids := map[int]bool{}
	for _, h := range hs {
		// systemd starting a service does not make the service one of its helpers.
		if h.pid != 1 {
			pids[h.pid] = true
		}
	}
	var out []holder
	for _, h := range hs {
		if h.pid != 1 && h.Exe != "" && !pids[h.ppid] {
			out = append(out, h)
		}
	}
	return out
}

// readPPid reads a process's parent from its /proc/<pid>/status; 0 when it cannot be read.
func readPPid(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "status"))
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "PPid:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// readService reads what a process (its /proc directory) is: its name, executable and systemd
// service.
func readService(dir string) Service {
	b, _ := os.ReadFile(filepath.Join(dir, "comm"))
	s := Service{Program: strings.TrimSpace(string(b))}
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		// A program replaced on disk while it runs, by an upgrade, is still the same program.
		s.Exe = strings.TrimSuffix(exe, " (deleted)")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
		s.Unit = unitOf(string(b))
	}
	return s
}

// unitOf reads the systemd service a process runs in from its /proc/<pid>/cgroup: the innermost
// ".service" on its path ("0::/system.slice/nginx.service" is nginx.service). Scopes are left out:
// a login session, or a container, is a new one each time.
func unitOf(cgroup string) string {
	path := ""
	for line := range strings.Lines(cgroup) {
		line = strings.TrimSpace(line)
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			path = p
			break
		}
		if _, p, ok := strings.Cut(line, ":name=systemd:"); ok {
			path = p
		}
	}
	parts := strings.Split(path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.HasSuffix(parts[i], ".service") {
			return parts[i]
		}
	}
	return ""
}
