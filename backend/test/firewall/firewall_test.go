package firewall_test

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
)

type fake struct {
	mu     sync.Mutex
	v4, v6 string
	calls  []string
	fail   string // a command that fails
	// What iptables-save -c and the kernel log say.
	save, journal string
}

func (f *fake) exec(_ context.Context, stdin []byte, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		return "", os.ErrPermission
	}
	switch {
	case name == "iptables-restore":
		f.v4 = string(stdin)
	case name == "ip6tables-restore":
		f.v6 = string(stdin)
	case strings.Contains(call, " -C "), name == "ufw", name == "firewall-cmd":
		return "", os.ErrNotExist
	case call == "iptables -w -S":
		return "-P INPUT DROP\n-P OUTPUT ACCEPT\n-A INPUT -j NIGHTJAR-RULES\n-A INPUT -p tcp --dport 22 -j ACCEPT\n", nil
	case name == "iptables-save":
		return f.save, nil
	case name == "journalctl":
		return f.journal, nil
	}
	return "", nil
}

func (f *fake) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// sshPort is the port sshd's config names here.
func sshPort() uint16 { return ddos.SSHPorts("/nonexistent")[0] }

// process puts a process in a fake /proc, with its name, its title and the sockets it holds. It runs
// as root, from the program its name says (sshd's own for sshd and sshd-session).
func process(t *testing.T, dir string, pid int, comm, title string, sockets ...int) {
	t.Helper()
	exe := map[string]string{"sshd": "/usr/sbin/sshd", "sshd-session": "/usr/lib/openssh/sshd-session"}[comm]
	runAs(t, dir, pid, comm, title, cmp.Or(exe, "/usr/bin/"+comm), 0, 1, sockets...)
}

// runAs puts a process in a fake /proc with its program, real user and parent too.
func runAs(t *testing.T, dir string, pid int, comm, title, exe string, uid, ppid int, sockets ...int) {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprint(pid))
	os.MkdirAll(filepath.Join(p, "fd"), 0o755)
	os.WriteFile(filepath.Join(p, "comm"), []byte(comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(p, "cmdline"), []byte(title+"\x00\x00"), 0o644)
	os.WriteFile(filepath.Join(p, "status"), []byte(fmt.Sprintf("Name:\t%s\nPPid:\t%d\nUid:\t%d\t%d\t%d\t%d\n", comm, ppid, uid, uid, uid, uid)), 0o644)
	os.Symlink(exe, filepath.Join(p, "exe"))
	for i, s := range sockets {
		if err := os.Symlink(fmt.Sprintf("socket:[%d]", s), filepath.Join(p, "fd", fmt.Sprint(3+i))); err != nil {
			t.Fatal(err)
		}
	}
}

// tcpTable is a /proc/net/tcp with sockets as local address, remote address, state and inode.
func tcpTable(lines ...string) string {
	out := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for i, l := range lines {
		f := strings.Fields(l)
		out += fmt.Sprintf("%4d: %s %s %s 00000000:00000000 00:00000000 00000000     0        0 %s 1 0000000000000000 20 4 30 10 -1\n", i, f[0], f[1], f[2], f[3])
	}
	return out
}

// proc is a /proc with one SSH login from 198.51.100.200 to sshd's port, held by the session sshd
// started for it once it let it in, and nothing listening.
func proc(t *testing.T) string {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	tcp := tcpTable(fmt.Sprintf("00000000:%04X 00000000:0000 0A 1", sshPort()), fmt.Sprintf("1100A8C0:%04X C86433C6:C350 01 4242", sshPort()))
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644)
	process(t, dir, 100, "sshd", "sshd: admin@pts/0", 4242)
	return dir
}

func TestRulesAreCheckedAndPutInOneForm(t *testing.T) {
	for _, c := range []struct {
		in   firewall.Rule
		want firewall.Rule
		err  string
	}{
		{in: firewall.Rule{Action: "Block", Source: " 203.0.113.7 "}, want: firewall.Rule{Action: "block", Source: "203.0.113.7"}},
		{in: firewall.Rule{Action: "allow", Source: "203.0.113.9/24", Port: "8000-8100", Proto: "TCP"}, want: firewall.Rule{Action: "allow", Source: "203.0.113.0/24", Port: "8000:8100", Proto: "tcp"}},
		{in: firewall.Rule{Action: "allow", Source: "any", Port: "443"}, want: firewall.Rule{Action: "allow", Port: "443"}},
		{in: firewall.Rule{Action: "drop", Port: "22"}, err: "allows, blocks or limits"},
		{in: firewall.Rule{Action: "limit", Port: "22", Proto: "tcp", Rate: "6 per minute"}, want: firewall.Rule{Action: "limit", Port: "22", Proto: "tcp", Rate: "6/min"}},
		{in: firewall.Rule{Action: "limit", Port: "53", Rate: "200/s"}, want: firewall.Rule{Action: "limit", Port: "53", Rate: "200/sec"}},
		{in: firewall.Rule{Action: "limit", Port: "443", Rate: "fast"}, err: "not a rate"},
		{in: firewall.Rule{Action: "limit", Port: "443", Rate: "0/min"}, err: "not a rate"},
		{in: firewall.Rule{Action: "limit", Source: "203.0.113.7", Rate: "6/min"}, err: "port of the service"},
		{in: firewall.Rule{Action: "block", Source: "203.0.113.7", Rate: "6/min"}, want: firewall.Rule{Action: "block", Source: "203.0.113.7"}},
		{in: firewall.Rule{Action: "block", Source: "nas.local"}, err: "not an address"},
		{in: firewall.Rule{Action: "block", Port: "70000"}, err: "not a port"},
		{in: firewall.Rule{Action: "block", Port: "90-80"}, err: "not a port"},
		{in: firewall.Rule{Action: "block", Source: "127.0.0.1"}, err: "this machine"},
		{in: firewall.Rule{Action: "block"}, err: "cut this machine off"},
		{in: firewall.Rule{Action: "allow"}, err: "open the whole machine"},
		{in: firewall.Rule{Action: "allow", Source: "203.0.113.7", Proto: "udp"}, err: "give a port"},
		{in: firewall.Rule{Action: "allow", Port: "443, 80,8000-8010, 80", Proto: "tcp"}, want: firewall.Rule{Action: "allow", Port: "443,80,8000:8010", Proto: "tcp"}},
		{in: firewall.Rule{Action: "allow", Port: "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15-16"}, err: "at most 15"},
		{in: firewall.Rule{Action: "allow", Port: ","}, err: "not a port"},
		{in: firewall.Rule{Action: "block", Direction: "OUT", Port: "25"}, want: firewall.Rule{Action: "block", Direction: "out", Port: "25"}},
		{in: firewall.Rule{Action: "block", Direction: "in", Port: "25"}, want: firewall.Rule{Action: "block", Port: "25"}},
		{in: firewall.Rule{Action: "block", Direction: "sideways", Port: "25"}, err: "incoming traffic"},
		{in: firewall.Rule{Action: "limit", Direction: "out", Port: "25", Rate: "6/min"}, err: "limits are for incoming"},
		{in: firewall.Rule{Action: "block", Direction: "out"}, err: "all outgoing traffic"},
	} {
		got, err := firewall.Normalize(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: %+v %v", c.in, got, err)
		}
	}
}

func TestRulesGoInOrderAndEachFamilyGetsItsOwn(t *testing.T) {
	rules := []firewall.Rule{
		{ID: "a1", Action: "allow", Source: "203.0.113.9", Port: "22", Proto: "tcp"},
		{ID: "b1", Action: "block", Source: "203.0.113.0/24"},
		{ID: "b2", Action: "block", Source: "2001:db8::/32", Port: "25"},
		{ID: "p1", Action: "block", Port: "23", Paused: true},
	}
	v4 := firewall.Build(rules, "", nil, false)
	want4 := "*filter\n:NIGHTJAR-RULES - [0:0]\n-A NIGHTJAR-RULES -i lo -j RETURN\n" +
		"-A NIGHTJAR-RULES -s 203.0.113.9 -p tcp --dport 22 -m comment --comment nj:a1 -j ACCEPT\n" +
		"-A NIGHTJAR-RULES -s 203.0.113.0/24 -m limit --limit 10/min --limit-burst 5 -j LOG --log-prefix \"NJ:b1 \"\n" +
		"-A NIGHTJAR-RULES -s 203.0.113.0/24 -m comment --comment nj:b1 -j DROP\nCOMMIT\n"
	if v4 != want4 {
		t.Errorf("v4, in order, the paused rule left out:\n%s", v4)
	}
	v6 := firewall.Build(rules, "", nil, true)
	for _, want := range []string{"-s 2001:db8::/32 -p tcp --dport 25 -m comment --comment nj:b2 -j DROP", "-s 2001:db8::/32 -p udp --dport 25 -m comment --comment nj:b2 -j DROP"} {
		if !strings.Contains(v6, want) {
			t.Errorf("v6 misses %q:\n%s", want, v6)
		}
	}
	if strings.Contains(v6, "203.0.113.") || strings.Contains(v6, ":NIGHTJAR-OUT") {
		t.Errorf("v6 has an IPv4 rule, or an outgoing chain with nothing in it:\n%s", v6)
	}
}

func TestLimitsAreNotLogged(t *testing.T) {
	v4 := firewall.Build([]firewall.Rule{
		{ID: "dmmwg3mj6uik", Action: "limit", Port: "22", Proto: "tcp", Rate: "6/min"},
		{ID: "limit2", Action: "limit", Port: "53", Rate: "200/sec"},
	}, "", nil, false)
	want := "-A NIGHTJAR-RULES -p tcp --dport 22 -m conntrack --ctstate NEW -m hashlimit --hashlimit-above 6/min --hashlimit-burst 6 --hashlimit-mode srcip --hashlimit-name njmwg3mj6uikt -m comment --comment nj:dmmwg3mj6uik -j DROP\n" +
		"-A NIGHTJAR-RULES -p tcp --dport 53 -m conntrack --ctstate NEW -m hashlimit --hashlimit-above 200/sec --hashlimit-burst 400 --hashlimit-mode srcip --hashlimit-name njlimit2t -m comment --comment nj:limit2 -j DROP\n" +
		"-A NIGHTJAR-RULES -p udp --dport 53 -m hashlimit --hashlimit-above 200/sec --hashlimit-burst 400 --hashlimit-mode srcip --hashlimit-name njlimit2u -m comment --comment nj:limit2 -j DROP\n"
	if !strings.Contains(v4, want) || strings.Contains(v4, "LOG") {
		t.Fatalf("limits, unlogged (a second hashlimit would count twice):\n%s", v4)
	}
	if got := firewall.Describe(firewall.Rule{Action: "limit", Port: "22", Proto: "tcp", Rate: "6/min"}); got != "limit anyone on tcp port 22 to 6/min each" {
		t.Fatalf("described: %q", got)
	}
	if got := firewall.Describe(firewall.Rule{Action: "block", Direction: "out", Port: "25,465", Proto: "tcp"}); got != "block outgoing to anyone on tcp ports 25, 465" {
		t.Fatalf("described: %q", got)
	}
}

func TestTheDefaultDropsWhatNoRuleAllowsButKeepsTheWayIn(t *testing.T) {
	rules := []firewall.Rule{{ID: "w", Action: "allow", Port: "80,443", Proto: "tcp"}}
	v4 := firewall.Build(rules, "drop", []uint16{22, 8080}, false)
	tail := "-A NIGHTJAR-RULES -p tcp -m multiport --dports 80,443 -m comment --comment nj:w -j ACCEPT\n" +
		"-A NIGHTJAR-RULES -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n" +
		"-A NIGHTJAR-RULES -p icmp -j RETURN\n" +
		"-A NIGHTJAR-RULES -p udp --sport 67 --dport 68 -j RETURN\n" +
		"-A NIGHTJAR-RULES -p tcp -m multiport --dports 22,8080 -j RETURN\n" +
		"-A NIGHTJAR-RULES -m limit --limit 10/min --limit-burst 5 -j LOG --log-prefix \"NJ:default \"\n" +
		"-A NIGHTJAR-RULES -m comment --comment nj:default -j DROP\nCOMMIT\n"
	if !strings.HasSuffix(v4, tail) {
		t.Fatalf("v4 tail:\n%s", v4)
	}
	if v6 := firewall.Build(rules, "drop", []uint16{22}, true); !strings.Contains(v6, "-p ipv6-icmp -j RETURN") || !strings.Contains(v6, "--dport 546 -j RETURN") {
		t.Fatalf("IPv6 keeps neighbour discovery and DHCPv6:\n%s", v6)
	}
	// With no rules, the default alone still needs the chain.
	if v4 := firewall.Build(nil, "drop", []uint16{22}, false); !strings.Contains(v4, "nj:default -j DROP") {
		t.Fatalf("default alone:\n%s", v4)
	}
}

func TestOutgoingRulesOnlyCatchNewConnections(t *testing.T) {
	v4 := firewall.Build([]firewall.Rule{
		{ID: "o1", Action: "allow", Direction: "out", Source: "198.51.100.25", Port: "25", Proto: "tcp"},
		{ID: "o2", Action: "block", Direction: "out", Port: "25", Proto: "tcp"},
	}, "", nil, false)
	want := "*filter\n:NIGHTJAR-OUT - [0:0]\n-A NIGHTJAR-OUT -o lo -j RETURN\n" +
		"-A NIGHTJAR-OUT -d 198.51.100.25 -p tcp --dport 25 -m conntrack --ctstate NEW -m comment --comment nj:o1 -j ACCEPT\n" +
		"-A NIGHTJAR-OUT -p tcp --dport 25 -m conntrack --ctstate NEW -m limit --limit 10/min --limit-burst 5 -j LOG --log-prefix \"NJ:o2 \"\n" +
		"-A NIGHTJAR-OUT -p tcp --dport 25 -m conntrack --ctstate NEW -m comment --comment nj:o2 -j DROP\nCOMMIT\n"
	if v4 != want {
		t.Fatalf("outgoing:\n%s", v4)
	}
}

func TestRulesAreKeptPutInPlaceAndTakenAway(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := &firewall.Manager{Exec: f.exec, Proc: proc(t), Load: func() []byte { return saved }, Save: func(b []byte) error { saved = b; return nil }}
	ctx := context.Background()
	dashboard := []firewall.Reach{{Address: "45.33.1.2", Ports: []uint16{9101}, What: "the dashboard"}}

	// Blocks that would cut off the dashboard or the SSH login are refused.
	for _, bad := range []firewall.Rule{
		{Action: "block", Source: "45.33.1.0/24"},
		{Action: "block", Port: "9101"},
		{Action: "block", Source: "198.51.100.200"},
		{Action: "block", Port: fmt.Sprintf("%d", sshPort()), Proto: "tcp"},
	} {
		if _, err := m.Add(ctx, bad, dashboard); err == nil || !strings.Contains(err.Error(), "cut off") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// Those not touching them are fine: another port, UDP on the SSH port, another address.
	r, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7", Note: "scanner"}, dashboard)
	if err != nil || r.ID == "" || r.Added == 0 {
		t.Fatalf("add: %+v %v", r, err)
	}
	if !strings.Contains(f.v4, "-A NIGHTJAR-RULES -s 203.0.113.7 -m comment --comment nj:"+r.ID+" -j DROP") || !f.ran("iptables -w -I INPUT 1 -j NIGHTJAR-RULES") || !f.ran("ip6tables-restore -w --noflush") {
		t.Fatalf("in place: %q %v", f.v4, f.calls)
	}
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7"}, dashboard); err == nil {
		t.Fatal("the same rule twice")
	}
	// A limit on the SSH port is fine if the login it would catch still gets 6 a minute.
	ssh := fmt.Sprintf("%d", sshPort())
	if _, err := m.Add(ctx, firewall.Rule{Action: "limit", Port: ssh, Proto: "tcp", Rate: "2/min"}, dashboard); err == nil || !strings.Contains(err.Error(), "too tight") {
		t.Fatalf("a limit too tight for SSH: %v", err)
	}
	lim, err := m.Add(ctx, firewall.Rule{Action: "limit", Port: ssh, Proto: "tcp", Rate: "6/min"}, dashboard)
	if err != nil || !strings.Contains(f.v4, "--hashlimit-above 6/min") {
		t.Fatalf("a limit on SSH: %v\n%s", err, f.v4)
	}
	if err := m.Remove(ctx, lim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Port: fmt.Sprintf("%d", sshPort()), Proto: "udp"}, dashboard); err != nil {
		t.Fatalf("udp on the SSH port: %v", err)
	}
	if !strings.Contains(string(saved), "scanner") {
		t.Fatalf("kept: %s", saved)
	}

	st := m.Status(ctx)
	if st.Tool != "iptables" || st.Policy != "DROP" || len(st.Rules) != 2 {
		t.Fatalf("status: %+v", st)
	}

	// A new run puts the kept rules back.
	f2 := &fake{}
	again := &firewall.Manager{Exec: f2.exec, Proc: proc(t), Load: func() []byte { return saved }, Save: func(b []byte) error { saved = b; return nil }}
	again.Start(ctx)
	if !strings.Contains(f2.v4, "-s 203.0.113.7 -m comment") {
		t.Fatalf("after a restart: %q", f2.v4)
	}

	// A rule iptables turns down is not kept.
	f.fail = "iptables-restore"
	if _, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "8443"}, nil); err == nil || len(m.Status(ctx).Rules) != 2 {
		t.Fatalf("a refused rule: %v", err)
	}
	f.fail = ""

	// Removing them all takes the chain away.
	for _, r := range m.Status(ctx).Rules {
		if err := m.Remove(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !f.ran("iptables -w -X NIGHTJAR-RULES") || !f.ran("ip6tables -w -D INPUT -j NIGHTJAR-RULES") {
		t.Fatalf("chain removed: %v", f.calls)
	}
	if err := m.Remove(ctx, "nope"); err != firewall.ErrNotFound {
		t.Fatalf("remove a missing rule: %v", err)
	}
}

func TestTheRulesAreValidIPTables(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("iptables-restore --test needs root")
	}
	rules := []firewall.Rule{
		{ID: "b1", Action: "block", Source: "203.0.113.0/24"}, {ID: "b2", Action: "block", Port: "25"}, {ID: "a1", Action: "allow", Source: "198.51.100.7", Port: "8000:8100", Proto: "tcp"},
		{ID: "b3", Action: "block", Source: "2001:db8::/32", Port: "53", Proto: "udp"},
		{ID: "dmmwg3mj6uik", Action: "limit", Port: "22", Proto: "tcp", Rate: "6/min"},
		{ID: "limit2", Action: "limit", Source: "198.51.100.0/24", Port: "8000:8100", Rate: "100/hour"},
		{ID: "limit3", Action: "limit", Port: "53,853", Rate: "200/sec"},
		{ID: "web", Action: "allow", Port: "80,443,8000:8010"},
		{ID: "o1", Action: "block", Direction: "out", Source: "192.0.2.0/24"},
		{ID: "o2", Action: "allow", Direction: "out", Port: "25,587", Proto: "tcp"},
		{ID: "o3", Action: "block", Direction: "out", Port: "25"},
	}
	open := []uint16{22, 80, 443, 8080, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	for cmd, six := range map[string]bool{"iptables-restore": false, "ip6tables-restore": true} {
		if _, err := exec.LookPath(cmd); err != nil {
			t.Skip("needs " + cmd)
		}
		for _, def := range []string{"", "drop"} {
			rules := firewall.Build(rules, def, open, six)
			c := exec.Command(cmd, "--test", "--noflush")
			c.Stdin = strings.NewReader(rules)
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s\n%s", cmd, err, out, rules)
			}
		}
	}
}

func TestListeningPortsAreReadWithTheirPrograms(t *testing.T) {
	ports := firewall.Listening("/proc")
	for _, p := range ports {
		if p.Port == 0 || (p.Proto != "tcp" && p.Proto != "udp") {
			t.Fatalf("port: %+v", p)
		}
	}
	// A listener of our own shows, with this program's name.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	for _, p := range firewall.Listening("/proc") {
		if p.Port == port && p.Proto == "tcp" {
			if !p.Local || p.Program == "" {
				t.Fatalf("our listener: %+v", p)
			}
			return
		}
	}
	t.Fatalf("our listener on %d is missing", port)
}

func newManager(t *testing.T, f *fake, saved *[]byte) *firewall.Manager {
	return &firewall.Manager{Exec: f.exec, Proc: proc(t), Load: func() []byte { return *saved }, Save: func(b []byte) error { *saved = b; return nil },
		Open: []firewall.Open{{Port: 8080, What: "this dashboard"}}}
}

func ids(rules []firewall.Rule) string {
	var out []string
	for _, r := range rules {
		out = append(out, r.Note)
	}
	return strings.Join(out, " ")
}

func TestRulesKeepTheirPlaceAndCanBeEditedPausedAndMoved(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	add := func(r firewall.Rule) firewall.Rule {
		t.Helper()
		got, err := m.Add(ctx, r, nil)
		if err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
		return got
	}
	add(firewall.Rule{Action: "allow", Port: "443", Proto: "tcp", Note: "web"})
	add(firewall.Rule{Action: "block", Source: "203.0.113.7", Note: "scanner"})
	smtp := add(firewall.Rule{Action: "block", Direction: "out", Port: "25", Note: "smtp"})
	add(firewall.Rule{Action: "allow", Source: "198.51.100.7", Port: "8443", Note: "office"})
	// A block goes before the allows, an allow last; an outgoing rule beside the incoming ones.
	if got := ids(m.Status(ctx).Rules); got != "scanner web smtp office" {
		t.Fatalf("order: %s", got)
	}

	// Pausing takes a rule out of iptables but keeps it.
	smtp.Paused = true
	if _, err := m.Update(ctx, smtp.ID, smtp, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.v4, "nj:"+smtp.ID) || !strings.Contains(string(saved), `"paused":true`) {
		t.Fatalf("paused: %s\n%s", f.v4, saved)
	}
	// With no outgoing rule left in place, the outgoing chain goes.
	if !f.ran("iptables -w -X NIGHTJAR-OUT") {
		t.Fatalf("outgoing chain left: %v", f.calls)
	}
	// Editing keeps the ID and the place, and is checked like a new rule.
	st := m.Status(ctx)
	scanner := st.Rules[0]
	scanner.Source = "203.0.113.0/24"
	if _, err := m.Update(ctx, scanner.ID, scanner, nil); err != nil {
		t.Fatal(err)
	}
	if r := m.Status(ctx).Rules[0]; r.ID != scanner.ID || r.Source != "203.0.113.0/24" {
		t.Fatalf("edited: %+v", r)
	}
	scanner.Source = "198.51.100.200" // the SSH login
	if _, err := m.Update(ctx, scanner.ID, scanner, nil); err == nil || !strings.Contains(err.Error(), "cut off") {
		t.Fatalf("an edit that cuts off SSH: %v", err)
	}
	scanner.Source = "203.0.113.0/24"
	if _, err := m.Update(ctx, "nope", scanner, nil); err != firewall.ErrNotFound {
		t.Fatalf("editing a missing rule: %v", err)
	}

	// Moving puts the rules in the order given, which must name each once.
	rules := m.Status(ctx).Rules
	order := []string{rules[1].ID, rules[0].ID, rules[3].ID, rules[2].ID}
	if err := m.Order(ctx, order); err != nil {
		t.Fatal(err)
	}
	if got := ids(m.Status(ctx).Rules); got != "web scanner office smtp" {
		t.Fatalf("moved: %s", got)
	}
	if !strings.Contains(f.v4, "nj:"+rules[1].ID+" -j ACCEPT\n-A NIGHTJAR-RULES -s 203.0.113.0/24") {
		t.Fatalf("iptables follows the order:\n%s", f.v4)
	}
	for _, bad := range [][]string{order[:3], {order[0], order[0], order[1], order[2]}, {"x", order[1], order[2], order[3]}} {
		if err := m.Order(ctx, bad); err == nil {
			t.Fatalf("a bad order %v went through", bad)
		}
	}

	// The open ports say whether anyone may reach them whatever the default.
	for _, p := range []struct {
		port  firewall.Port
		allow bool
	}{{firewall.Port{Port: 443, Proto: "tcp"}, true}, {firewall.Port{Port: 8080, Proto: "tcp"}, true}, {firewall.Port{Port: 8443, Proto: "tcp"}, false}, {firewall.Port{Port: 443, Proto: "udp"}, false}} {
		if got := firewall.Allowed(p.port, m.Status(ctx).Rules, m.Status(ctx).Open); got != p.allow {
			t.Errorf("%+v allowed: %v", p.port, got)
		}
	}
}

func TestSavedRulesFromBeforeTheOrderKeepWhatTheyDid(t *testing.T) {
	saved := []byte(`{"rules":[{"id":"a","action":"allow","port":"22","note":"allow"},{"id":"l","action":"limit","port":"22","rate":"6/min","note":"limit"},{"id":"b","action":"block","source":"203.0.113.7","note":"block"}]}`)
	m := newManager(t, &fake{}, &saved)
	if got := ids(m.Status(context.Background()).Rules); got != "block limit allow" {
		t.Fatalf("old rules in the order they worked in: %s", got)
	}
}

func TestTheDefaultGoesBackUnlessKept(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	m.KeepWithin = 150 * time.Millisecond
	ctx := context.Background()
	if _, err := m.SetDefault(ctx, "reject"); err == nil {
		t.Fatal("an unknown default")
	}

	// Not kept in time: it goes back by itself, and was never saved.
	p, err := m.SetDefault(ctx, "drop")
	if err != nil || p.Default != "drop" || p.Until == 0 {
		t.Fatalf("set: %+v %v", p, err)
	}
	st := m.Status(ctx)
	if st.Default != "drop" || st.Pending == nil || !strings.Contains(f.v4, "nj:default -j DROP") {
		t.Fatalf("in place at once: %+v\n%s", st, f.v4)
	}
	if strings.Contains(string(saved), "drop") {
		t.Fatalf("saved before it was kept: %s", saved)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.Status(ctx).Default != "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := m.Status(ctx); st.Default != "" || st.Pending != nil || !f.ran("iptables -w -X NIGHTJAR-RULES") {
		t.Fatalf("went back: %+v %v", st, f.calls)
	}
	if err := m.Keep(ctx, true); err == nil {
		t.Fatal("keeping a change that went back")
	}

	// Undone at once.
	m.SetDefault(ctx, "drop")
	if err := m.Keep(ctx, false); err != nil || m.Status(ctx).Default != "" {
		t.Fatalf("undo: %v", err)
	}

	// Kept: saved, and back after a restart.
	m.KeepWithin = time.Minute
	m.SetDefault(ctx, "drop")
	if err := m.Keep(ctx, true); err != nil || m.Status(ctx).Pending != nil {
		t.Fatalf("keep: %v", err)
	}
	f2 := &fake{}
	again := newManager(t, f2, &saved)
	again.Start(ctx)
	// SSH's port and the dashboard's stay open.
	if again.Status(ctx).Default != "drop" || !strings.Contains(f2.v4, "nj:default -j DROP") || !strings.Contains(f2.v4, fmt.Sprintf("--dports %d,8080 -j RETURN", sshPort())) {
		t.Fatalf("after a restart:\n%s", f2.v4)
	}
}

func TestWhatTheRulesCaughtIsCountedAndLogged(t *testing.T) {
	f := &fake{
		save: `*filter
:INPUT ACCEPT [10:1000]
[7:420] -A NIGHTJAR-RULES -s 203.0.113.0/24 -m limit --limit 10/min --limit-burst 5 -j LOG --log-prefix "NJ:mj6uikblk1 "
[7:420] -A NIGHTJAR-RULES -s 203.0.113.0/24 -m comment --comment nj:dmmwg3mj6uikblk1 -j DROP
[2:100] -A NIGHTJAR-RULES -p tcp --dport 25 -m comment --comment "nj:x2" -j DROP
[1:50] -A NIGHTJAR-RULES -p udp --dport 25 -m comment --comment nj:x2 -j DROP
[30:1800] -A NIGHTJAR-RULES -m comment --comment nj:default -j DROP
[9:9] -A INPUT -m comment --comment nj:x2 -j DROP
COMMIT
`,
		journal: `1762318800.100000 home kernel: NJ:mj6uikblk1 IN=eth0 OUT= MAC=00 SRC=203.0.113.5 DST=192.168.1.10 LEN=60 PROTO=TCP SPT=40000 DPT=22 WINDOW=1 SYN
1762318801.200000 home kernel: some other line
1762318802.300000 home kernel: NJ:default IN=eth0 OUT= SRC=198.51.100.9 DST=192.168.1.10 LEN=40 PROTO=UDP SPT=5353 DPT=161
1762318803.400000 home kernel: NJ:x2 IN= OUT=eth0 SRC=192.168.1.10 DST=192.0.2.25 PROTO=TCP SPT=50000 DPT=25
1762318804.500000 home kernel: NJ:unknown IN=eth0 OUT= SRC=192.0.2.1 DPT=1
`,
	}
	saved := []byte(`{"version":2,"rules":[{"id":"dmmwg3mj6uikblk1","action":"block","source":"203.0.113.0/24"},{"id":"x2","action":"block","port":"25"}]}`)
	m := newManager(t, f, &saved)
	st := m.Status(context.Background())
	if got := st.Hits["dmmwg3mj6uikblk1"]; got.Packets != 7 || got.Bytes != 420 {
		t.Errorf("block hits (its log line not counted twice): %+v", got)
	}
	if got := st.Hits["x2"]; got.Packets != 3 {
		t.Errorf("both protocols, only our chain: %+v", got)
	}
	if st.Hits["default"].Packets != 30 {
		t.Errorf("default: %+v", st.Hits)
	}
	want := []firewall.Drop{
		{At: 1762318803.4, Rule: "x2", Direction: "out", Source: "192.168.1.10", Dest: "192.0.2.25", Proto: "tcp", Port: 25},
		{At: 1762318802.3, Rule: "default", Source: "198.51.100.9", Dest: "192.168.1.10", Proto: "udp", Port: 161},
		{At: 1762318800.1, Rule: "dmmwg3mj6uikblk1", Source: "203.0.113.5", Dest: "192.168.1.10", Proto: "tcp", Port: 22},
	}
	if fmt.Sprint(st.Drops) != fmt.Sprint(want) {
		t.Errorf("drops, newest first, unknown rules left out:\n%+v", st.Drops)
	}
	if st.Native != "-P INPUT DROP\n-P OUTPUT ACCEPT\n-A INPUT -p tcp --dport 22 -j ACCEPT" || !st.Full {
		t.Errorf("the machine's own rules, without ours: %q", st.Native)
	}
}

func TestOnlyALoginIsKeptNotAConnectionToSSH(t *testing.T) {
	ctx := context.Background()
	// A connection still before the password, which anyone can open and hold, is not a login: blocking
	// the address that holds it is allowed.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcpTable(fmt.Sprintf("1100A8C0:%04X C86433C6:C350 01 4242", sshPort()))), 0o644)
	process(t, dir, 100, "sshd", "sshd: root [priv]", 4242)
	process(t, dir, 101, "sshd", "sshd: root [net]", 4242)
	process(t, dir, 102, "sshd", "sshd: a@b [net]", 4242)
	f := &fake{}
	m := &firewall.Manager{Exec: f.exec, Proc: dir}
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "198.51.100.200"}, nil); err != nil {
		t.Fatalf("an SSH brute-forcer could not be blocked: %v", err)
	}

	// Once in, OpenSSH 9.8's session process keeps it, on whichever port it came in.
	dir = t.TempDir()
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	os.WriteFile(filepath.Join(dir, "net", "tcp6"), []byte(tcpTable(
		"0000000000000000FFFF00001100A8C0:08AE 0000000000000000FFFF0000C86433C6:C350 01 77",
		"0000000000000000FFFF00001100A8C0:D431 0000000000000000FFFF0000077100CB:0050 01 78", // a connection it forwards
		"00000000000000000000000000000000:08AE 00000000000000000000000000000000:0000 0A 76",
	)), 0o644)
	process(t, dir, 199, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", 76)
	process(t, dir, 200, "sshd-session", "sshd-session: admin@notty", 77, 78)
	m = &firewall.Manager{Exec: f.exec, Proc: dir}
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Port: "2222", Proto: "tcp"}, nil); err == nil || !strings.Contains(err.Error(), "cut off an SSH login (198.51.100.200, port 2222)") {
		t.Fatalf("a login on another port: %v", err)
	}
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7"}, nil); err != nil {
		t.Fatalf("where a login forwards to is not a login: %v", err)
	}
}

func TestSSHsPortsAreReadFromItsConfigAndItsListeners(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sshd_config.d"), 0o755)
	os.WriteFile(filepath.Join(dir, "sshd_config"), []byte("# Port 1\nInclude sshd_config.d/*.conf\nPasswordAuthentication no\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "sshd_config.d", "10-port.conf"), []byte("Port 2222 # moved\nport=2223\nListenAddress 0.0.0.0\nListenAddress [::1]:2224\n"), 0o644)
	if got := fmt.Sprint(ddos.SSHConfigPorts(filepath.Join(dir, "sshd_config"))); got != "[2222 2223 2224]" {
		t.Fatalf("ports from the included file: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "only"), []byte("ListenAddress 192.0.2.1:2200\n"), 0o644)
	if got := fmt.Sprint(ddos.SSHConfigPorts(filepath.Join(dir, "only"))); got != "[2200]" {
		t.Fatalf("every address with its port: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "none"), []byte("PermitRootLogin no\n"), 0o644)
	if got := fmt.Sprint(ddos.SSHConfigPorts(filepath.Join(dir, "none"))); got != "[22]" {
		t.Fatalf("the default: %s", got)
	}

	// What sshd listens on now counts too, read at each check: the rules follow it there.
	p := proc(t)
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	m.Proc = p
	m.CheckEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	if _, err := m.SetDefault(ctx, "drop"); err != nil {
		t.Fatal(err)
	}
	m.Keep(ctx, true)
	os.WriteFile(filepath.Join(p, "net", "tcp6"), []byte(tcpTable("00000000000000000000000000000000:1F40 00000000000000000000000000000000:0000 0A 99")), 0o644)
	process(t, p, 300, "sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", 99)
	want := fmt.Sprintf("--dports %d,8000,8080 -j RETURN", sshPort())
	if sshPort() > 8000 {
		want = fmt.Sprintf("--dports 8000,%d,8080 -j RETURN", sshPort())
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(f.v4Now(), want) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(f.v4Now(), want) {
		t.Fatalf("sshd's new port is not kept open (%s):\n%s", want, f.v4Now())
	}
}

func (f *fake) v4Now() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.v4
}

func (f *fake) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// hooks keeps INPUT's and OUTPUT's jumps for each family, in order, in front of a fake.
type hooks struct {
	mu   sync.Mutex
	f    *fake
	jump map[string][]string // by "iptables INPUT"
}

func (h *hooks) exec(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	if (name == "iptables" || name == "ip6tables") && len(args) >= 3 && (args[2] == "INPUT" || args[2] == "OUTPUT") {
		h.mu.Lock()
		key := name + " " + args[2]
		in := h.jump[key]
		var out string
		var err error
		switch args[1] {
		case "-S":
			out = "-P " + args[2] + " ACCEPT\n"
			for _, target := range in {
				out += "-A " + args[2] + " -j " + target + "\n"
			}
		case "-C":
			if !slices.Contains(in, args[4]) {
				err = os.ErrNotExist
			}
		case "-I":
			at, _ := strconv.Atoi(args[3])
			if at < 1 || at > len(in)+1 {
				err = fmt.Errorf("index of insertion too big")
			} else {
				h.jump[key] = slices.Insert(slices.Clone(in), at-1, args[5])
			}
		case "-D":
			if i := slices.Index(in, args[4]); i < 0 {
				err = os.ErrNotExist
			} else {
				h.jump[key] = slices.Delete(slices.Clone(in), i, i+1)
			}
		}
		h.mu.Unlock()
		h.f.exec(ctx, stdin, name, args...) // recorded
		return out, err
	}
	return h.f.exec(ctx, stdin, name, args...)
}

func (h *hooks) get(key string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprint(h.jump[key])
}

func (h *hooks) set(key string, targets ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jump[key] = targets
}

func TestRulesTakenOutFromOutsideArePutBack(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	m.CheckEvery = 10 * time.Millisecond
	h := &hooks{f: f, jump: map[string][]string{}}
	m.Exec = h.exec
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7"}, nil); err != nil {
		t.Fatal(err)
	}
	// Still there: checked, left alone.
	before := f.count("iptables-restore")
	deadline := time.Now().Add(3 * time.Second)
	for f.count("iptables -w -S INPUT") < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("iptables-restore") != before {
		t.Fatalf("rules in place were put in place again: %v", f.calls)
	}
	// A reload of the whole firewall took the jump out: it is put back.
	h.set("iptables INPUT")
	for f.count("iptables-restore") == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("iptables-restore") == before {
		t.Fatal("rules taken out were not put back")
	}
	for h.get("iptables INPUT") != "[NIGHTJAR-RULES]" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.get("iptables INPUT"); got != "[NIGHTJAR-RULES]" {
		t.Fatalf("INPUT: %s", got)
	}
}

// The attack defence's jump (NIGHTJAR) comes before the rules' in INPUT: an allow among the rules
// would otherwise let a flood in before the defence's limits and blocks see it. The rules go in just
// after it, and are moved there at the next check when found before it, never leaving INPUT without
// them in between.
func TestTheRulesComeAfterTheAttackDefence(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	m.CheckEvery = 10 * time.Millisecond
	h := &hooks{f: f, jump: map[string][]string{
		"iptables INPUT":  {"NIGHTJAR", "ufw-before-input"},
		"ip6tables INPUT": {"NIGHTJAR"},
	}}
	m.Exec = h.exec
	ctx := context.Background()
	if _, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "443", Proto: "tcp"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.get("iptables INPUT"); got != "[NIGHTJAR NIGHTJAR-RULES ufw-before-input]" {
		t.Fatalf("iptables INPUT: %s", got)
	}
	if got := h.get("ip6tables INPUT"); got != "[NIGHTJAR NIGHTJAR-RULES]" {
		t.Fatalf("ip6tables INPUT: %s", got)
	}
	// Without the defence, the rules go first.
	h.set("ip6tables INPUT", "ufw-before-input")
	if _, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "80", Proto: "tcp"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.get("ip6tables INPUT"); got != "[NIGHTJAR-RULES ufw-before-input]" {
		t.Fatalf("ip6tables INPUT without the defence: %s", got)
	}

	// The defence started after the rules were put before it (as an older version did): the
	// periodic check moves them after it.
	h.set("iptables INPUT", "NIGHTJAR-RULES", "NIGHTJAR", "ufw-before-input")
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.Start(cctx)
	deadline := time.Now().Add(3 * time.Second)
	for h.get("iptables INPUT") != "[NIGHTJAR NIGHTJAR-RULES ufw-before-input]" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.get("iptables INPUT"); got != "[NIGHTJAR NIGHTJAR-RULES ufw-before-input]" {
		t.Fatalf("not moved after the defence: %s", got)
	}
	f.mu.Lock()
	moved := strings.Contains(strings.Join(f.calls, "|"), "iptables -w -S INPUT|iptables -w -I INPUT 3 -j NIGHTJAR-RULES|iptables -w -D INPUT -j NIGHTJAR-RULES|")
	f.mu.Unlock()
	if !moved {
		t.Fatalf("the old jump went before the new one was in: %v", f.calls)
	}
}

// IPv6 that turns the rules down (a kernel booted with ipv6.disable=1) does not keep them from IPv4.
func TestIPv6TurningTheRulesDownLeavesIPv4WithThem(t *testing.T) {
	f := &fake{fail: "ip6tables"}
	var saved []byte
	m := newManager(t, f, &saved)
	if _, err := m.Add(context.Background(), firewall.Rule{Action: "block", Source: "203.0.113.7"}, nil); err != nil {
		t.Fatalf("refused for IPv6's sake: %v", err)
	}
	if !strings.Contains(f.v4Now(), "-s 203.0.113.7") || len(m.Status(context.Background()).Rules) != 1 {
		t.Fatalf("IPv4 rules: %s", f.v4Now())
	}
	// IPv4 turning them down still undoes the change.
	f.fail = "iptables-restore"
	if _, err := m.Add(context.Background(), firewall.Rule{Action: "block", Source: "203.0.113.8"}, nil); err == nil || len(m.Status(context.Background()).Rules) != 1 {
		t.Fatalf("an IPv4 refusal: %v", err)
	}
}

func TestLimitsCountAnIPv6SlashSixtyFourAsOne(t *testing.T) {
	rules := []firewall.Rule{{ID: "l", Action: "limit", Port: "22", Proto: "tcp", Rate: "6/min"}}
	if v6 := firewall.Build(rules, "", nil, true); !strings.Contains(v6, "--hashlimit-mode srcip --hashlimit-srcmask 64 --hashlimit-name njlt") {
		t.Fatalf("v6 limit per /64:\n%s", v6)
	}
	if v4 := firewall.Build(rules, "", nil, false); strings.Contains(v4, "srcmask") {
		t.Fatalf("v4 limit per address:\n%s", v4)
	}
}

func TestUninstallTakesTheRulesOut(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7"}, nil); err != nil {
		t.Fatal(err)
	}
	m.Uninstall(ctx)
	for _, want := range []string{"iptables -w -D INPUT -j NIGHTJAR-RULES", "iptables -w -X NIGHTJAR-RULES", "iptables -w -D OUTPUT -j NIGHTJAR-OUT", "ip6tables -w -D INPUT -j NIGHTJAR-RULES", "ip6tables -w -X NIGHTJAR-OUT"} {
		if !f.ran(want) {
			t.Errorf("did not run %q: %v", want, f.calls)
		}
	}
	if len(saved) == 0 {
		t.Fatal("the saved rules are left alone")
	}
}

// Letting only the dashboard reach the agent's port: an allow for it, then a block for everyone on that
// port. The block goes behind the allow it is an exception to, and is fine there; taking the allow
// away, pausing it or moving it below the block would cut the dashboard off, so each is refused.
func TestOnlyTheDashboardMayReachAPort(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	dashboard := []firewall.Reach{{Address: "45.33.1.2", Ports: []uint16{9101}, What: "the dashboard"}}

	web, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "443", Proto: "tcp"}, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	// On its own, a block on the port is still refused.
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Port: "9101", Proto: "tcp"}, dashboard); err == nil || !strings.Contains(err.Error(), "cut off the dashboard") {
		t.Fatalf("block before the exception: %v", err)
	}
	only, err := m.Add(ctx, firewall.Rule{Action: "allow", Source: "45.33.1.2", Port: "9101", Proto: "tcp", Note: "the dashboard only"}, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := m.Add(ctx, firewall.Rule{Action: "block", Port: "9101", Proto: "tcp"}, dashboard)
	if err != nil {
		t.Fatalf("block behind the exception: %v", err)
	}
	ids := func() []string {
		var out []string
		for _, r := range m.Status(ctx).Rules {
			out = append(out, r.ID)
		}
		return out
	}
	// The block went behind the dashboard's allow; the allow for another port does not meet it.
	if got := ids(); !slices.Equal(got, []string{web.ID, only.ID, rest.ID}) && !slices.Equal(got, []string{only.ID, rest.ID, web.ID}) {
		t.Fatalf("order: %v (web %s, only %s, rest %s)", got, web.ID, only.ID, rest.ID)
	}
	// And iptables gets them in that order: the dashboard's ACCEPT before everyone's DROP.
	v4 := f.v4Now()
	allow, drop := strings.Index(v4, "-s 45.33.1.2 -p tcp --dport 9101"), strings.Index(v4, "-A NIGHTJAR-RULES -p tcp --dport 9101")
	if allow < 0 || drop < 0 || allow > drop {
		t.Fatalf("iptables order:\n%s", v4)
	}

	// What would undo the exception is refused, and the rules stay as they were.
	if err := m.Remove(ctx, only.ID, dashboard...); err == nil || !strings.Contains(err.Error(), "cut off the dashboard") {
		t.Fatalf("remove the exception: %v", err)
	}
	paused := only
	paused.Paused = true
	if _, err := m.Update(ctx, only.ID, paused, dashboard); err == nil {
		t.Fatal("pausing the exception was let through")
	}
	if err := m.Order(ctx, []string{rest.ID, only.ID, web.ID}, dashboard...); err == nil {
		t.Fatal("moving the block above the exception was let through")
	}
	if len(m.Status(ctx).Rules) != 3 {
		t.Fatalf("rules changed: %+v", m.Status(ctx).Rules)
	}
	// Taking the block away first, then the exception, is fine.
	if err := m.Remove(ctx, rest.ID, dashboard...); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, only.ID, dashboard...); err != nil {
		t.Fatal(err)
	}
}

// A block still goes before an allow it narrows: blocking one address on a port everyone may reach.
func TestABlockStillGoesBeforeABroaderAllow(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	web, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "443", Proto: "tcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.9", Port: "443", Proto: "tcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Status(ctx).Rules; len(got) != 2 || got[0].ID != bad.ID || got[1].ID != web.ID {
		t.Fatalf("order: %+v", got)
	}
}

// A port kept open (the agent's) is still shut by a block for everyone ahead of it, since the rules
// come first; the addresses allowed before that block are the only ones let in.
func TestAKeptOpenPortFollowsTheRulesAheadOfIt(t *testing.T) {
	agentPort := firewall.Port{Port: 9101, Proto: "tcp"}
	open := []firewall.Open{{Port: 9101, What: "this agent"}}
	allowed, blocked, only := firewall.Reachable(agentPort, nil, open)
	if !allowed || blocked || only != nil {
		t.Fatalf("no rules: %v %v %v", allowed, blocked, only)
	}
	rules := []firewall.Rule{
		{ID: "a", Action: "allow", Source: "45.33.1.2", Port: "9101", Proto: "tcp"},
		{ID: "b", Action: "block", Port: "9101", Proto: "tcp"},
	}
	allowed, blocked, only = firewall.Reachable(agentPort, rules, open)
	if allowed || !blocked || !slices.Equal(only, []string{"45.33.1.2"}) {
		t.Fatalf("only the dashboard: %v %v %v", allowed, blocked, only)
	}
	if firewall.Allowed(agentPort, rules, open) {
		t.Fatal("Allowed still says anyone")
	}
	// Paused, the block decides nothing, and the port is kept open again.
	rules[1].Paused = true
	if allowed, blocked, _ = firewall.Reachable(agentPort, rules, open); !allowed || blocked {
		t.Fatalf("block paused: %v %v", allowed, blocked)
	}
	// A block alone shuts it for everyone.
	allowed, blocked, only = firewall.Reachable(agentPort, []firewall.Rule{{ID: "b", Action: "block", Port: "9101"}}, open)
	if allowed || !blocked || only != nil {
		t.Fatalf("block alone: %v %v %v", allowed, blocked, only)
	}
}

// The dashboard's "Secure email" rules: mail from other servers on 25, sending and reading mail over
// TLS limited per address (465, 587, 993), and plain-text POP3 and IMAP blocked (110, 143).
func TestTheSecureEmailRulesGoIn(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	for _, r := range []firewall.Rule{
		{Action: "allow", Port: "25", Proto: "tcp", Note: "email"},
		{Action: "limit", Port: "465,587,993", Proto: "tcp", Rate: "30/min", Note: "email"},
		{Action: "block", Port: "110,143", Proto: "tcp", Note: "email"},
	} {
		if _, err := m.Add(ctx, r, nil); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	v4 := f.v4Now()
	for _, want := range []string{"--dport 25 ", "--dports 465,587,993", "--dports 110,143"} {
		if !strings.Contains(v4, want) {
			t.Errorf("no %q in:\n%s", want, v4)
		}
	}
}

// The dashboard's "Secure web" rules: the site open over TCP and HTTP/3, the databases and caches
// behind a web app shut, and none of it catching the dashboard.
func TestTheSecureWebRulesGoIn(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	dashboard := []firewall.Reach{{Address: "45.33.1.2", Ports: []uint16{8080, 9101}, What: "the dashboard"}}
	for _, r := range []firewall.Rule{
		{Action: "allow", Port: "80,443", Proto: "tcp", Note: "web"},
		{Action: "allow", Port: "443", Proto: "udp", Note: "web"},
		{Action: "block", Port: "3306,5432,6379,9200,11211,27017", Proto: "tcp", Note: "web"},
	} {
		if _, err := m.Add(ctx, r, dashboard); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	v4 := f.v4Now()
	for _, want := range []string{"--dports 80,443", "-p udp --dport 443", "--dports 3306,5432,6379,9200,11211,27017"} {
		if !strings.Contains(v4, want) {
			t.Errorf("no %q in:\n%s", want, v4)
		}
	}
}

// A new allow that is an exception to a block goes just before it, so it takes effect: another server
// of yours let into PostgreSQL after "Secure web" shut the databases. One that is not an exception
// still goes last.
func TestANewAllowGoesAheadOfTheBlockItIsAnExceptionTo(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	ctx := context.Background()
	web, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "80,443", Proto: "tcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbs, err := m.Add(ctx, firewall.Rule{Action: "block", Port: "3306,5432,6379", Proto: "tcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := m.Add(ctx, firewall.Rule{Action: "allow", Source: "10.0.0.5", Port: "5432", Proto: "tcp", Note: "my app server"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plex, err := m.Add(ctx, firewall.Rule{Action: "allow", Port: "32400", Proto: "tcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range m.Status(ctx).Rules {
		got = append(got, r.ID)
	}
	// The new block went before nothing it meets (the web allow is for other ports), so it is last of
	// the first two; the exception sits just before it; the unrelated allow goes last.
	idx := func(id string) int { return slices.Index(got, id) }
	if idx(mine.ID) != idx(dbs.ID)-1 || idx(plex.ID) != len(got)-1 || idx(web.ID) < 0 {
		t.Fatalf("order %v: web %s, dbs %s, mine %s, plex %s", got, web.ID, dbs.ID, mine.ID, plex.ID)
	}
	if allowed, blocked, only := firewall.Reachable(firewall.Port{Port: 5432, Proto: "tcp"}, m.Status(ctx).Rules, nil); allowed || !blocked || !slices.Equal(only, []string{"10.0.0.5"}) {
		t.Fatalf("5432: %v %v %v", allowed, blocked, only)
	}
}
