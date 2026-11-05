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

// Listening are the ports something listens on, from /proc/net, with the program holding each.
func Listening(proc string) []Port {
	owners := socketOwners(proc)
	seen := map[string]bool{}
	var out []Port
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
			local := isLoopback(addr)
			key := t.proto + "/" + port
			if seen[key] {
				// Open on another address too: it is only local if it is local everywhere.
				if !local {
					for i := range out {
						if out[i].Proto == t.proto && out[i].Port == uint16(n) {
							out[i].Local = false
						}
					}
				}
				continue
			}
			seen[key] = true
			out = append(out, Port{Port: uint16(n), Proto: t.proto, Program: owners[fl[9]], Local: local})
		}
		f.Close()
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
	return out
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

// socketOwners maps socket inodes to the name of the program holding each.
func socketOwners(proc string) map[string]string {
	out := map[string]string{}
	dirs, _ := os.ReadDir(proc)
	for _, d := range dirs {
		if _, err := strconv.Atoi(d.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(proc, d.Name(), "fd"))
		if err != nil {
			continue
		}
		name := ""
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(proc, d.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			if name == "" {
				b, _ := os.ReadFile(filepath.Join(proc, d.Name(), "comm"))
				name = strings.TrimSpace(string(b))
			}
			out[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = name
		}
	}
	return out
}
