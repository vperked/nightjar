package firewall_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// process puts a process in a fake /proc, with its name, its title and the sockets it holds.
func process(t *testing.T, dir string, pid int, comm, title string, sockets ...int) {
	t.Helper()
	p := filepath.Join(dir, fmt.Sprint(pid))
	os.MkdirAll(filepath.Join(p, "fd"), 0o755)
	os.WriteFile(filepath.Join(p, "comm"), []byte(comm+"\n"), 0o644)
	os.WriteFile(filepath.Join(p, "cmdline"), []byte(title+"\x00\x00"), 0o644)
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

func TestRulesTakenOutFromOutsideArePutBack(t *testing.T) {
	f := &fake{}
	var saved []byte
	m := newManager(t, f, &saved)
	m.CheckEvery = 10 * time.Millisecond
	present := true
	var mu sync.Mutex
	m.Exec = func(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
		mu.Lock()
		there := present
		mu.Unlock()
		if there && slices.Contains(args, "-C") {
			f.exec(ctx, stdin, name, args...)
			return "", nil
		}
		return f.exec(ctx, stdin, name, args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	if _, err := m.Add(ctx, firewall.Rule{Action: "block", Source: "203.0.113.7"}, nil); err != nil {
		t.Fatal(err)
	}
	// Still there: checked, left alone.
	before := f.count("iptables-restore")
	deadline := time.Now().Add(3 * time.Second)
	for f.count("iptables -w -C INPUT -j NIGHTJAR-RULES") < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("iptables-restore") != before {
		t.Fatalf("rules in place were put in place again: %v", f.calls)
	}
	// A reload of the whole firewall took the jump out: it is put back.
	mu.Lock()
	present = false
	mu.Unlock()
	for f.count("iptables-restore") == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.count("iptables-restore") == before {
		t.Fatal("rules taken out were not put back")
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
