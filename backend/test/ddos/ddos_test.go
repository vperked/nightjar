package ddos_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/ddos"
)

func TestTheKernelsCountersAndTablesAreRead(t *testing.T) {
	snmp := "Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors\nUdp: 167395433 16736 346930 265397770 346921\nIcmp: InMsgs InEchos\nIcmp: 32267 1675\n"
	m := ddos.ParseSNMP([]byte(snmp))
	if m["Udp.InDatagrams"] != 167395433 || m["Udp.NoPorts"] != 16736 || m["Icmp.InEchos"] != 1675 {
		t.Fatalf("snmp: %v", m)
	}
	dev := "Inter-| Receive\n face |bytes packets\n  eth0: 5000 50 0 0 0 0 0 0 9000 90\n    lo: 1 1\n"
	if p, b, ok := ddos.ParseDev([]byte(dev), "eth0"); !ok || p != 50 || b != 5000 {
		t.Fatalf("dev: %d %d %v", p, b, ok)
	}
	// 1.2.3.4 established twice to the web server, 5.6.7.8 half-open, localhost ignored, and this
	// machine's own connection out to 13.12.11.10 not counted: nothing listens on the port it is
	// from. Addresses are little-endian words.
	tcp := `  sl  local_address rem_address   st
   0: 0100007F:1F90 0100007F:D431 01 00000000:00000000
   1: 1100A8C0:01BB 04030201:C350 01 00000000:00000000
   2: 1100A8C0:01BB 04030201:C351 01 00000000:00000000
   3: 1100A8C0:01BB 08070605:C352 03 00000000:00000000
   4: 1100A8C0:0016 08070605:C353 0A 00000000:00000000
   5: 1100A8C0:D431 0A0B0C0D:01BB 01 00000000:00000000
   6: 00000000:01BB 00000000:0000 0A 00000000:00000000
`
	tbl := ddos.Table{BySource: map[string]*ddos.Peer{}}
	ddos.ParseTCP(strings.NewReader(tcp), &tbl)
	if tbl.Established != 2 || tbl.Web != 2 || tbl.SynRecv != 1 || tbl.BySource["1.2.3.4"].Established != 2 || tbl.BySource["5.6.7.8"].SynRecv != 1 || tbl.BySource["127.0.0.1"] != nil || tbl.BySource["13.12.11.10"] != nil {
		t.Fatalf("table: %+v %v", tbl, tbl.BySource)
	}
	// A connection open to sshd is not a login: none is marked as one.
	for addr, p := range tbl.BySource {
		if p.SSH {
			t.Fatalf("%s marked as logged in over SSH", addr)
		}
	}
}

func TestOnlyThoseSSHLetInAreLoggedIn(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	row := func(local, remote, state string, inode int) string {
		return fmt.Sprintf("   0: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0 20 4 30 10 -1\n", local, remote, state, inode)
	}
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		row("00000000:08AE", "00000000:0000", "0A", 10) + // sshd listening on 2222
		row("1100A8C0:08AE", "C86433C6:C350", "01", 11) + // logged in from 198.51.100.200
		row("1100A8C0:08AE", "077100CB:C351", "01", 12) + // 203.0.113.7, still before the password
		row("00000000:177A", "00000000:0000", "0A", 13) + // the session's own X11 or ssh -R listener on 6010
		row("1100A8C0:177A", "090071CB:C352", "01", 14) // 203.0.113.9 connected to that forward, not a login
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644)
	proc := func(pid int, title string, inode int) {
		p := filepath.Join(dir, fmt.Sprint(pid))
		os.MkdirAll(filepath.Join(p, "fd"), 0o755)
		os.WriteFile(filepath.Join(p, "comm"), []byte("sshd\n"), 0o644)
		os.WriteFile(filepath.Join(p, "cmdline"), []byte(title+"\x00"), 0o644)
		os.WriteFile(filepath.Join(p, "status"), []byte("PPid:\t1\nUid:\t0\t0\t0\t0\n"), 0o644)
		os.Symlink("/usr/sbin/sshd", filepath.Join(p, "exe"))
		os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(p, "fd", "3"))
	}
	proc(1, "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", 10)
	proc(2, "sshd: admin@pts/0", 11)
	proc(6, "sshd: admin@pts/0", 13)
	proc(7, "sshd: admin@pts/0", 14)
	proc(3, "sshd: admin [priv]", 11)
	proc(4, "sshd: root [priv]", 12)
	proc(5, "sshd: unknown [net]", 12)
	if got := fmt.Sprint(ddos.SSHLogins(dir)); got != "[{198.51.100.200 2222}]" {
		t.Fatalf("logins: %s", got)
	}
	if ports := ddos.SSHPorts(dir); !slices.Contains(ports, 2222) {
		t.Fatalf("sshd's listening port: %v", ports)
	}
}

// machine fakes the counters a watcher reads, advancing them by rates each step.
type machine struct {
	c   ddos.Counters
	now time.Time
}

func (m *machine) step(w *ddos.Watcher, rates ddos.Counters, table ddos.Table, requests int, busy ddos.Busy) {
	m.now = m.now.Add(5 * time.Second)
	m.c.SynCookies += rates.SynCookies * 5
	m.c.ListenDrops += rates.ListenDrops * 5
	m.c.UDPIn += rates.UDPIn * 5
	m.c.UDPNoPorts += rates.UDPNoPorts * 5
	m.c.ICMPEchos += rates.ICMPEchos * 5
	m.c.RxPackets += rates.RxPackets * 5
	m.c.RxBytes += rates.RxBytes * 5
	if table.BySource == nil {
		table.BySource = map[string]*ddos.Peer{}
	}
	w.Step(m.now, m.c, table, requests*5, 1e9, busy)
}

var normal = ddos.Counters{UDPIn: 400, ICMPEchos: 1, RxPackets: 3000, RxBytes: 2e6}

func calmTable() ddos.Table {
	return ddos.Table{Established: 60, BySource: map[string]*ddos.Peer{"203.0.113.9": {Established: 4}}}
}

func TestEachMethodIsNamedAndBusyMachinesAreNotAttacks(t *testing.T) {
	cases := []struct {
		name   string
		rates  ddos.Counters
		table  ddos.Table
		req    int
		busy   ddos.Busy
		method string
		expect bool
	}{
		{"SYN flood", ddos.Counters{SynCookies: 4000, RxPackets: 90000, RxBytes: 5e6}, ddos.Table{SynRecv: 2000}, 0, ddos.Busy{}, ddos.SYNFlood, false},
		{"UDP flood at closed ports", ddos.Counters{UDPIn: 400000, UDPNoPorts: 390000, RxPackets: 400000, RxBytes: 6e7}, ddos.Table{}, 0, ddos.Busy{}, ddos.UDPFlood, false},
		{"ping flood", ddos.Counters{ICMPEchos: 50000, RxPackets: 50000, RxBytes: 4e6}, ddos.Table{}, 0, ddos.Busy{}, ddos.ICMPFlood, false},
		{"one address holding hundreds of connections", ddos.Counters{RxPackets: 5000, RxBytes: 2e6}, ddos.Table{Established: 900, BySource: map[string]*ddos.Peer{"198.51.100.7": {Established: 800}}}, 0, ddos.Busy{}, ddos.ConnectionFlood, false},
		{"HTTP flood", ddos.Counters{RxPackets: 20000, RxBytes: 5e6}, ddos.Table{}, 2500, ddos.Busy{}, ddos.HTTPFlood, false},
		{"the line full", ddos.Counters{RxPackets: 800000, RxBytes: 120e6}, ddos.Table{}, 0, ddos.Busy{}, ddos.Volumetric, false},
		{"the line full while apt installs", ddos.Counters{RxPackets: 80000, RxBytes: 120e6}, ddos.Table{}, 0, ddos.Busy{Installing: "apt-get"}, ddos.Volumetric, true},
		{"torrents over uTP", ddos.Counters{UDPIn: 90000, UDPNoPorts: 20, RxPackets: 90000, RxBytes: 9e7}, ddos.Table{}, 0, ddos.Busy{Torrents: "qbittorrent-nox"}, ddos.UDPFlood, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &ddos.Watcher{}
			m := &machine{now: time.Unix(1_800_000_000, 0)}
			for range 20 {
				m.step(w, normal, calmTable(), 2, ddos.Busy{})
			}
			if v := w.View(); len(v.Findings) != 0 || v.Learning {
				t.Fatalf("a normal day: %+v", v)
			}
			m.step(w, c.rates, c.table, c.req, c.busy)
			v := w.View()
			var got *ddos.Finding
			for i := range v.Findings {
				if v.Findings[i].Method == c.method {
					got = &v.Findings[i]
				}
			}
			if got == nil {
				t.Fatalf("%s not seen: %+v", c.method, v.Findings)
			}
			if (got.Expected != "") != c.expect {
				t.Fatalf("expected %v: %+v", c.expect, got)
			}
			if c.expect && ddos.Attacking(v.Findings) {
				t.Fatalf("explained traffic counted as an attack: %+v", v.Findings)
			}
		})
	}
}

func TestAnAttackIsRecordedFromStartToEnd(t *testing.T) {
	var mu sync.Mutex
	var started, ended []ddos.Attack
	w := &ddos.Watcher{
		OnStart: func(a ddos.Attack) { mu.Lock(); started = append(started, a); mu.Unlock() },
		OnEnd:   func(a ddos.Attack) { mu.Lock(); ended = append(ended, a); mu.Unlock() },
	}
	m := &machine{now: time.Unix(1_800_000_000, 0)}
	for range 20 {
		m.step(w, normal, calmTable(), 2, ddos.Busy{})
	}
	flood := ddos.Table{SynRecv: 3000, BySource: map[string]*ddos.Peer{
		"198.51.100.7": {SynRecv: 900}, "198.51.100.8": {SynRecv: 700}, "104.16.1.1": {Established: 40},
	}}
	// One spike is not an attack; two in a row are.
	m.step(w, ddos.Counters{SynCookies: 5000, RxPackets: 120000, RxBytes: 9e6}, flood, 0, ddos.Busy{})
	if w.View().Attacking {
		t.Fatal("one spike called an attack")
	}
	m.step(w, ddos.Counters{SynCookies: 9000, RxPackets: 200000, RxBytes: 14e6}, flood, 0, ddos.Busy{})
	m.step(w, ddos.Counters{SynCookies: 7000, UDPIn: 500000, UDPNoPorts: 480000, RxPackets: 600000, RxBytes: 70e6}, flood, 0, ddos.Busy{})
	v := w.View()
	if !v.Attacking || v.Current == nil {
		t.Fatalf("under attack: %+v", v)
	}
	for range 6 {
		m.step(w, normal, calmTable(), 2, ddos.Busy{})
	}
	v = w.View()
	if v.Attacking || len(v.History) != 1 {
		t.Fatalf("over after half a minute of calm: %+v", v)
	}
	a := v.History[0]
	if a.End == 0 || ddos.Describe(a) != "SYN flood and UDP flood" || a.Methods[0].Value != 9000 || a.PeakRx != 70e6*8 {
		t.Fatalf("record: %+v (%s)", a, ddos.Describe(a))
	}
	if a.Sources[0].Address != "198.51.100.7" || a.Sources[0].SynRecv != 900 {
		t.Fatalf("sources: %+v", a.Sources)
	}
	for _, s := range a.Sources {
		if s.Address == "104.16.1.1" && s.Network != "Cloudflare" {
			t.Fatalf("Cloudflare's address is named, not blamed: %+v", s)
		}
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 1 || len(ended) != 1 || ended[0].ID != started[0].ID {
		t.Fatalf("told once at the start and once at the end: %d %d", len(started), len(ended))
	}
}

func TestWebRequestsAreCountedFromTheLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	line := func(ip string) string {
		return ip + ` - - [05/Nov/2026:00:00:00 +0000] "GET / HTTP/1.1" 200 12 "-" "curl"` + "\n"
	}
	os.WriteFile(path, []byte(line("203.0.113.1")), 0o644)
	tail := &ddos.LogTail{Path: path}
	by := map[string]*ddos.Peer{}
	if n, _ := tail.Requests(by, nil); n != 0 {
		t.Fatalf("what was there before is not counted: %d", n)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("198.51.100.7") + line("198.51.100.7") + line("127.0.0.1") + "198.51.100.9 - - [half a line")
	f.Close()
	web := ddos.NewWebStats()
	if n, _ := tail.Requests(by, web); n != 3 || by["198.51.100.7"].Requests != 2 || by["127.0.0.1"] != nil || web.ByRequest["GET /"] != 3 || web.Statuses[200] != 3 || web.Agents["curl"] != 3 {
		t.Fatalf("new lines: %d %v %+v", n, by, web)
	}
	// Rotated: a new file in its place is read from its start.
	os.Rename(path, path+".1")
	os.WriteFile(path, []byte(line("192.0.2.5")), 0o644)
	by = map[string]*ddos.Peer{}
	if n, _ := tail.Requests(by, nil); n != 1 || by["192.0.2.5"] == nil {
		t.Fatalf("after rotation: %d %v", n, by)
	}
}

func TestInABigFloodEachAddressKeepsItsShareOfTheRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	os.WriteFile(path, nil, 0o644)
	tail := &ddos.LogTail{Path: path}
	tail.Requests(map[string]*ddos.Peer{}, nil)
	// 24 MiB in one interval, three times what one read parses: ten addresses with a tenth each.
	var b strings.Builder
	for i := 0; b.Len() < 24<<20; i++ {
		fmt.Fprintf(&b, "198.51.100.%d - - [05/Nov/2026:00:00:00 +0000] \"GET / HTTP/1.1\" 200 12 \"-\" \"bot\"\n", 10+i%10)
	}
	os.WriteFile(path, []byte(b.String()), 0o644)
	by := map[string]*ddos.Peer{}
	n, _ := tail.Requests(by, nil)
	for i := 10; i < 20; i++ {
		got := by[fmt.Sprintf("198.51.100.%d", i)].Requests
		if share := float64(got) / float64(n); share < 0.095 || share > 0.105 {
			t.Fatalf("198.51.100.%d: %d of %d requests, a share of %.3f", i, got, n, share)
		}
	}
}
