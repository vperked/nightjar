package ddos_test

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nightjar/internal/ddos"
)

// frame builds an Ethernet frame carrying an IPv4 packet: proto 6 (TCP, with flags), 17 (UDP, from
// srcPort to dstPort) or 1 (ICMP of type icmp). fragment sets the more-fragments bit.
func frame(src [4]byte, proto byte, srcPort, dstPort uint16, flags, icmp byte, fragment bool) []byte {
	f := make([]byte, 14+20+20)
	binary.BigEndian.PutUint16(f[12:], 0x0800)
	ip := f[14:]
	ip[0] = 0x45
	ip[9] = proto
	copy(ip[12:16], src[:])
	if fragment {
		binary.BigEndian.PutUint16(ip[6:], 0x2000)
	}
	l4 := ip[20:]
	switch proto {
	case 6:
		binary.BigEndian.PutUint16(l4[0:], srcPort)
		binary.BigEndian.PutUint16(l4[2:], dstPort)
		l4[13] = flags
	case 17:
		binary.BigEndian.PutUint16(l4[0:], srcPort)
		binary.BigEndian.PutUint16(l4[2:], dstPort)
	case 1:
		l4[0] = icmp
	}
	return f
}

const (
	syn    = 0x02
	synAck = 0x12
	ack    = 0x10
)

func kindsOf(add func(p *ddos.Profile)) []ddos.Kind {
	p := &ddos.Profile{TCPFlags: map[string]int{}, UDPSrc: map[uint16]int{}, UDPDst: map[uint16]int{}, ICMPTypes: map[uint8]int{}, Sources: map[string]int{}}
	add(p)
	return p.Kinds()
}

func names(ks []ddos.Kind) string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Name+" "+k.Detail)
	}
	return strings.Join(out, " | ")
}

func TestTheExactKindOfFloodIsNamedFromItsPackets(t *testing.T) {
	cases := []struct {
		name string
		add  func(p *ddos.Profile)
		want string // the first kind, with its detail
	}{
		{"SYN flood", func(p *ddos.Profile) {
			for i := range 900 {
				p.Add(frame([4]byte{45, 142, 120, byte(i)}, 6, uint16(1024+i), 443, syn, 0, false))
			}
			for range 100 {
				p.Add(frame([4]byte{203, 0, 113, 9}, 6, 50000, 443, ack, 0, false))
			}
		}, "SYN flood "},
		{"DNS amplification", func(p *ddos.Profile) {
			for i := range 800 {
				p.Add(frame([4]byte{8, 8, byte(i), 1}, 17, 53, uint16(30000+i), 0, 0, i%5 == 0))
			}
		}, "DNS amplification from port 53"},
		{"SYN-ACK reflection", func(p *ddos.Profile) {
			for i := range 800 {
				p.Add(frame([4]byte{93, 184, byte(i), 34}, 6, 80, uint16(40000+i), synAck, 0, false))
			}
		}, "SYN-ACK reflection "},
		{"ACK flood", func(p *ddos.Profile) {
			for i := range 800 {
				p.Add(frame([4]byte{185, 220, 101, byte(i)}, 6, uint16(i), 22, ack, 0, false))
			}
		}, "ACK flood "},
		{"random-port UDP", func(p *ddos.Profile) {
			for i := range 1000 {
				p.Add(frame([4]byte{91, 92, byte(i), 17}, 17, uint16(5000+i), uint16(1000+i*7), 0, 0, false))
			}
		}, "UDP flood (random ports) "},
		{"UDP on one port", func(p *ddos.Profile) {
			for i := range 1000 {
				p.Add(frame([4]byte{91, 92, byte(i), 17}, 17, uint16(5000+i), 27015, 0, 0, false))
			}
		}, "UDP flood on one port on port 27015"},
		{"ping flood", func(p *ddos.Profile) {
			for i := range 800 {
				p.Add(frame([4]byte{198, 51, 100, byte(i)}, 1, 0, 0, 0, 8, false))
			}
		}, "Ping flood "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ks := kindsOf(c.add)
			if len(ks) == 0 || ks[0].Name+" "+ks[0].Detail != c.want {
				t.Fatalf("kinds: %s", names(ks))
			}
			if ks[0].How == "" {
				t.Fatalf("says how it works: %+v", ks[0])
			}
		})
	}
	// Fragments are a kind of their own when they are a large share.
	ks := kindsOf(func(p *ddos.Profile) {
		for i := range 4000 {
			p.Add(frame([4]byte{8, 8, 4, 4}, 17, 53, uint16(30000+i%30000), 0, 0, i%5 == 0))
		}
	})
	if !strings.Contains(names(ks), "IP fragment flood") {
		t.Fatalf("a fifth fragments: %s", names(ks))
	}
}

func TestQuietTrafficIsNotCalledAFlood(t *testing.T) {
	// A normal second: a few dozen acknowledgements and some torrent UDP.
	ks := kindsOf(func(p *ddos.Profile) {
		for range 53 {
			p.Add(frame([4]byte{203, 0, 113, 9}, 6, 50000, 443, ack, 0, false))
		}
		for range 16 {
			p.Add(frame([4]byte{198, 51, 100, 4}, 17, 6881, 61561, 0, 0, false))
		}
	})
	if len(ks) != 0 {
		t.Fatalf("ordinary traffic named: %s", names(ks))
	}
}

func TestOddFramesAreReadOrSkippedSafely(t *testing.T) {
	ks := kindsOf(func(p *ddos.Profile) {
		// A VLAN-tagged SYN.
		f := frame([4]byte{1, 2, 3, 4}, 6, 1, 80, syn, 0, false)
		tagged := append(append(append([]byte{}, f[:12]...), 0x81, 0x00, 0x00, 0x05), f[12:]...)
		for range 600 {
			p.Add(tagged)
		}
		// An IPv6 UDP packet from port 123.
		v6 := make([]byte, 14+40+8)
		binary.BigEndian.PutUint16(v6[12:], 0x86dd)
		v6[14] = 0x60
		v6[14+6] = 17
		binary.BigEndian.PutUint16(v6[14+40:], 123)
		for range 600 {
			p.Add(v6)
		}
		// Junk.
		p.Add(nil)
		p.Add([]byte{1, 2, 3})
		p.Add(make([]byte, 20))
	})
	got := names(ks)
	if !strings.Contains(got, "SYN flood") || !strings.Contains(got, "NTP amplification from port 123") {
		t.Fatalf("kinds: %s", got)
	}
}

// webLog builds an interval's web log: n requests from line(i).
func webLog(t *testing.T, n int, line func(i int) string) *ddos.WebStats {
	path := filepath.Join(t.TempDir(), "access.log")
	os.WriteFile(path, nil, 0o644)
	tail := &ddos.LogTail{Path: path}
	tail.Requests(map[string]*ddos.Peer{}, nil)
	var b strings.Builder
	for i := range n {
		b.WriteString(line(i) + "\n")
	}
	os.WriteFile(path, []byte(b.String()), 0o644)
	web := ddos.NewWebStats()
	tail.Requests(map[string]*ddos.Peer{}, web)
	return web
}

func req(ip, method, target string, status int, agent string) string {
	return fmt.Sprintf(`%s - - [05/Nov/2026:00:00:00 +0000] "%s %s HTTP/1.1" %d 512 "-" "%s"`, ip, method, target, status, agent)
}

func TestTheKindOfHTTPFloodIsNamed(t *testing.T) {
	browser := "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
	cases := []struct {
		name  string
		web   func(t *testing.T) *ddos.WebStats
		conns int
		want  string
	}{
		{"GET flood on one page", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 600, func(i int) string {
				return req(fmt.Sprintf("45.1.%d.%d", i/250, i%250), "GET", "/search", 200, browser+strconv.Itoa(i%7))
			})
		}, 0, "HTTP GET flood on /search"},
		{"login flood", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 500, func(i int) string { return req("45.1.1.1", "POST", "/login", 401, browser+strconv.Itoa(i%5)) })
		}, 0, "Login flood on /login, turned away"},
		{"XML-RPC", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 500, func(i int) string { return req("45.1.1.1", "POST", "/xmlrpc.php", 200, browser+strconv.Itoa(i%5)) })
		}, 0, "WordPress XML-RPC flood on /xmlrpc.php"},
		{"cache busting", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 500, func(i int) string {
				return req("45.1.1.1", "GET", fmt.Sprintf("/?v=%d", i), 200, browser+strconv.Itoa(i%5))
			})
		}, 0, "Cache-busting flood on /, with random queries"},
		{"random URLs", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 500, func(i int) string {
				return req("45.1.1.1", "GET", fmt.Sprintf("/x%d", i), 404, browser+strconv.Itoa(i%5))
			})
		}, 0, "Random URL flood 100% not found"},
		{"Slowloris, seen in the connections", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 10, func(i int) string { return req("45.1.1.1", "GET", "/", 200, browser) })
		},
			3000, "Slowloris 3000 connections, few requests"},
		{"one tool behind it all", func(t *testing.T) *ddos.WebStats {
			return webLog(t, 500, func(i int) string { return req("45.1.1.1", "GET", "/", 200, "python-requests/2.31") })
		}, 0, "HTTP GET flood on /; every request says it is python-requests/2.31"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ks := ddos.HTTPKinds(c.web(t), c.conns)
			if len(ks) == 0 || strings.TrimSpace(ks[0].Name+" "+ks[0].Detail) != c.want {
				t.Fatalf("kinds: %s", names(ks))
			}
		})
	}
	// Slow POSTs that time out.
	slow := webLog(t, 300, func(i int) string {
		if i%2 == 0 {
			return req("45.1.1.1", "POST", "/upload", 408, browser)
		}
		return req("45.1.1.2", "POST", "/upload", 200, browser+"x")
	})
	if ks := ddos.HTTPKinds(slow, 0); len(ks) == 0 || ks[0].Name != "Slow POST (R.U.D.Y.)" {
		t.Fatalf("slow POST: %s", names(ks))
	}
	if ks := ddos.HTTPKinds(webLog(t, 20, func(i int) string { return req("45.1.1.1", "GET", "/", 200, browser) }), 10); ks != nil {
		t.Fatalf("a quiet log names nothing: %s", names(ks))
	}
}

func TestTheAttackCarriesItsExactKinds(t *testing.T) {
	w := &ddos.Watcher{}
	m := &machine{now: time.Unix(1_800_000_000, 0)}
	for range 20 {
		m.step(w, normal, calmTable(), 2, ddos.Busy{})
	}
	flood := ddos.Counters{UDPIn: 500000, UDPNoPorts: 1000, RxPackets: 500000, RxBytes: 9e7}
	m.step(w, flood, ddos.Table{}, 0, ddos.Busy{})
	// The packet sample comes back between the first spike and the second.
	w.SetKinds([]ddos.Kind{{ID: "udp_reflect_53", Name: "DNS amplification", Detail: "from port 53", Share: 0.92}})
	m.step(w, flood, ddos.Table{}, 0, ddos.Busy{})
	v := w.View()
	if !v.Attacking || len(v.Current.Kinds) != 1 || ddos.Describe(*v.Current) != "DNS amplification" {
		t.Fatalf("current: %+v", v.Current)
	}
}
