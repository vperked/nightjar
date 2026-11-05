package netload_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nightjar/internal/netload"
)

const procNetDev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 10 0 0 0 0 0 0 1000 10 0 0 0 0 0 0
  eth0: 5000 50 1 2 0 0 0 0 9000 90 3 4 0 0 0 0
`

func TestParseProcNetDev(t *testing.T) {
	c := netload.ParseProcNetDev([]byte(procNetDev))
	if e := c["eth0"]; e.RXBytes != 5000 || e.TXBytes != 9000 || e.RXPackets != 50 || e.TXPackets != 90 || e.Errors != 4 || e.Drops != 6 {
		t.Fatalf("eth0 %+v", e)
	}
	if len(c) != 2 {
		t.Fatalf("interfaces %v", c)
	}
}

const ss = `0      0      10.0.0.5:22      10.0.0.9:51000 users:(("sshd",pid=10,fd=4))
	 cubic rtt:1 bytes_sent:500 bytes_acked:400 bytes_received:300 segs_out:9
0      0      10.0.0.5:32400   203.0.113.7:4444 users:(("Plex Media Serv",pid=20,fd=9))
	 cubic bytes_acked:100000 bytes_received:2000
0      0      127.0.0.1:5173   127.0.0.1:55864 users:(("node",pid=30,fd=3))
	 cubic bytes_acked:99999999 bytes_received:99999999
0      0      [::ffff:10.0.0.5]:443   [::ffff:198.51.100.4]:60000 users:(("nginx",pid=40,fd=7))
	 cubic bytes_acked:10 bytes_received:20
`

func TestParseSS(t *testing.T) {
	socks := netload.ParseSS([]byte(ss))
	if len(socks) != 3 {
		t.Fatalf("loopback is left out: %+v", socks)
	}
	if socks[2].Process != "nginx" || socks[2].Recv != 20 {
		t.Fatalf("IPv6 brackets: %+v", socks[2])
	}
	if s := socks[0]; s.Process != "sshd" || s.PID != 10 || s.Sent != 400 || s.Recv != 300 || s.Peer != "10.0.0.9:51000" {
		t.Fatalf("socket %+v", s)
	}
}

// fakeMachine moves counters forward by the given rates each call.
type fakeMachine struct {
	dev  netload.Counters
	plex uint64
}

func meter(f *fakeMachine) *netload.Meter {
	m := netload.New()
	m.ReadDev = func() (map[string]netload.Counters, error) {
		return map[string]netload.Counters{"eth0": f.dev, "docker0": {}}, nil
	}
	m.ReadSockets = func(context.Context) ([]netload.Socket, error) {
		return []netload.Socket{
			{Local: "10.0.0.5:32400", Peer: "203.0.113.7:4444", Process: "Plex Media Serv", PID: 20, Sent: f.plex},
			{Local: "10.0.0.5:32400", Peer: "203.0.113.8:5555", Process: "Plex Media Serv", PID: 20},
			{Local: "10.0.0.5:22", Peer: "10.0.0.9:51000", Process: "sshd", PID: 10},
		}, nil
	}
	m.Identify = func(pid int, comm string) (string, string) {
		if pid == 20 {
			return "Plex Media Server", "plexmediaserver"
		}
		return comm, "ssh"
	}
	m.Describe = func(name string) (bool, bool, int) { return true, name != "eth0", map[string]int{"eth0": 1000}[name] }
	m.DefaultIf = func() string { return "eth0" }
	return m
}

func TestRatesPeaksAndTalkers(t *testing.T) {
	f := &fakeMachine{}
	m := meter(f)
	t0 := time.Unix(1_800_000_000, 0)
	m.Sample(t0, true)
	for i, rate := range []uint64{1000, 3000, 2000} {
		f.dev.RXBytes += rate
		f.dev.TXBytes += rate * 10
		f.dev.Errors += uint64(i)
		f.plex += 4000
		m.Sample(t0.Add(time.Duration(i+1)*time.Second), true)
	}
	v := m.View(0)
	eth := v.Interfaces[0]
	if eth.Name != "eth0" || !eth.Default || eth.Virtual || eth.Speed != 1000 {
		t.Fatalf("the default route's interface lists first: %+v", v.Interfaces)
	}
	if eth.RXRate != 2000 || eth.TXRate != 20000 || eth.RXPeak != 3000 || eth.RXAvg != 2000 || eth.NewErrors != 3 {
		t.Fatalf("eth0 %+v", eth)
	}
	if len(v.Samples["eth0"]) != 3 {
		t.Fatalf("one sample per second: %v", v.Samples["eth0"])
	}
	if got := m.View(v.Samples["eth0"][1].TS).Samples["eth0"]; len(got) != 1 {
		t.Fatalf("since returns only newer samples: %v", got)
	}
	plex := v.Processes[0]
	if plex.Name != "Plex Media Server" || plex.Unit != "plexmediaserver" || plex.Connections != 2 || plex.TX != 4000 {
		t.Fatalf("busiest program first, by full name: %+v", v.Processes)
	}
	if h := v.Hosts[0]; h.Peer != "203.0.113.7" || h.TX != 4000 || h.Process != "Plex Media Server" {
		t.Fatalf("hosts %+v", v.Hosts)
	}
	if v.Connections != 3 {
		t.Fatalf("connections %d", v.Connections)
	}
}

func TestFirstSocketReadingHasNoRates(t *testing.T) {
	f := &fakeMachine{plex: 1 << 40} // a long-lived connection with a huge total
	m := meter(f)
	m.Sample(time.Unix(100, 0), true)
	if p := m.View(0).Processes[0]; p.TX != 0 {
		t.Fatalf("a total is not a rate: %+v", p)
	}
}

func TestAWokenMeterAnswersWithItsInterfacesAtOnce(t *testing.T) {
	f := &fakeMachine{}
	m := meter(f)
	m.Tick = time.Hour // only the reading Watch takes itself
	m.Watch()
	v := m.View(0)
	if len(v.Interfaces) == 0 || v.Interfaces[0].Name != "eth0" {
		t.Fatalf("the first answer after waking has no interfaces: %+v", v.Interfaces)
	}
	if len(v.Samples["eth0"]) != 0 {
		t.Fatalf("one reading has no rate yet: %v", v.Samples["eth0"])
	}
}

// An interface with nothing newer than since, and a meter with no talkers yet, answer with empty
// lists, never null: the dashboard goes through each.
func TestTheViewNeverHasNullLists(t *testing.T) {
	f := &fakeMachine{}
	m := meter(f)
	m.Sample(time.Unix(1_800_000_000, 0), true)
	b, err := json.Marshal(m.View(1_900_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "null") {
		t.Fatalf("null in %s", b)
	}
	b, _ = json.Marshal(meter(&fakeMachine{}).View(0))
	if strings.Contains(string(b), "null") {
		t.Fatalf("a meter that has not sampled: %s", b)
	}
}
