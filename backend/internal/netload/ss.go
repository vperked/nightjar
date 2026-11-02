package netload

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

var (
	usersRE = regexp.MustCompile(`users:\(\("([^"]*)",pid=(\d+)`)
	countRE = regexp.MustCompile(`\b(bytes_acked|bytes_received):(\d+)`)
)

// ParseSS reads `ss -tinpH state established`: each connection is a line with the queues, both
// addresses and its process, followed by an indented line of TCP details. Connections to this
// machine itself (loopback or its own addresses) never leave it, so they are left out.
func ParseSS(out []byte) []Socket { return parseSS(out, ownAddresses()) }

func parseSS(out []byte, own map[string]bool) []Socket {
	var socks []Socket
	var cur *Socket
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if cur != nil {
				for _, m := range countRE.FindAllStringSubmatch(line, -1) {
					v, _ := strconv.ParseUint(m[2], 10, 64)
					if m[1] == "bytes_acked" {
						cur.Sent = v
					} else {
						cur.Recv = v
					}
				}
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			cur = nil
			continue
		}
		s := Socket{Local: f[2], Peer: f[3]}
		if m := usersRE.FindStringSubmatch(line); m != nil {
			s.Process = m[1]
			s.PID, _ = strconv.Atoi(m[2])
		}
		if local(s.Peer, own) {
			cur = nil
			continue
		}
		socks = append(socks, s)
		cur = &socks[len(socks)-1]
	}
	return socks
}

func loopback(addr string) bool {
	h := hostOf(addr)
	return strings.HasPrefix(h, "127.") || h == "::1"
}

// local reports whether a connection's far end is this machine (one of its own addresses), so its
// traffic never crosses the network.
func local(addr string, own map[string]bool) bool { return loopback(addr) || own[hostOf(addr)] }

// ownAddresses are this machine's IP addresses.
func ownAddresses() map[string]bool {
	own := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return own
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			own[ipn.IP.String()] = true
		}
	}
	return own
}
