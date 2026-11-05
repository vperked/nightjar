// Package netload is a live network meter for the dashboard: every interface's rates each second,
// with peaks, averages and totals, and which programs and remote hosts the TCP traffic belongs to.
// It samples only while someone is watching, then stops.
package netload

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// historyLen is five minutes of one-second samples.
	historyLen = 300
	// idleAfter stops sampling once nobody has asked for a while.
	idleAfter = 45 * time.Second
	topN      = 12
)

// Sample is one second of traffic on an interface, in bytes per second.
type Sample struct {
	TS float64 `json:"ts"`
	RX float64 `json:"rx"`
	TX float64 `json:"tx"`
}

// Interface is one network interface's live state.
type Interface struct {
	Name    string `json:"name"`
	Up      bool   `json:"up"`
	Default bool   `json:"default"` // carries the default route
	Virtual bool   `json:"virtual"` // loopback, bridges, containers, tunnels
	Speed   int    `json:"speed_mbps,omitempty"`

	RXRate    float64 `json:"rx_bps"`
	TXRate    float64 `json:"tx_bps"`
	RXPackets float64 `json:"rx_pps"`
	TXPackets float64 `json:"tx_pps"`
	RXPeak    float64 `json:"rx_peak"`
	TXPeak    float64 `json:"tx_peak"`
	RXAvg     float64 `json:"rx_avg"`
	TXAvg     float64 `json:"tx_avg"`
	RXTotal   uint64  `json:"rx_total"` // since boot
	TXTotal   uint64  `json:"tx_total"`
	Errors    uint64  `json:"errors"` // since boot, both directions
	Drops     uint64  `json:"drops"`
	// NewErrors and NewDrops happened while watching.
	NewErrors uint64 `json:"new_errors,omitempty"`
	NewDrops  uint64 `json:"new_drops,omitempty"`
}

// Talker is a program's share of the TCP traffic.
type Talker struct {
	Name        string  `json:"name"`
	Unit        string  `json:"unit,omitempty"` // the systemd service it runs under
	PIDs        []int   `json:"pids,omitempty"`
	Connections int     `json:"connections"`
	RX          float64 `json:"rx_bps"`
	TX          float64 `json:"tx_bps"`
	Source      string  `json:"source,omitempty"` // "tcp", or where else the numbers came from
}

// Host is a remote address's share of the TCP traffic.
type Host struct {
	Peer        string  `json:"peer"`
	Process     string  `json:"process,omitempty"`
	Connections int     `json:"connections"`
	RX          float64 `json:"rx_bps"`
	TX          float64 `json:"tx_bps"`
}

// View is the meter's state for the dashboard.
type View struct {
	Interfaces  []Interface         `json:"interfaces"`
	Samples     map[string][]Sample `json:"samples"`
	Processes   []Talker            `json:"processes"`
	Hosts       []Host              `json:"hosts"`
	Connections int                 `json:"connections"` // established TCP connections off this machine
	Since       float64             `json:"since"`       // when the meter started this time
}

// Counters are one interface's cumulative kernel counters.
type Counters struct {
	RXBytes, TXBytes, RXPackets, TXPackets, Errors, Drops uint64
}

// Socket is one established TCP connection with its byte counters.
type Socket struct {
	Local, Peer string
	Process     string
	PID         int
	Sent, Recv  uint64
}

// Meter samples while watched. The readers are fields so tests can stand in for the machine.
type Meter struct {
	ReadDev     func() (map[string]Counters, error)
	ReadSockets func(ctx context.Context) ([]Socket, error)
	// Identify names a process fully (the kernel cuts names at 15 characters) and finds its service.
	Identify  func(pid int, comm string) (name, unit string)
	Describe  func(name string) (up, virtual bool, speed int)
	DefaultIf func() string
	Tick      time.Duration

	mu       sync.Mutex
	watched  time.Time
	running  bool
	since    float64
	prev     map[string]Counters
	start    map[string]Counters
	prevAt   time.Time
	ifaces   map[string]*ifaceState
	sockPrev map[string]Socket
	sockAt   time.Time
	talkers  []Talker
	hosts    []Host
	conns    int
	names    map[int][2]string
}

type ifaceState struct {
	info    Interface
	samples []Sample
}

func New() *Meter {
	return &Meter{ReadDev: readProcNetDev, ReadSockets: readSockets, Identify: identify, Describe: describe, DefaultIf: defaultInterface, Tick: time.Second}
}

// Watch keeps the meter sampling (starting it if needed) for a while longer. Starting, it reads the
// interfaces at once, so the request that woke it already has them to show rather than none.
func (m *Meter) Watch() {
	m.mu.Lock()
	m.watched = time.Now()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.since = float64(time.Now().Unix())
	m.prev, m.start, m.ifaces, m.sockPrev = nil, nil, map[string]*ifaceState{}, nil
	m.talkers, m.hosts, m.conns, m.names = nil, nil, 0, map[int][2]string{}
	m.mu.Unlock()
	m.sample(time.Now(), false)
	go m.loop()
}

func (m *Meter) loop() {
	t := time.NewTicker(m.Tick)
	defer t.Stop()
	for n := 1; ; n++ {
		now := <-t.C
		m.mu.Lock()
		idle := now.Sub(m.watched) > idleAfter
		if idle {
			m.running = false
		}
		m.mu.Unlock()
		if idle {
			return
		}
		// Sockets every other second, from the first: per-program numbers need less resolution than the graph.
		m.sample(now, n%2 == 1)
	}
}

// Sample takes one reading; the loop calls it each tick, tests call it directly.
func (m *Meter) Sample(now time.Time, sockets bool) { m.sample(now, sockets) }

func (m *Meter) sample(now time.Time, sockets bool) {
	dev, err := m.ReadDev()
	if err == nil {
		m.addCounters(now, dev)
	}
	if !sockets || m.ReadSockets == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if socks, err := m.ReadSockets(ctx); err == nil {
		m.addSockets(now, socks)
	}
}

func (m *Meter) addCounters(now time.Time, dev map[string]Counters) {
	def := m.DefaultIf()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ifaces == nil {
		m.ifaces = map[string]*ifaceState{}
	}
	if m.start == nil {
		m.start = dev
	}
	elapsed := now.Sub(m.prevAt).Seconds()
	for name, c := range dev {
		st := m.ifaces[name]
		if st == nil {
			st = &ifaceState{}
			m.ifaces[name] = st
		}
		up, virtual, speed := m.Describe(name)
		in := &st.info
		in.Name, in.Up, in.Virtual, in.Speed, in.Default = name, up, virtual, speed, name == def
		in.RXTotal, in.TXTotal, in.Errors, in.Drops = c.RXBytes, c.TXBytes, c.Errors, c.Drops
		if s, ok := m.start[name]; ok {
			in.NewErrors, in.NewDrops = sub(c.Errors, s.Errors), sub(c.Drops, s.Drops)
		}
		p, ok := m.prev[name]
		if !ok || elapsed <= 0 {
			continue
		}
		in.RXRate = float64(sub(c.RXBytes, p.RXBytes)) / elapsed
		in.TXRate = float64(sub(c.TXBytes, p.TXBytes)) / elapsed
		in.RXPackets = float64(sub(c.RXPackets, p.RXPackets)) / elapsed
		in.TXPackets = float64(sub(c.TXPackets, p.TXPackets)) / elapsed
		st.samples = append(st.samples, Sample{TS: float64(now.UnixMilli()) / 1000, RX: in.RXRate, TX: in.TXRate})
		if over := len(st.samples) - historyLen; over > 0 {
			st.samples = slices.Delete(st.samples, 0, over)
		}
		in.RXPeak, in.TXPeak, in.RXAvg, in.TXAvg = 0, 0, 0, 0
		for _, s := range st.samples {
			in.RXPeak, in.TXPeak = max(in.RXPeak, s.RX), max(in.TXPeak, s.TX)
			in.RXAvg += s.RX / float64(len(st.samples))
			in.TXAvg += s.TX / float64(len(st.samples))
		}
	}
	for name := range m.ifaces {
		if _, ok := dev[name]; !ok {
			delete(m.ifaces, name)
		}
	}
	m.prev, m.prevAt = dev, now
}

// sub is a counter delta that survives a counter reset.
func sub(now, before uint64) uint64 {
	if now < before {
		return 0
	}
	return now - before
}

func (m *Meter) addSockets(now time.Time, socks []Socket) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := make(map[string]Socket, len(socks))
	for _, s := range socks {
		cur[s.Local+">"+s.Peer] = s
	}
	// The first reading has no baseline, so it counts connections but no traffic.
	elapsed := now.Sub(m.sockAt).Seconds()
	measure := m.sockPrev != nil && elapsed > 0
	byProc := map[string]*Talker{}
	byHost := map[string]*Host{}
	for key, s := range cur {
		name, unit := m.identify(s)
		t := byProc[name]
		if t == nil {
			t = &Talker{Name: name, Unit: unit, Source: "tcp"}
			byProc[name] = t
		}
		t.Connections++
		if s.PID > 0 && !slices.Contains(t.PIDs, s.PID) {
			t.PIDs = append(t.PIDs, s.PID)
		}
		peer := hostOf(s.Peer)
		h := byHost[peer+"\x00"+name]
		if h == nil {
			h = &Host{Peer: peer, Process: name}
			byHost[peer+"\x00"+name] = h
		}
		h.Connections++
		if measure {
			rx, tx := rates(s, m.sockPrev[key], elapsed)
			t.RX, t.TX = t.RX+rx, t.TX+tx
			h.RX, h.TX = h.RX+rx, h.TX+tx
		}
	}
	for _, t := range byProc {
		slices.Sort(t.PIDs)
	}
	m.talkers = busiestFirst(byProc, func(t Talker) (float64, int, string) { return t.RX + t.TX, t.Connections, t.Name })
	m.hosts = busiestFirst(byHost, func(h Host) (float64, int, string) { return h.RX + h.TX, h.Connections, h.Peer })
	m.hosts = m.hosts[:min(len(m.hosts), topN)]
	m.conns = len(socks)
	m.sockPrev, m.sockAt = cur, now
}

// identify names a connection's program, asking the machine once per process.
func (m *Meter) identify(s Socket) (name, unit string) {
	name = cmp.Or(s.Process, "unknown")
	if s.PID <= 0 || m.Identify == nil {
		return name, ""
	}
	if m.names == nil {
		m.names = map[int][2]string{}
	}
	id, ok := m.names[s.PID]
	if !ok {
		n, u := m.Identify(s.PID, s.Process)
		id = [2]string{cmp.Or(n, name), u}
		m.names[s.PID] = id
	}
	return id[0], id[1]
}

// rates is a connection's traffic since the last reading; a connection new since then counts from zero.
func rates(s, prev Socket, elapsed float64) (rx, tx float64) {
	return float64(sub(s.Recv, prev.Recv)) / elapsed, float64(sub(s.Sent, prev.Sent)) / elapsed
}

// busiestFirst orders rows by traffic, then connections, then name.
func busiestFirst[T any](rows map[string]*T, key func(T) (float64, int, string)) []T {
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b T) int {
		at, an, as := key(a)
		bt, bn, bs := key(b)
		return cmp.Or(cmp.Compare(bt, at), cmp.Compare(bn, an), strings.Compare(as, bs))
	})
	return out
}

// hostOf is an address without its port, IPv4-mapped IPv6 (::ffff:1.2.3.4) written as plain IPv4.
func hostOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	addr = strings.Trim(addr, "[]")
	return strings.TrimPrefix(addr, "::ffff:")
}

// orEmpty is a copy of a list, empty rather than nil.
func orEmpty[T any](s []T) []T {
	return append(make([]T, 0, len(s)), s...)
}

// View returns the state, with each interface's samples newer than since.
func (m *Meter) View(since float64) View {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Lists are never null, so the dashboard can go through them: an interface with nothing newer
	// than since has no samples, not null ones.
	v := View{Interfaces: []Interface{}, Samples: map[string][]Sample{}, Processes: orEmpty(m.talkers), Hosts: orEmpty(m.hosts), Connections: m.conns, Since: m.since}
	for _, st := range m.ifaces {
		v.Interfaces = append(v.Interfaces, st.info)
		i, _ := slices.BinarySearchFunc(st.samples, since, func(s Sample, t float64) int {
			if s.TS <= t {
				return -1
			}
			return 1
		})
		v.Samples[st.info.Name] = orEmpty(st.samples[i:])
	}
	// The default route first, then real interfaces before virtual ones, then by name.
	slices.SortFunc(v.Interfaces, func(a, b Interface) int {
		rank := func(i Interface) int {
			switch {
			case i.Default:
				return 0
			case !i.Virtual && i.Up:
				return 1
			case !i.Virtual:
				return 2
			}
			return 3
		}
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	if v.Processes == nil {
		v.Processes = []Talker{}
	}
	if v.Hosts == nil {
		v.Hosts = []Host{}
	}
	if v.Interfaces == nil {
		v.Interfaces = []Interface{}
	}
	return v
}

// ---------- reading the machine ----------

// readProcNetDev parses /proc/net/dev.
func readProcNetDev() (map[string]Counters, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	return ParseProcNetDev(data), nil
}

// ParseProcNetDev reads the kernel's interface table: receive bytes, packets, errs, drop … then the
// same for transmit.
func ParseProcNetDev(data []byte) map[string]Counters {
	out := map[string]Counters{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		n := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		out[strings.TrimSpace(name)] = Counters{RXBytes: n(0), RXPackets: n(1), TXBytes: n(8), TXPackets: n(9), Errors: n(2) + n(10), Drops: n(3) + n(11)}
	}
	return out
}

func describe(name string) (up, virtual bool, speed int) {
	base := filepath.Join("/sys/class/net", name)
	state, _ := os.ReadFile(filepath.Join(base, "operstate"))
	s := strings.TrimSpace(string(state))
	// Loopback and many tunnels report "unknown"; they are up whenever they exist.
	up = s == "up" || s == "unknown"
	if _, err := os.Stat(filepath.Join(base, "device")); err != nil {
		virtual = true
	}
	if raw, err := os.ReadFile(filepath.Join(base, "speed")); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && v > 0 {
			speed = v
		}
	}
	return up, virtual, speed
}

// defaultInterface is the interface of the IPv4 default route.
func defaultInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		if f := strings.Fields(line); len(f) > 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// identify names a process by its command (the kernel's own name stops at 15 characters) and finds
// the systemd service it belongs to from its cgroup.
func identify(pid int, comm string) (name, unit string) {
	name = comm
	if len(comm) >= 15 {
		if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline")); err == nil {
			argv0, _, _ := strings.Cut(string(raw), "\x00")
			if base := filepath.Base(argv0); base != "" && base != "." && base != "/" {
				name = base
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup")); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if i := strings.LastIndex(line, "/"); i >= 0 && strings.HasSuffix(line, ".service") {
				unit = strings.TrimSuffix(line[i+1:], ".service")
				break
			}
		}
	}
	return name, unit
}

// readSockets lists established TCP connections off this machine with their byte counters.
func readSockets(ctx context.Context) ([]Socket, error) {
	out, err := exec.CommandContext(ctx, "ss", "-tinpH", "state", "established").Output()
	if err != nil {
		return nil, err
	}
	return ParseSS(out), nil
}
