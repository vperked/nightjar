package ddos_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/ddos"
)

// fakeFirewall stands in for iptables and nginx: it keeps the rules each family was last given, and
// every command run.
type fakeFirewall struct {
	mu     sync.Mutex
	rules  string // IPv4
	rules6 string
	jumps  map[string]bool // whether INPUT jumps to the defender's chain, by command
	calls  []string
	fail   bool
	// nginxFails is nginx turning down its config.
	nginxFails bool
}

func (f *fakeFirewall) exec(_ context.Context, stdin []byte, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.fail {
		return "", os.ErrPermission
	}
	if f.jumps == nil {
		f.jumps = map[string]bool{}
	}
	switch name {
	case "iptables-restore":
		f.rules = string(stdin)
	case "ip6tables-restore":
		f.rules6 = string(stdin)
	case "nginx":
		if f.nginxFails && args[0] == "-t" {
			return "", errors.New("nginx: [emerg] cannot load certificate")
		}
	case "iptables", "ip6tables":
		switch args[1] {
		case "-C":
			if !f.jumps[name] {
				return "", os.ErrNotExist
			}
		case "-I":
			f.jumps[name] = true
		case "-D":
			if !f.jumps[name] {
				return "", os.ErrNotExist
			}
			f.jumps[name] = false
		case "-F":
			f.rules, f.rules6 = "", ""
		}
	}
	return "", nil
}

// reload is a tool reloading the whole firewall, which takes the defender's chains and jump out.
func (f *fakeFirewall) reload() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules, f.rules6, f.jumps = "", "", nil
}

func (f *fakeFirewall) Count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count(prefix)
}

func (f *fakeFirewall) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// everything is an attack using every method and kind, from a few kinds of address.
func everything() ddos.Situation {
	return ddos.Situation{
		Attacking: true,
		Methods:   []string{ddos.SYNFlood, ddos.UDPFlood, ddos.ICMPFlood, ddos.ConnectionFlood, ddos.HTTPFlood, ddos.Volumetric},
		Kinds: []ddos.Kind{
			{ID: "tcp_SYN"}, {ID: "tcp_ACK"}, {ID: "tcp_SYN-ACK"}, {ID: "udp_reflect_123"}, {ID: "udp_reflect_53"}, {ID: "udp_random"},
			{ID: "icmp_echo"}, {ID: "icmp_reply"}, {ID: "icmp_unreach"}, {ID: "http_slowloris"}, {ID: "gre"},
		},
		Sources: []ddos.Source{
			{Address: "203.0.113.7", SynRecv: 400},                   // a SYN flooder, its address maybe forged
			{Address: "198.51.100.9", Established: 900},              // holds connections
			{Address: "203.0.113.8", Established: 600},               // holds connections too
			{Address: "2001:db8::66", Established: 2, Requests: 900}, // floods the web server directly
			{Address: "192.0.2.44", Requests: 1200},                  // floods it through Cloudflare
			{Address: "104.16.3.3", Established: 5000},               // Cloudflare itself
			{Address: "192.168.0.10", SynRecv: 900},                  // the local network
			{Address: "198.51.100.200", Established: 500, SSH: true}, // you, over SSH
			{Address: "203.0.113.99", Established: 3, Requests: 2},   // an ordinary visitor
		},
		Interval:  5,
		Requests:  3000,
		OpenUDP:   []uint16{53, 51820},
		Conntrack: true,
	}
}

func defender(f *fakeFirewall, clock *time.Time, web string) *ddos.Defender {
	return &ddos.Defender{Exec: f.exec, Now: func() time.Time { return *clock }, WebBlockFile: web, Trusted: func() []string { return []string{"45.33.1.2", "2001:db8:aaaa::/48"} }}
}

// removed says the chains were taken down: the jump out of INPUT, then the chains, in both families.
func removed(f *fakeFirewall) bool {
	return f.Count("iptables -w -D INPUT -j NIGHTJAR") > 0 && f.Count("iptables -w -X NIGHTJAR") > 0 && f.Count("ip6tables -w -X NIGHTJAR") > 0
}

func TestEachKindOfAttackGetsItsDefence(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	ctx := context.Background()
	d.Start(ctx)
	if !removed(f) {
		t.Fatalf("chains left from before are removed on start: %v", f.calls)
	}
	f.calls = nil
	d.Update(ctx, everything())
	r, r6 := f.rules, f.rules6
	for _, want := range []string{
		// Its own chains, declared (which empties them) and filled in one step.
		"*filter\n:NIGHTJAR - [0:0]\n:NIGHTJAR-UDP - [0:0]\n", "COMMIT\n",
		// Never touched, and back to INPUT for the machine's own firewall: loopback, local networks,
		// Cloudflare, SSH, and the trusted.
		"-A NIGHTJAR -i lo -j RETURN", "-A NIGHTJAR -s 192.168.0.0/16 -j RETURN", "-A NIGHTJAR -s 104.16.0.0/13 -j RETURN",
		"-A NIGHTJAR -s 198.51.100.200/32 -j RETURN", "-A NIGHTJAR -s 45.33.1.2/32 -j RETURN",
		// Replies to this machine's own connections, whoever sends them, before any block.
		"-A NIGHTJAR -m conntrack --ctstate ESTABLISHED,RELATED --ctdir REPLY -j RETURN\n-A NIGHTJAR -s ",
		// Blocked: the worst addresses that connect directly.
		"-A NIGHTJAR -s 198.51.100.9 -j DROP", "-A NIGHTJAR -s 203.0.113.8 -j DROP",
		// SYN flood: a limit per address.
		"-A NIGHTJAR -p tcp --syn -m hashlimit --hashlimit-above 50/sec --hashlimit-burst 100 --hashlimit-name nj-syn --hashlimit-mode srcip -j DROP",
		// ACK floods and SYN-ACK reflection: TCP outside connections.
		"-A NIGHTJAR -m conntrack --ctstate INVALID -j DROP", "-A NIGHTJAR -p tcp ! --syn -m conntrack --ctstate NEW -j DROP",
		// Amplification and ping-reply reflection: replies never asked for.
		"-A NIGHTJAR -p udp -m multiport --sports 123,53 -m conntrack --ctstate NEW -j DROP", "-A NIGHTJAR -p icmp --icmp-type echo-reply -m conntrack --ctstate NEW -j DROP",
		// UDP floods: a limit per address, and new UDP to closed ports dropped.
		"--hashlimit-above 10000/sec", "-A NIGHTJAR -p udp -m conntrack --ctstate NEW -j NIGHTJAR-UDP",
		"-A NIGHTJAR-UDP -p udp --dport 53 -j RETURN", "-A NIGHTJAR-UDP -p udp --dport 51820 -j RETURN", "-A NIGHTJAR-UDP -j DROP",
		// Pings and ICMP errors, for everyone together.
		"-p icmp --icmp-type echo-request -m hashlimit --hashlimit-above 100/sec --hashlimit-burst 200 --hashlimit-name nj-ping -j DROP",
		"-p icmp --icmp-type destination-unreachable -m hashlimit --hashlimit-above 100/sec",
		// Connection floods and Slowloris: caps per address, tighter on the web ports.
		"-p tcp --syn -m multiport --dports 80,443 -m connlimit --connlimit-above 50 --connlimit-mask 32 -j DROP",
		"-p tcp --syn -m connlimit --connlimit-above 300 --connlimit-mask 32 -j DROP",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("the IPv4 rules miss %q:\n%s", want, r)
		}
	}
	for _, want := range []string{
		"-A NIGHTJAR -s fe80::/10 -j RETURN", "-A NIGHTJAR -s 2001:db8:aaaa::/48 -j RETURN",
		// An IPv6 machine has a /64 to send from: the block covers it, and the limits count it as one.
		"-A NIGHTJAR -s 2001:db8::/64 -j DROP",
		"-p ipv6-icmp --icmpv6-type echo-request", "-p ipv6-icmp --icmpv6-type echo-reply", "--connlimit-mask 64",
		"--hashlimit-name nj-syn --hashlimit-mode srcip --hashlimit-srcmask 64 -j DROP", "--hashlimit-name nj-udp --hashlimit-mode srcip --hashlimit-srcmask 64 -j DROP",
	} {
		if !strings.Contains(r6, want) {
			t.Errorf("the IPv6 rules miss %q:\n%s", want, r6)
		}
	}
	if strings.Contains(r6, "203.0.113.") || strings.Contains(r, "2001:db8::") || strings.Contains(r6, "destination-unreachable") || strings.Contains(r, "srcmask") {
		t.Error("a rule went to the wrong family")
	}
	// Then traffic is sent through them, first thing in INPUT.
	for _, want := range []string{"iptables-restore -w --noflush", "iptables -w -I INPUT 1 -j NIGHTJAR", "ip6tables-restore -w --noflush", "ip6tables -w -I INPUT 1 -j NIGHTJAR"} {
		if f.Count(want) != 1 {
			t.Errorf("ran %q %d times: %v", want, f.Count(want), f.calls)
		}
	}
	// Never blocked: Cloudflare, the local network, SSH, an ordinary visitor, half-open connections
	// whose address anyone can forge; and without an nginx file, not the address behind Cloudflare
	// either.
	for _, addr := range []string{"104.16.3.3", "192.168.0.10", "198.51.100.200", "203.0.113.99", "192.0.2.44", "203.0.113.7"} {
		if strings.Contains(r, "-s "+addr+" -j DROP") {
			t.Errorf("%s was blocked", addr)
		}
	}

	v := d.View()
	if !v.On || !v.Active || len(v.Blocked) != 3 || v.Problem != "" || v.Error != "" {
		t.Fatalf("view: %+v", v)
	}
	ids := []string{}
	for _, m := range v.Measures {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"syn_limit", "stray_tcp", "reflection", "udp_closed", "udp_limit", "ping_limit", "icmp_errors", "conn_cap", "http_blocks"} {
		if !slices.Contains(ids, want) {
			t.Errorf("no %s measure in %v", want, ids)
		}
	}
	// It says what it cannot do: a full line, GRE, and Cloudflare-borne requests without nginx.
	cannot := strings.Join(v.Cannot, " ")
	for _, want := range []string{"line itself is full", "GRE", "Under Attack mode"} {
		if !strings.Contains(cannot, want) {
			t.Errorf("cannot misses %q: %v", want, v.Cannot)
		}
	}

	// The same moment again changes nothing in the firewall; it only checks its rules are still there.
	before := len(f.calls)
	clock = clock.Add(5 * time.Second)
	d.Update(ctx, everything())
	for _, c := range f.calls[before:] {
		if !strings.Contains(c, " -C INPUT -j NIGHTJAR") {
			t.Fatalf("an unchanged situation rewrote the rules: %v", f.calls[before:])
		}
	}

	// A tool that reloads the whole firewall took them out: they are put back.
	f.reload()
	d.Update(ctx, everything())
	if !strings.Contains(f.rules, "-s 198.51.100.9 -j DROP") || !strings.Contains(f.rules6, "-s 2001:db8::/64 -j DROP") || f.Count("iptables -w -I INPUT 1 -j NIGHTJAR") != 2 {
		t.Fatalf("not put back: %v\n%s", f.calls, f.rules)
	}

	// Unblocking by hand lifts the block and leaves the address alone after.
	if !d.Unblock(ctx, "203.0.113.8") || strings.Contains(f.rules, "-s 203.0.113.8 -j DROP") {
		t.Fatalf("unblock:\n%s", f.rules)
	}
	d.Update(ctx, everything())
	if strings.Contains(f.rules, "-s 203.0.113.8 -j DROP") || !strings.Contains(f.rules, "-s 203.0.113.8/32 -j RETURN") {
		t.Fatal("an address unblocked by hand was blocked again, or not trusted for a while")
	}

	// Over: the measures go, the blocks stay until they run out, then the chains go.
	calm := ddos.Situation{Conntrack: true}
	d.Update(ctx, calm)
	if !strings.Contains(f.rules, "-s 198.51.100.9 -j DROP") || strings.Contains(f.rules, "hashlimit") {
		t.Fatalf("after the attack, blocks without measures:\n%s", f.rules)
	}
	f.calls = nil
	clock = clock.Add(16 * time.Minute)
	d.Update(ctx, calm)
	if !removed(f) || d.View().Active || len(d.View().Blocked) != 0 {
		t.Fatalf("once the blocks ran out the chains are removed: %v %+v", f.calls, d.View())
	}
	f.calls = nil
	d.Update(ctx, calm)
	if len(f.calls) != 0 {
		t.Fatalf("calm moments run iptables: %v", f.calls)
	}
}

func TestDefendingCanBeSwitchedOff(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	on := true
	d := defender(f, &clock, "")
	d.On = func() bool { return on }
	ctx := context.Background()
	d.Update(ctx, everything())
	if !d.View().Active {
		t.Fatal("not defending")
	}
	f.calls = nil
	on = false
	d.Update(ctx, everything())
	if v := d.View(); v.On || v.Active || len(v.Blocked) != 0 || !removed(f) {
		t.Fatalf("switched off, its chains are removed: %+v %v", v, f.calls)
	}
}

func TestWithoutConnectionTrackingItSaysWhatItCannotTellApart(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	s := everything()
	s.Conntrack, s.SynCookiesOff = false, true
	d.Update(context.Background(), s)
	if strings.Contains(f.rules, "conntrack") || strings.Contains(f.rules, "connlimit") || !strings.Contains(f.rules, "-A NIGHTJAR -s 198.51.100.9 -j DROP") {
		t.Fatalf("rules that need connection tracking:\n%s", f.rules)
	}
	cannot := strings.Join(d.View().Cannot, " ")
	if !strings.Contains(cannot, "not tracking connections") || !strings.Contains(cannot, "SYN cookies are off") {
		t.Fatalf("cannot: %v", d.View().Cannot)
	}
}

func TestFloodsThroughCloudflareAreBlockedInNginx(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	file := filepath.Join(t.TempDir(), "nightjar-blocked.conf")
	d := defender(f, &clock, file)
	ctx := context.Background()
	d.Update(ctx, everything())
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), "deny 192.0.2.44;") || strings.Contains(string(b), "deny 203.0.113.7;") {
		t.Fatalf("deny file: %q %v", b, err)
	}
	if f.Count("nginx -t") != 1 || f.Count("nginx -s reload") != 1 {
		t.Fatalf("nginx is checked, then reloaded: %v", f.calls)
	}
	if strings.Contains(strings.Join(d.View().Cannot, " "), "Under Attack") {
		t.Fatal("with an nginx file, Cloudflare-borne requests can be blocked")
	}
	clock = clock.Add(16 * time.Minute)
	d.Update(ctx, ddos.Situation{})
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the file is removed when its blocks run out: %v", err)
	}
	if f.Count("nginx -s reload") != 2 {
		t.Fatalf("and nginx reloaded: %v", f.calls)
	}
}

func TestAFailureToApplyIsShown(t *testing.T) {
	f := &fakeFirewall{fail: true}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	d.Update(context.Background(), everything())
	if v := d.View(); v.Active || !strings.Contains(v.Error, "could not") {
		t.Fatalf("view: %+v", v)
	}
}

// The rules are checked by iptables itself, without putting them in place, where it can be run.
func TestTheRulesAreValidIPTables(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("iptables-restore --test needs root")
	}
	f := &fakeFirewall{}
	clock := time.Now()
	d := defender(f, &clock, "")
	d.Update(context.Background(), everything())
	for cmd, rules := range map[string]string{"iptables-restore": f.rules, "ip6tables-restore": f.rules6} {
		if _, err := exec.LookPath(cmd); err != nil {
			t.Skip("needs " + cmd)
		}
		c := exec.Command(cmd, "--test", "--noflush")
		c.Stdin = strings.NewReader(rules)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s --test: %v\n%s\n%s", cmd, err, out, rules)
		}
	}
}

func TestTheExactKindAloneIsEnough(t *testing.T) {
	for kind, want := range map[string]string{"tcp_SYN": "nj-syn", "tcp_ACK": "--ctstate INVALID -j DROP", "udp_reflect_11211": "--sports 11211 ", "icmp_echo": "echo-request -m hashlimit"} {
		f := &fakeFirewall{}
		clock := time.Unix(1_800_000_000, 0)
		d := defender(f, &clock, "")
		d.Update(context.Background(), ddos.Situation{Attacking: true, Kinds: []ddos.Kind{{ID: kind}}, Conntrack: true})
		if !strings.Contains(f.rules, want) {
			t.Errorf("%s: no %q in\n%s", kind, want, f.rules)
		}
	}
}

func TestOpenUDPPortsAreRead(t *testing.T) {
	ports := map[uint16]bool{}
	ddos.ParseUDPPorts(strings.NewReader(`   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  123: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   101        0 1 2 0 0
  456: 00000000:CA6C 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 3 2 0 0
  789: 0100007F:D431 0100007F:0035 01 00000000:00000000 00:00000000 00000000     0        0 4 2 0 0
`), ports)
	if !ports[53] || !ports[51820] || len(ports) != 2 {
		t.Fatalf("ports: %v", ports)
	}
}

func TestAddressesAreBlockedOnlyForWhatTheAttackDoes(t *testing.T) {
	visitor := ddos.Source{Address: "198.51.100.50", Established: 4, Requests: 750} // a gallery's thumbnails
	holder := ddos.Source{Address: "198.51.100.60", Established: 400}
	for _, c := range []struct {
		methods []string
		blocked []string
	}{
		{[]string{ddos.UDPFlood}, nil},
		{[]string{ddos.SYNFlood, ddos.Volumetric}, nil},
		{[]string{ddos.HTTPFlood}, []string{"198.51.100.50"}},
		{[]string{ddos.ConnectionFlood}, []string{"198.51.100.60"}},
	} {
		f := &fakeFirewall{}
		clock := time.Unix(1_800_000_000, 0)
		d := defender(f, &clock, "")
		d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: c.methods, Interval: 5, Requests: 1000, Conntrack: true,
			Sources: []ddos.Source{visitor, holder, {Address: "203.0.113.7", SynRecv: 5000}}})
		var got []string
		for _, b := range d.View().Blocked {
			got = append(got, b.Address)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.blocked) {
			t.Errorf("%v: blocked %v, want %v", c.methods, got, c.blocked)
		}
	}
}

func TestAnIPv6AttackerIsBlockedByItsSlashSixtyFour(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	var sources []ddos.Source
	for i := range 4 {
		sources = append(sources, ddos.Source{Address: fmt.Sprintf("2001:db8:1:2::%x", i+1), Established: 100}) // none over the line alone
	}
	sources = append(sources, ddos.Source{Address: "2001:db8:aaaa:1::9", Established: 900}) // in a trusted network
	d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: []string{ddos.ConnectionFlood}, Sources: sources, Conntrack: true})
	v := d.View()
	if len(v.Blocked) != 1 || v.Blocked[0].Address != "2001:db8:1:2::/64" || !strings.Contains(f.rules6, "-A NIGHTJAR -s 2001:db8:1:2::/64 -j DROP") {
		t.Fatalf("blocked: %+v\n%s", v.Blocked, f.rules6)
	}
	if !d.Unblock(context.Background(), "2001:db8:1:2::/64") || strings.Contains(f.rules6, "2001:db8:1:2::/64 -j DROP") || !strings.Contains(f.rules6, "-s 2001:db8:1:2::/64 -j RETURN") {
		t.Fatalf("unblock:\n%s", f.rules6)
	}
}

func TestNameServersAndGatewaysAreNeverBlocked(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	d := defender(f, &clock, "")
	d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: []string{ddos.ConnectionFlood}, Upstream: []string{"8.8.8.8"}, Conntrack: true,
		Sources: []ddos.Source{{Address: "8.8.8.8", Established: 900}, {Address: "198.51.100.9", Established: 900}}})
	if strings.Contains(f.rules, "-s 8.8.8.8 -j DROP") || !strings.Contains(f.rules, "-A NIGHTJAR -s 8.8.8.8/32 -j RETURN") || len(d.View().Blocked) != 1 {
		t.Fatalf("a name server blocked:\n%s", f.rules)
	}
}

func TestStoppingTakesTheDefenceAway(t *testing.T) {
	f := &fakeFirewall{}
	clock := time.Unix(1_800_000_000, 0)
	file := filepath.Join(t.TempDir(), "nightjar-blocked.conf")
	d := defender(f, &clock, file)
	ctx := context.Background()
	d.Update(ctx, everything())
	if _, err := os.Stat(file); err != nil || !d.View().Active {
		t.Fatalf("defending: %v %+v", err, d.View())
	}
	f.calls = nil
	d.Stop(ctx)
	if !removed(f) || d.View().Active || len(d.View().Blocked) != 0 {
		t.Fatalf("stopped, its chains are removed: %v %+v", f.calls, d.View())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("and its nginx blocks: %v", err)
	}
}

func TestALeftoverNginxFileIsNeverReplacedWithJunk(t *testing.T) {
	f := &fakeFirewall{nginxFails: true}
	clock := time.Unix(1_800_000_000, 0)
	file := filepath.Join(t.TempDir(), "nightjar-blocked.conf")
	os.WriteFile(file, []byte("deny 192.0.2.44;\n"), 0o644)
	d := defender(f, &clock, file)
	ctx := context.Background()
	d.Start(ctx)
	for range 3 {
		if b, err := os.ReadFile(file); err == nil {
			t.Fatalf("while nginx turns its config down, the file holds %q", b)
		}
		clock = clock.Add(5 * time.Second)
		d.Update(ctx, ddos.Situation{})
	}
	// Once nginx takes its config again, it is reloaded without the old blocks, once.
	f.mu.Lock()
	f.nginxFails = false
	f.mu.Unlock()
	d.Update(ctx, ddos.Situation{})
	d.Update(ctx, ddos.Situation{})
	if f.Count("nginx -s reload") != 1 {
		t.Fatalf("reloaded: %v", f.calls)
	}
}
