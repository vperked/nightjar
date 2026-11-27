package ddos

// What the detector reads, all from the kernel's own counters and tables, so nothing is captured and
// nothing needs installing: /proc/net/snmp and /proc/net/netstat (TCP, UDP and ICMP totals),
// /proc/net/dev (the interface's packets and bytes), /proc/net/tcp and tcp6 (who is connected), and
// nginx's access log (web requests, for floods aimed at the sites).

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Counters are the kernel's running totals; the detector turns them into rates.
type Counters struct {
	SynCookies   uint64 // SYNs answered with a cookie because the queue was full: a SYN flood's mark
	ListenDrops  uint64 // connection attempts dropped at a full listen queue
	UDPIn        uint64
	UDPNoPorts   uint64 // UDP packets to ports nothing listens on
	UDPRcvbufErr uint64 // UDP packets dropped for want of buffer
	ICMPEchos    uint64 // pings received
	RxPackets    uint64
	RxBytes      uint64
}

// ParseSNMP reads /proc/net/snmp or /proc/net/netstat, which pair a header line with a values line
// for each protocol, into one map ("Udp.InDatagrams" → 167395433).
func ParseSNMP(data []byte) map[string]uint64 {
	out := map[string]uint64{}
	lines := strings.Split(string(data), "\n")
	for i := 0; i+1 < len(lines); i++ {
		head, vals := strings.Fields(lines[i]), strings.Fields(lines[i+1])
		if len(head) < 2 || len(head) != len(vals) || head[0] != vals[0] {
			continue
		}
		proto := strings.TrimSuffix(head[0], ":")
		for j := 1; j < len(head); j++ {
			if v, err := strconv.ParseUint(vals[j], 10, 64); err == nil {
				out[proto+"."+head[j]] = v
			}
		}
		i++
	}
	return out
}

// ParseDev reads one interface's received packets and bytes from /proc/net/dev.
func ParseDev(data []byte, iface string) (packets, bytes uint64, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 2 {
			return 0, 0, false
		}
		b, err1 := strconv.ParseUint(f[0], 10, 64)
		p, err2 := strconv.ParseUint(f[1], 10, 64)
		return p, b, err1 == nil && err2 == nil
	}
	return 0, 0, false
}

func readCounters(iface string) Counters {
	var c Counters
	if b, err := os.ReadFile("/proc/net/snmp"); err == nil {
		m := ParseSNMP(b)
		c.UDPIn, c.UDPNoPorts, c.UDPRcvbufErr, c.ICMPEchos = m["Udp.InDatagrams"], m["Udp.NoPorts"], m["Udp.RcvbufErrors"], m["Icmp.InEchos"]
	}
	if b, err := os.ReadFile("/proc/net/netstat"); err == nil {
		m := ParseSNMP(b)
		c.SynCookies, c.ListenDrops = m["TcpExt.SyncookiesSent"], m["TcpExt.ListenDrops"]
	}
	if b, err := os.ReadFile("/proc/net/dev"); err == nil {
		c.RxPackets, c.RxBytes, _ = ParseDev(b, iface)
	}
	return c
}

// Table is who is connected now: half-open (SYN-RECV) and established TCP connections, by address.
type Table struct {
	SynRecv     int
	Established int
	BySource    map[string]*Peer
	// Web is the established connections to the web ports (80 and 443): many of them making few
	// requests is a slow-request attack.
	Web int
	// listening are the local ports with a listening socket, which tell connections made to this
	// machine from those it made itself.
	listening map[uint16]bool
}

// Peer is one remote address's connections.
type Peer struct {
	Established int
	SynRecv     int
	Requests    int  // web requests in the last interval, from nginx's log
	SSH         bool // logged in over SSH
}

// maxSockets caps how many lines of the connection tables one sample reads: a flood can make them
// enormous, and a count of the first part tells the story.
const maxSockets = 200_000

// ParseTCP adds /proc/net/tcp or tcp6 to t. Only connections from elsewhere to a port this machine
// listens on count: this machine talking to itself is not an attack, and neither are the connections
// it opens itself, to a proxied app or a storage server. A half-open connection is always one made to
// it.
func ParseTCP(r io.Reader, t *Table) { parseTCP(r, t, nil) }

// parseTCP is ParseTCP that also hands sshd each line, when it holds sockets, so one read of the
// table serves both: the kernel builds it afresh at each read, which is most of what reading it costs.
func parseTCP(r io.Reader, t *Table, sshd *sshdScan) {
	if t.listening == nil {
		t.listening = map[uint16]bool{}
	}
	if t.BySource == nil {
		t.BySource = map[string]*Peer{}
	}
	// The kernel lists the listening sockets first, but a connection whose port is not known to be
	// listening yet waits for the end of the table to be sure.
	type pending struct {
		key  string
		port uint16
	}
	var later []pending
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	forSSHD := sshd != nil && sshd.holds()
	var f []string
	// Each reader stops at its own maxSockets: this one counts the table's connections, sshd's
	// every line.
	n := 0
	for line := 0; sc.Scan() && (n < maxSockets || forSSHD && line < maxSockets); line++ {
		f = fields(sc.Text(), f)
		if forSSHD && line < maxSockets {
			sshd.add(f)
		}
		if n >= maxSockets || len(f) < 4 || f[0] == "sl" {
			continue
		}
		n++
		state := f[3]
		if state == "0A" { // LISTEN
			t.listening[localPort(f[1])] = true
			continue
		}
		if state != "01" && state != "03" { // ESTABLISHED, SYN_RECV
			continue
		}
		ip, ok := decodeHexAddr(f[2])
		if !ok || ip.IsLoopback() {
			continue
		}
		key := ip.String()
		if state == "03" {
			t.SynRecv++
			t.peer(key).SynRecv++
			continue
		}
		port := localPort(f[1])
		if !t.listening[port] {
			later = append(later, pending{key, port})
			continue
		}
		t.established(key, port)
	}
	for _, p := range later {
		if t.listening[p.port] {
			t.established(p.key, p.port)
		}
	}
}

func (t *Table) peer(key string) *Peer {
	p := t.BySource[key]
	if p == nil {
		p = &Peer{}
		t.BySource[key] = p
	}
	return p
}

func (t *Table) established(key string, port uint16) {
	t.Established++
	t.peer(key).Established++
	if port == 80 || port == 443 {
		t.Web++
	}
}

// localPort is the port of a /proc/net/tcp local address ("0100007F:1F90" → 8080).
func localPort(s string) uint16 {
	_, h, ok := strings.Cut(s, ":")
	if !ok {
		return 0
	}
	v, err := strconv.ParseUint(h, 16, 16)
	if err != nil {
		return 0
	}
	return uint16(v)
}

// hexAddr decodes a /proc/net/tcp address ("0100007F:1F90"): the kernel writes it as 32-bit words in
// the machine's byte order.
func hexAddr(s string) net.IP {
	ip, ok := decodeHexAddr(s)
	if !ok {
		return nil
	}
	return ip.AsSlice()
}

// decodeHexAddr is hexAddr without allocating, for reading every line of a connection table. An
// IPv4-mapped address comes back as IPv4.
func decodeHexAddr(s string) (netip.Addr, bool) {
	h, _, ok := strings.Cut(s, ":")
	if !ok || (len(h) != 8 && len(h) != 32) {
		return netip.Addr{}, false
	}
	var b [16]byte
	for i := 0; i < len(h); i += 2 {
		hi, ok1 := unhex(h[i])
		lo, ok2 := unhex(h[i+1])
		if !ok1 || !ok2 {
			return netip.Addr{}, false
		}
		b[i/2] = hi<<4 | lo
	}
	for w := 0; w < len(h)/2; w += 4 {
		b[w], b[w+1], b[w+2], b[w+3] = b[w+3], b[w+2], b[w+1], b[w]
	}
	if len(h) == 8 {
		return netip.AddrFrom4([4]byte(b[:4])), true
	}
	return netip.AddrFrom16(b).Unmap(), true
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// ParseUDPPorts reads the local ports of the sockets in a /proc/net/udp table that take packets from
// anyone: connected sockets only hear from their peer, whose replies the firewall already knows.
func ParseUDPPorts(r io.Reader, ports map[uint16]bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 0; sc.Scan() && n < maxSockets; n++ {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[0] == "sl" || localPort(f[2]) != 0 {
			continue
		}
		if port := localPort(f[1]); port != 0 {
			ports[port] = true
		}
	}
}

// openUDP are the local UDP ports with a socket open, smallest first.
func openUDP(proc string) []uint16 {
	ports := map[uint16]bool{}
	for _, p := range []string{"/net/udp", "/net/udp6"} {
		if f, err := os.Open(proc + p); err == nil {
			ParseUDPPorts(f, ports)
			f.Close()
		}
	}
	out := make([]uint16, 0, len(ports))
	for p := range ports {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// tracking says whether the kernel tracks connections, and whether SYN cookies are off.
func tracking(proc string) (conntrack, synCookiesOff bool) {
	_, err := os.Stat(proc + "/sys/net/netfilter/nf_conntrack_count")
	b, _ := os.ReadFile(proc + "/sys/net/ipv4/tcp_syncookies")
	return err == nil, strings.TrimSpace(string(b)) == "0"
}

func readTable(proc string) Table {
	t := Table{BySource: map[string]*Peer{}}
	sshd := scanSSHD(proc)
	for _, p := range []string{"/net/tcp", "/net/tcp6"} {
		if f, err := os.Open(proc + p); err == nil {
			parseTCP(f, &t, sshd)
			f.Close()
		}
	}
	_, logins := sshd.result()
	for _, l := range logins {
		t.peer(l.Address).SSH = true
	}
	return t
}

// resolvConfs are where the name servers this machine asks are listed: systemd-resolved's own
// upstream servers are in the second.
var resolvConfs = []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"}

// upstream are the addresses this machine relies on to reach anything: its name servers and its
// default gateways. Blocking one would cut it off, and their address is easy to forge.
func upstream(proc string) []string {
	var out []string
	for _, path := range resolvConfs {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(b)) {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "nameserver" {
				addr, _, _ := strings.Cut(f[1], "%")
				if ip := net.ParseIP(addr); ip != nil && !ip.IsLoopback() {
					out = append(out, ip.String())
				}
			}
		}
	}
	if b, err := os.ReadFile(proc + "/net/route"); err == nil {
		for line := range strings.Lines(string(b)) {
			// Iface Destination Gateway …, in hex.
			if f := strings.Fields(line); len(f) > 2 && f[1] == "00000000" && f[2] != "00000000" {
				if ip := hexAddr(f[2] + ":0"); ip != nil {
					out = append(out, ip.String())
				}
			}
		}
	}
	if b, err := os.ReadFile(proc + "/net/ipv6_route"); err == nil {
		for line := range strings.Lines(string(b)) {
			// Destination, its prefix length, source, its prefix length, next hop, …
			f := strings.Fields(line)
			if len(f) < 5 || f[1] != "00" || strings.Trim(f[0], "0") != "" || strings.Trim(f[4], "0") == "" {
				continue
			}
			if b, err := hex.DecodeString(f[4]); err == nil && len(b) == 16 {
				out = append(out, net.IP(b).String())
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// LogTail follows a log file from where it was last read; a rotated or truncated file starts again.
type LogTail struct {
	Path   string
	offset int64
	inode  uint64
}

// maxLogBytes is the most one sample reads of the web log; beyond it lines are counted, not parsed.
const maxLogBytes = 8 << 20

// WebStats are what the web requests in an interval asked for and got, to tell kinds of HTTP flood
// apart.
type WebStats struct {
	Requests  int
	ByRequest map[string]int // "GET /login"
	Queries   map[string]int // distinct query strings seen, by path (counted up to a limit)
	Statuses  map[int]int
	Agents    map[string]int // user agents ("" for none)
	queries   map[string]bool
}

// NewWebStats is an empty set.
func NewWebStats() *WebStats {
	return &WebStats{ByRequest: map[string]int{}, Queries: map[string]int{}, Statuses: map[int]int{}, Agents: map[string]int{}, queries: map[string]bool{}}
}

// maxQueries caps how many distinct query strings are remembered in one interval.
const maxQueries = 20000

// add takes in one log line of nginx's combined format:
// 1.2.3.4 - - [time] "GET /path?q HTTP/1.1" 200 123 "referer" "agent"
func (w *WebStats) add(line []byte) {
	w.Requests++
	parts := bytes.Split(line, []byte(`"`))
	if len(parts) < 3 {
		return
	}
	f := strings.Fields(string(parts[1]))
	if len(f) >= 2 && len(f[0]) <= 10 {
		path, query, _ := strings.Cut(f[1], "?")
		if len(path) > 120 {
			path = path[:120]
		}
		w.ByRequest[f[0]+" "+path]++
		if query != "" && len(w.queries) < maxQueries && !w.queries[path+"?"+query] {
			w.queries[path+"?"+query] = true
			w.Queries[path]++
		}
	} else {
		// A request that never finished arriving ("-", or nothing): nginx logs it with 400 or 408.
		w.ByRequest["- -"]++
	}
	if st := strings.Fields(string(parts[2])); len(st) > 0 {
		if code, err := strconv.Atoi(st[0]); err == nil {
			w.Statuses[code]++
		}
	}
	if len(parts) >= 6 {
		agent := strings.TrimSpace(string(parts[5]))
		if agent == "-" {
			agent = ""
		}
		if len(agent) > 120 {
			agent = agent[:120]
		}
		w.Agents[agent]++
	}
}

// Requests counts the web requests logged since the last call, by the address that made them
// (nginx's combined format starts each line with it), and adds them to web when it is not nil. The
// first call only finds the end.
func (l *LogTail) Requests(bySource map[string]*Peer, web *WebStats) (int, bool) {
	f, err := os.Open(l.Path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, false
	}
	ino := inode(st)
	if l.inode == 0 {
		l.inode, l.offset = ino, st.Size()
		return 0, false
	}
	if ino != l.inode || st.Size() < l.offset {
		l.inode, l.offset = ino, 0
	}
	size := st.Size() - l.offset
	if size <= 0 {
		return 0, true
	}
	buf := make([]byte, min(size, maxLogBytes))
	n, _ := f.ReadAt(buf, l.offset)
	buf = buf[:n]
	// Only whole lines; the rest waits for the next read.
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
	} else {
		return 0, true
	}
	count := 0
	mine := map[string]int{} // this log's requests, by address
	for _, line := range bytes.Split(bytes.TrimSuffix(buf, []byte("\n")), []byte("\n")) {
		count++
		addr, _, _ := bytes.Cut(line, []byte(" "))
		if ip := net.ParseIP(string(addr)); ip != nil && !ip.IsLoopback() {
			mine[ip.String()]++
		}
		if web != nil {
			web.add(line)
		}
	}
	l.offset += int64(len(buf))
	scale := 1.0
	if size > maxLogBytes {
		// More than one read can parse: the rest is taken to be like what was read, for the total and
		// for each address alike, so their shares stay true; and the read moves on to the end.
		scale = float64(size) / float64(len(buf))
		count = int(float64(count) * scale)
		l.offset = st.Size()
	}
	for addr, n := range mine {
		p := bySource[addr]
		if p == nil {
			p = &Peer{}
			bySource[addr] = p
		}
		p.Requests += int(float64(n) * scale)
	}
	return count, true
}
