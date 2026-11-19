package ddos

// Defending: while an attack goes on, the defender puts rules suited to it in iptables chains of its
// own (NIGHTJAR, and NIGHTJAR-UDP for UDP to closed ports), jumped to first from INPUT, for IPv4 and
// IPv6: limits per address on what the attack sends too much of, and blocks on the addresses sending
// the most. It never changes another chain beyond that one jump, so removing its chains undoes
// everything; it does that once the attack is over and its blocks have run out, when it stops, and
// when it starts, in case it stopped mid-attack. Addresses that must not be cut off are never limited
// or blocked: this machine and its local networks, its name servers and gateways, Cloudflare (which
// carries visitors), whoever is logged in over SSH, and the addresses it is told to trust. Those
// return to INPUT untouched, so the machine's own firewall still decides about them; so do the
// replies to connections this machine made, whoever sends them.
//
// An IPv6 machine has a whole /64 to send from, so for IPv6 the limits count, and the blocks cover,
// a /64 at a time.
//
// What a firewall on the server cannot do is make the line wider: a flood that fills it is lost
// before it arrives, and the view says so.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/logx"
)

var log = logx.Component("Attacks")

// Measure is one defence in force.
type Measure struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	How  string `json:"how"`
}

// Block is an address cut off until a time.
type Block struct {
	Address string  `json:"address"`
	Until   float64 `json:"until"`
	Why     string  `json:"why"`
	// Where is "firewall" for an address that connects directly, or "web" for one that reaches the
	// web server through a proxy such as Cloudflare, blocked in nginx instead.
	Where string `json:"where"`
}

// DefenseView is what the defender is doing, for the dashboard.
type DefenseView struct {
	On bool `json:"on"`
	// Problem says why it cannot defend on this machine.
	Problem  string    `json:"problem,omitempty"`
	Active   bool      `json:"active"` // its rules are in place
	Measures []Measure `json:"measures"`
	Blocked  []Block   `json:"blocked"`
	// Cannot says what it cannot do about the attack under way, in words.
	Cannot []string `json:"cannot,omitempty"`
	// Error is why putting its rules in place last failed.
	Error string `json:"error,omitempty"`
	// Web is whether it can also block addresses in the web server.
	Web bool `json:"web"`
}

// Situation is what the defender works from at each sample.
type Situation struct {
	Attacking bool
	Methods   []string // the methods found now, expected ones left out
	Kinds     []Kind
	Sources   []Source // the busiest addresses, Requests counted over Interval
	Interval  float64  // seconds
	Requests  int      // web requests in the interval, from everyone
	OpenUDP   []uint16 // local UDP ports with a socket open
	Conntrack bool     // the kernel tracks connections, so packets outside one can be told apart
	// SynCookiesOff is true when the kernel's SYN cookies are switched off.
	SynCookiesOff bool
	// Upstream are the name servers and gateways this machine relies on, never blocked.
	Upstream []string
}

// Thresholds for blocking one address while an attack goes on. Half-open connections are not among
// them: a forged address makes those, so a block for them would cut off whoever it names. The SYN
// limit and SYN cookies answer them instead.
const (
	blockConnections = 300 // connections held
	blockRequests    = 30  // web requests a second, and a twentieth of them all
	maxBlocks        = 2000
)

// Rates the limits allow one address, and everyone together for pings.
const (
	synPerSecond   = 50
	udpPerSecond   = 10000
	connsPerSource = 300
	webConnsSlow   = 50
	pingsPerSecond = 100
)

// Defender keeps a firewall table that answers the attack under way.
type Defender struct {
	// On says whether to defend at all; nil always does.
	On func() bool
	// Trusted are more addresses or networks never to block: the dashboard, its servers, and any
	// named in settings.
	Trusted func() []string
	// WebBlockFile is a file of nginx deny lines, in a folder nginx includes in its http block, for
	// addresses that reach the web server through a proxy. Empty blocks in the firewall only.
	WebBlockFile string
	// BlockFor is how long a block lasts; it is renewed while the address keeps on. Default 15m.
	BlockFor time.Duration
	// Exec runs iptables and nginx, with stdin; nil runs them. For tests.
	Exec func(ctx context.Context, stdin []byte, name string, args ...string) (string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// NginxDir is nginx's settings folder, read for the proxies nginx takes visitors' addresses from
	// (set_real_ip_from), which are never blocked. Empty is /etc/nginx; "-" reads none.
	NginxDir string

	mu       sync.Mutex
	started  bool
	present  bool // its chains may be in the firewall
	ipv6     bool // ip6tables is there too
	problem  string
	applied  string // the rules in place, so they are replaced only on change
	active   bool
	measures []Measure
	cannot   []string
	blocked  map[string]Block
	pardoned map[string]time.Time // unblocked by hand: left alone until then
	// proxies are the networks nginx takes visitors' addresses from, as read at proxiesRead.
	proxies     []string
	proxiesRead time.Time
	lastErr     string
	v6Err       string // why IPv6 last could not take the rules
	web         string // the deny file as last written
	// webStale is a deny file left by an earlier run, whose content is not web's.
	webStale bool
	last     Situation
}

// DefendKey is where the dashboard keeps whether to defend: "off", or anything else for on.
const DefendKey = "attacks.defend"

// The defender's chains.
const (
	chain    = "NIGHTJAR"
	udpChain = "NIGHTJAR-UDP"
)

// Chain is the defender's chain, which INPUT jumps to first. The firewall's own rules (RulesChain)
// come after it: an allow there would otherwise let a flood in before its limits and blocks see it.
const Chain = chain

// RulesChain is where the firewall the dashboard manages keeps its incoming rules.
const RulesChain = "NIGHTJAR-RULES"

// JumpPositions are the places, counting from 1, of hook's plain jumps to target in a listing of
// "iptables -S hook".
func JumpPositions(listing, hook, target string) []int {
	var out []int
	n := 0
	for line := range strings.Lines(listing) {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "-A" || f[1] != hook {
			continue
		}
		n++
		if len(f) == 4 && f[2] == "-j" && f[3] == target {
			out = append(out, n)
		}
	}
	return out
}

// pardonFor is how long an address unblocked by hand is left alone.
const pardonFor = time.Hour

func (d *Defender) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Defender) on() bool { return d.On == nil || d.On() }

func (d *Defender) blockFor() time.Duration {
	if d.BlockFor > 0 {
		return d.BlockFor
	}
	return 15 * time.Minute
}

func (d *Defender) run(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	if d.Exec != nil {
		return d.Exec(ctx, stdin, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			err = errors.New(lastLine(msg))
		}
	}
	return string(out), err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Start checks the defender can work here and removes chains left by an earlier run.
func (d *Defender) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.startLocked(ctx)
}

func (d *Defender) startLocked(ctx context.Context) {
	d.started = true
	if d.Exec == nil {
		if os.Geteuid() != 0 {
			d.problem = "Nightjar does not run as root here, so it cannot change the firewall."
			return
		}
		if _, err := exec.LookPath("iptables-restore"); err != nil {
			d.problem = "iptables is not installed, so Nightjar cannot change the firewall."
			return
		}
		_, err := exec.LookPath("ip6tables-restore")
		d.ipv6 = err == nil
	} else {
		d.ipv6 = true
	}
	works := d.ipv6 && ipv6Works(ctx, d.run)
	// What an earlier run left goes from both families, in case IPv6 took rules then.
	d.present = true
	d.removeLocked(ctx)
	d.ipv6 = works
	if d.WebBlockFile != "" {
		if _, err := os.Stat(d.WebBlockFile); err == nil {
			// Blocks from an earlier run are forgotten with its chains.
			d.webStale = true
			d.writeWebLocked(ctx, "")
		}
	}
}

// Update brings the firewall in line with the situation: the measures the attack calls for, the
// addresses to block, and nothing at all once it is over and the blocks have run out.
func (d *Defender) Update(ctx context.Context, s Situation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started {
		d.startLocked(ctx)
	}
	d.last = s
	if d.problem != "" {
		return
	}
	now := d.now()
	for addr, b := range d.blocked {
		if b.Until <= float64(now.Unix()) {
			delete(d.blocked, addr)
		}
	}
	for addr, until := range d.pardoned {
		if now.After(until) {
			delete(d.pardoned, addr)
		}
	}
	if !d.on() {
		d.blocked = nil
		d.measures, d.cannot = nil, nil
		d.removeLocked(ctx)
		d.writeWebLocked(ctx, "")
		return
	}
	trust := d.trust(s)
	var p plan
	if s.Attacking {
		d.blockLocked(s, trust, now)
		p = planFor(s, d.WebBlockFile != "")
	}
	p.conntrack = s.Conntrack
	d.measures, d.cannot = p.measures, p.cannot
	d.syncLocked(ctx, p, trust)
}

// Stop takes the defender's rules and its nginx blocks away, for when the program stops or is
// removed: nothing would be left to lift them. Before Start, it removes what an earlier run left.
func (d *Defender) Stop(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started {
		d.startLocked(ctx)
		return
	}
	if d.problem != "" {
		return
	}
	if d.active {
		log.Info("Stopped defending: Nightjar is stopping, and nothing would lift the blocks")
	}
	d.blocked, d.measures, d.cannot, d.active = nil, nil, nil, false
	d.removeLocked(ctx)
	d.writeWebLocked(ctx, "")
}

// Unblock lifts a block by hand and leaves the address alone for an hour.
func (d *Defender) Unblock(ctx context.Context, addr string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.blocked[addr]; !ok {
		return false
	}
	delete(d.blocked, addr)
	if d.pardoned == nil {
		d.pardoned = map[string]time.Time{}
	}
	d.pardoned[addr] = d.now().Add(pardonFor)
	if d.problem == "" {
		var p plan
		if d.last.Attacking && d.on() {
			p = planFor(d.last, d.WebBlockFile != "")
		}
		p.conntrack = d.last.Conntrack
		d.syncLocked(ctx, p, d.trust(d.last))
	}
	return true
}

// View is what the defender is doing now.
func (d *Defender) View() DefenseView {
	d.mu.Lock()
	defer d.mu.Unlock()
	v := DefenseView{On: d.on(), Problem: d.problem, Active: d.active, Measures: slices.Clone(d.measures), Cannot: slices.Clone(d.cannot), Error: d.lastErr, Web: d.WebBlockFile != ""}
	if v.Measures == nil {
		v.Measures = []Measure{}
	}
	v.Blocked = make([]Block, 0, len(d.blocked))
	for _, b := range d.blocked {
		v.Blocked = append(v.Blocked, b)
	}
	slices.SortFunc(v.Blocked, func(a, b Block) int { return strings.Compare(a.Address, b.Address) })
	return v
}

// syncLocked puts the plan and the blocks in place, if they differ from what is there.
func (d *Defender) syncLocked(ctx context.Context, p plan, trust trustList) {
	var fw, web []Block
	for _, b := range d.blocked {
		if b.Where == "web" {
			web = append(web, b)
		} else {
			fw = append(fw, b)
		}
	}
	d.writeWebLocked(ctx, webDenyFile(web))
	if len(p.rules) == 0 && len(fw) == 0 {
		d.removeLocked(ctx)
		return
	}
	v4, v6 := Rules(p, trust, fw)
	// Each family's chains are filled in one step, then jumped to from INPUT, first.
	type family struct{ cmd, rules string }
	families := []family{{"iptables", v4}}
	if d.ipv6 {
		families = append(families, family{"ip6tables", v6})
	}
	if v4+v6 == d.applied {
		// Unchanged, as long as they are still there and first: a tool that reloads the whole
		// firewall (netfilter-persistent, iptables-restore) takes them out without a word. IPv6 that
		// could not take them is left until they change.
		gone := slices.ContainsFunc(families, func(f family) bool {
			if f.cmd == "ip6tables" && d.v6Err != "" {
				return false
			}
			_, ok := d.jumpPlaced(ctx, f.cmd)
			return !ok
		})
		if !gone {
			return
		}
		log.Warn("Nightjar's defence was taken out of the firewall, or moved after its other rules; putting it back first")
	}
	v6Err := ""
	for _, f := range families {
		if err := d.applyFamilyLocked(ctx, f.cmd, f.rules); err != nil {
			if f.cmd == "ip6tables" {
				// IPv6 that cannot take rules is no reason to leave IPv4 undefended.
				if v6Err = err.Error(); v6Err != d.v6Err {
					log.Warn("Could not defend over IPv6; IPv4 is defended", "err", err)
				}
				continue
			}
			d.lastErr = err.Error()
			log.Warn("Could not defend against the attack", "cmd", f.cmd, "err", err)
			return
		}
	}
	if !d.active {
		log.Info("Defending against the attack", "measures", len(p.measures), "blocked", len(fw))
	}
	d.applied, d.active, d.lastErr, d.v6Err = v4+v6, true, "", v6Err
}

// applyFamilyLocked fills one family's chains and sends INPUT through them first.
func (d *Defender) applyFamilyLocked(ctx context.Context, cmd, rules string) error {
	if _, err := d.run(ctx, []byte(rules), cmd+"-restore", "-w", "--noflush"); err != nil {
		return fmt.Errorf("could not put the firewall rules in place: %w", err)
	}
	d.present = true
	jumps, ok := d.jumpPlaced(ctx, cmd)
	if ok {
		return nil
	}
	// A jump below the firewall's rules is taken out before the new one goes first: deleting by
	// match takes the first, which would be the new one. Only the defender's own jump is touched.
	for range jumps {
		d.run(ctx, nil, cmd, "-w", "-D", "INPUT", "-j", chain)
	}
	if _, err := d.run(ctx, nil, cmd, "-w", "-I", "INPUT", "1", "-j", chain); err != nil {
		return fmt.Errorf("could not send traffic through Nightjar's rules: %w", err)
	}
	return nil
}

// jumpPlaced reads where INPUT jumps to the defender's chain; ok is a jump there before any jump to
// the firewall's rules.
func (d *Defender) jumpPlaced(ctx context.Context, cmd string) (jumps []int, ok bool) {
	listing, err := d.run(ctx, nil, cmd, "-w", "-S", "INPUT")
	if err != nil {
		return nil, false
	}
	jumps, rules := JumpPositions(listing, "INPUT", chain), JumpPositions(listing, "INPUT", RulesChain)
	return jumps, len(jumps) > 0 && (len(rules) == 0 || jumps[0] < rules[0])
}

// ipv6Works says whether IPv6 has a firewall to put rules in: ip6tables can be installed on a kernel
// booted without IPv6 (ipv6.disable=1), where every change to it fails.
func ipv6Works(ctx context.Context, run func(ctx context.Context, stdin []byte, name string, args ...string) (string, error)) bool {
	if _, err := run(ctx, nil, "ip6tables", "-w", "-S", "INPUT"); err != nil {
		log.Info("IPv6 has no firewall here; defending over IPv4 only", "err", err)
		return false
	}
	return true
}

// removeLocked takes the jump out of INPUT and deletes the defender's chains, in both families.
func (d *Defender) removeLocked(ctx context.Context) {
	if !d.present {
		return
	}
	cmds := []string{"iptables"}
	if d.ipv6 {
		cmds = append(cmds, "ip6tables")
	}
	for _, cmd := range cmds {
		// The jump is taken out however many times it is there.
		for range 10 {
			if _, err := d.run(ctx, nil, cmd, "-w", "-D", "INPUT", "-j", chain); err != nil {
				break
			}
		}
		for _, c := range []string{chain, udpChain} {
			if _, err := d.run(ctx, nil, cmd, "-w", "-F", c); err == nil {
				d.run(ctx, nil, cmd, "-w", "-X", c)
			}
		}
	}
	if d.active {
		log.Info("Stopped defending: the attack is over and its blocks have run out")
	}
	d.applied, d.active, d.present, d.lastErr, d.v6Err = "", false, false, "", ""
}

// writeWebLocked writes the nginx deny file and reloads nginx when it changed; an empty file is
// removed. A config nginx turns down is undone.
func (d *Defender) writeWebLocked(ctx context.Context, content string) {
	if d.WebBlockFile == "" || (content == d.web && !d.webStale) {
		return
	}
	previous := d.web
	var err error
	if content == "" {
		err = os.Remove(d.WebBlockFile)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = os.WriteFile(d.WebBlockFile, []byte(content), 0o644)
	}
	if err == nil {
		if _, err = d.run(ctx, nil, "nginx", "-t", "-q"); err == nil {
			_, err = d.run(ctx, nil, "nginx", "-s", "reload")
		} else if previous == "" {
			os.Remove(d.WebBlockFile)
		} else {
			os.WriteFile(d.WebBlockFile, []byte(previous), 0o644)
		}
	}
	if err != nil {
		d.lastErr = "could not block in nginx: " + err.Error()
		log.Warn("Could not update nginx's blocked addresses", "file", d.WebBlockFile, "err", err)
		return
	}
	d.web, d.webStale = content, false
}

// webDenyFile is nginx's deny lines for the web blocks; empty when there are none.
func webDenyFile(blocks []Block) string {
	if len(blocks) == 0 {
		return ""
	}
	slices.SortFunc(blocks, func(a, b Block) int { return strings.Compare(a.Address, b.Address) })
	var b strings.Builder
	b.WriteString("# Written by Nightjar while defending against an HTTP flood: addresses that sent the most\n" +
		"# requests, each until its block runs out. Nightjar rewrites and removes this file; do not edit it.\n")
	for _, bl := range blocks {
		fmt.Fprintf(&b, "deny %s;\n", bl.Address)
	}
	return b.String()
}

// blockLocked blocks the addresses doing the most damage, or renews their blocks. Each is judged by
// what the attack under way does, so a busy visitor is not blocked for its requests during a flood of
// another kind: connections held in a connection flood or a slow-request attack, web requests in an
// HTTP flood.
func (d *Defender) blockLocked(s Situation, trust trustList, now time.Time) {
	holds := slices.Contains(s.Methods, ConnectionFlood) || slices.ContainsFunc(s.Kinds, func(k Kind) bool { return k.ID == "http_slowloris" || k.ID == "http_slow" })
	floods := slices.Contains(s.Methods, HTTPFlood)
	if !holds && !floods {
		return
	}
	if d.blocked == nil {
		d.blocked = map[string]Block{}
	}
	until := float64(now.Add(d.blockFor()).Unix())
	for _, src := range blockTargets(s.Sources) {
		if src.SSH || trust.overlaps(src.net) {
			continue
		}
		if _, ok := d.pardoned[src.Address]; ok {
			continue
		}
		why := ""
		switch {
		case holds && src.Established >= blockConnections:
			why = fmt.Sprintf("%d connections", src.Established)
		case floods && s.Interval > 0 && float64(src.Requests)/s.Interval >= blockRequests && src.Requests*20 >= s.Requests:
			why = fmt.Sprintf("%.0f web requests a second", float64(src.Requests)/s.Interval)
		default:
			continue
		}
		where := "firewall"
		if src.Established+src.SynRecv == 0 {
			// Its requests come through a proxy, so only the web server sees who it is.
			if d.WebBlockFile == "" {
				continue
			}
			where = "web"
		}
		old, had := d.blocked[src.Address]
		if !had && len(d.blocked) >= maxBlocks {
			continue
		}
		if had && old.Until-float64(now.Unix()) > d.blockFor().Seconds()/2 {
			continue // renewed only when half run out, so the rules change rarely
		}
		d.blocked[src.Address] = Block{Address: src.Address, Until: until, Why: why, Where: where}
		if !had {
			log.Info("Blocked an address taking part in the attack", "address", src.Address, "why", why, "in", where)
		}
	}
}

// target is what one block covers, with what its addresses did together: an IPv4 address, or the
// /64 of an IPv6 one, written as a network ("2001:db8::/64").
type target struct {
	Source
	net *net.IPNet
}

// blockTargets gathers the sources into what a block covers, busiest first as they came.
func blockTargets(sources []Source) []target {
	var out []target
	at := map[string]int{}
	for _, src := range sources {
		ip := net.ParseIP(src.Address)
		if ip == nil {
			continue
		}
		n := &net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}
		key := src.Address
		if n.IP == nil {
			n = &net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}
			key = n.String()
		}
		i, ok := at[key]
		if !ok {
			at[key] = len(out)
			out = append(out, target{Source: Source{Address: key}, net: n})
			i = len(out) - 1
		}
		t := &out[i]
		t.Established += src.Established
		t.SynRecv += src.SynRecv
		t.Requests += src.Requests
		t.SSH = t.SSH || src.SSH
	}
	return out
}

// ---------- the rules ----------

// rule is one iptables rule, as the arguments after "-A <chain>", for each family; an empty one
// leaves that family out.
type rule struct{ chain, v4, v6 string }

// both is a rule written the same for IPv4 and IPv6.
func both(r string) rule { return rule{chain: chain, v4: r, v6: r} }

// plan is the measures an attack calls for, and their rules.
type plan struct {
	measures []Measure
	rules    []rule
	cannot   []string
	// conntrack is whether the kernel tracks connections, so replies to this machine's own can be
	// let through before any block.
	conntrack bool
}

func (p *plan) add(id, name, how string, rules ...rule) {
	p.measures = append(p.measures, Measure{ID: id, Name: name, How: how})
	p.rules = append(p.rules, rules...)
}

// above matches traffic over a rate, per address with mode srcip, or all together without one.
func above(name string, rate int, mode string) string {
	s := fmt.Sprintf("-m hashlimit --hashlimit-above %d/sec --hashlimit-burst %d --hashlimit-name %s", rate, 2*rate, name)
	if mode != "" {
		s += " --hashlimit-mode " + mode
	}
	return s
}

// perSource drops what one address sends beyond a rate, an IPv6 /64 counting as one address.
func perSource(match, name string, rate int) rule {
	r := match + " " + above(name, rate, "srcip")
	return rule{chain: chain, v4: r + " -j DROP", v6: r + " --hashlimit-srcmask 64 -j DROP"}
}

// connsOver matches a new connection from an address that already holds n, an IPv6 /64 counting as
// one address.
func connsOver(match string, n int) rule {
	r := func(mask int) string {
		return fmt.Sprintf("-p tcp --syn %s-m connlimit --connlimit-above %d --connlimit-mask %d -j DROP", match, n, mask)
	}
	return rule{chain: chain, v4: r(32), v6: r(64)}
}

// newOnly matches packets that start no tracked connection: not replies to this machine's own.
const newOnly = "-m conntrack --ctstate NEW"

// planFor works out the measures that answer what the attack is: its methods, and the exact kinds
// the packets showed.
func planFor(s Situation, webBlocks bool) plan {
	var p plan
	has := func(m string) bool { return slices.Contains(s.Methods, m) }
	kind := func(pred func(string) bool) []Kind {
		var out []Kind
		for _, k := range s.Kinds {
			if pred(k.ID) {
				out = append(out, k)
			}
		}
		return out
	}
	is := func(id string) func(string) bool { return func(k string) bool { return k == id } }
	needsTracking := func(what string) {
		p.cannot = append(p.cannot, "The kernel is not tracking connections here, so "+what+" cannot be told apart from real traffic.")
	}

	if has(SYNFlood) || len(kind(is("tcp_SYN"))) > 0 {
		p.add("syn_limit", "SYN limit per address", fmt.Sprintf("New connections from one address beyond %d a second are dropped. SYN cookies answer the rest without setting memory aside for each.", synPerSecond),
			perSource("-p tcp --syn", "nj-syn", synPerSecond))
		if s.SynCookiesOff {
			p.cannot = append(p.cannot, "SYN cookies are off on this machine (net.ipv4.tcp_syncookies = 0); with them on, a SYN flood cannot fill the queue of new connections.")
		}
	}

	// TCP that belongs to no connection: ACK, RST, FIN, PSH-ACK, NULL and XMAS floods, SYN-ACK reflection.
	if stray := kind(func(id string) bool { return strings.HasPrefix(id, "tcp_") && id != "tcp_SYN" }); len(stray) > 0 {
		if s.Conntrack {
			p.add("stray_tcp", "Drop TCP outside connections", "TCP packets that neither start a connection nor belong to one, such as a flood of ACKs or of replies to connections never made, are dropped before anything handles them.",
				both("-m conntrack --ctstate INVALID -j DROP"), both("-p tcp ! --syn "+newOnly+" -j DROP"))
		} else {
			needsTracking("packets outside a connection")
		}
	}

	// Reflection: replies to requests this machine never made.
	var ports []string
	for _, k := range kind(func(id string) bool { return strings.HasPrefix(id, "udp_reflect_") }) {
		ports = append(ports, strings.TrimPrefix(k.ID, "udp_reflect_"))
	}
	pingReplies := len(kind(is("icmp_reply"))) > 0
	if len(ports) > 0 || pingReplies {
		if s.Conntrack {
			var rules []rule
			var what []string
			// multiport takes at most 15 ports a rule.
			for chunk := range slices.Chunk(ports, 15) {
				rules = append(rules, both("-p udp -m multiport --sports "+strings.Join(chunk, ",")+" "+newOnly+" -j DROP"))
			}
			if len(ports) > 0 {
				what = append(what, "UDP from port "+strings.Join(ports, ", "))
			}
			if pingReplies {
				rules = append(rules, rule{chain: chain, v4: "-p icmp --icmp-type echo-reply " + newOnly + " -j DROP", v6: "-p ipv6-icmp --icmpv6-type echo-reply " + newOnly + " -j DROP"})
				what = append(what, "ping replies")
			}
			p.add("reflection", "Drop unasked-for replies", "Replies this machine never asked for ("+strings.Join(what, " and ")+") are dropped; answers to its own requests still get through.", rules...)
		} else {
			needsTracking("unasked-for replies")
		}
	}

	if has(UDPFlood) || len(kind(func(id string) bool { return id == "udp_port" || id == "udp_random" })) > 0 {
		p.add("udp_limit", "UDP limit per address", fmt.Sprintf("UDP from one address beyond %d packets a second is dropped.", udpPerSecond),
			perSource("-p udp", "nj-udp", udpPerSecond))
		if s.Conntrack {
			// New UDP goes through its own chain: back to INPUT for a port something listens on,
			// dropped for the rest.
			rules := []rule{both("-p udp " + newOnly + " -j " + udpChain)}
			for _, port := range s.OpenUDP {
				rules = append(rules, rule{chain: udpChain, v4: fmt.Sprintf("-p udp --dport %d -j RETURN", port), v6: fmt.Sprintf("-p udp --dport %d -j RETURN", port)})
			}
			rules = append(rules, rule{chain: udpChain, v4: "-j DROP", v6: "-j DROP"})
			p.add("udp_closed", "Drop UDP to closed ports", "UDP sent to ports nothing listens on is dropped quietly, so the server stops answering each packet with an error.", rules...)
		}
	}

	if has(ICMPFlood) || len(kind(is("icmp_echo"))) > 0 {
		p.add("ping_limit", "Ping limit", fmt.Sprintf("Pings beyond %d a second are dropped, so answering them cannot use up the line or the CPU.", pingsPerSecond),
			rule{chain: chain, v4: "-p icmp --icmp-type echo-request " + above("nj-ping", pingsPerSecond, "") + " -j DROP", v6: "-p ipv6-icmp --icmpv6-type echo-request " + above("nj-ping", pingsPerSecond, "") + " -j DROP"})
	}
	if len(kind(is("icmp_unreach"))) > 0 {
		p.add("icmp_errors", "ICMP error limit", fmt.Sprintf("Unreachable messages beyond %d a second are dropped.", pingsPerSecond),
			rule{chain: chain, v4: "-p icmp --icmp-type destination-unreachable " + above("nj-unreach", pingsPerSecond, "") + " -j DROP"})
	}
	if len(kind(is("gre"))) > 0 {
		p.cannot = append(p.cannot, "GRE packets are left alone, since a tunnel this machine uses could need them; if it uses none, drop GRE (IP protocol 47) in its firewall.")
	}

	slow := len(kind(func(id string) bool { return id == "http_slowloris" || id == "http_slow" })) > 0
	if has(ConnectionFlood) || slow {
		if s.Conntrack {
			var rules []rule
			how := fmt.Sprintf("One address may hold at most %d connections; more are refused, and an address holding %d is blocked for a while.", connsPerSource, blockConnections)
			if slow {
				rules = append(rules, connsOver("-m multiport --dports 80,443 ", webConnsSlow))
				how = fmt.Sprintf("One address may hold at most %d connections to the web server, and %d in all; more are refused, so slow requests cannot take every place, and an address holding %d is blocked for a while.", webConnsSlow, connsPerSource, blockConnections)
			}
			rules = append(rules, connsOver("", connsPerSource))
			p.add("conn_cap", "Connection cap per address", how, rules...)
		} else {
			needsTracking("each address's connections")
		}
	}

	if has(HTTPFlood) {
		where := "in the firewall"
		if webBlocks {
			where = "in the firewall, or in nginx for those coming through Cloudflare"
		}
		p.measures = append(p.measures, Measure{ID: "http_blocks", Name: "Block the busiest addresses",
			How: fmt.Sprintf("Addresses sending more than %d web requests a second are blocked for a while, %s.", blockRequests, where)})
		if !webBlocks {
			p.cannot = append(p.cannot, "Requests that come through Cloudflare cannot be blocked here by address; turn on Under Attack mode in Cloudflare, or let Nightjar write an nginx block file (WEB_BLOCK_FILE).")
		}
	}
	if has(Volumetric) {
		p.cannot = append(p.cannot, "The line itself is full: the flood is lost before it reaches this server, so only your provider, or a service in front such as Cloudflare, can absorb it.")
	}
	return p
}

// Rules are the iptables-restore input for a plan and the blocks, for IPv4 and for IPv6: the
// defender's chains declared (which empties them) and filled in one step, with --noflush so nothing
// else is touched. Trusted addresses return to INPUT before anything else, and so do the replies to
// connections this machine made, where the kernel tracks them; blocked ones are dropped.
func Rules(p plan, trust trustList, blocks []Block) (v4, v6 string) {
	var b4, b6 []string
	for _, bl := range blocks {
		ip, _, err := net.ParseCIDR(bl.Address)
		if err != nil {
			ip = net.ParseIP(bl.Address)
		}
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			b4 = append(b4, bl.Address)
		} else {
			b6 = append(b6, bl.Address)
		}
	}
	slices.Sort(b4)
	slices.Sort(b6)
	family := func(trusted, blocked []string, pick func(rule) string) string {
		var b strings.Builder
		w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
		w("*filter")
		w(":%s - [0:0]", chain)
		w(":%s - [0:0]", udpChain)
		w("-A %s -i lo -j RETURN", chain)
		for _, n := range trusted {
			w("-A %s -s %s -j RETURN", chain, n)
		}
		if p.conntrack {
			// A block on an address must not drop its answers to this machine's own requests.
			w("-A %s -m conntrack --ctstate ESTABLISHED,RELATED --ctdir REPLY -j RETURN", chain)
		}
		for _, addr := range blocked {
			w("-A %s -s %s -j DROP", chain, addr)
		}
		for _, r := range p.rules {
			if text := pick(r); text != "" {
				w("-A %s %s", r.chain, text)
			}
		}
		w("COMMIT")
		return b.String()
	}
	return family(trust.v4, b4, func(r rule) string { return r.v4 }), family(trust.v6, b6, func(r rule) string { return r.v6 })
}

// ---------- who is never blocked ----------

// trustList is the networks never limited or blocked, by family.
type trustList struct {
	nets   []*net.IPNet
	v4, v6 []string
}

// alwaysTrusted are this machine's own kinds of network: private ranges, carrier-grade NAT,
// link-local (which IPv6 neighbour discovery needs) and unique-local IPv6.
var alwaysTrusted = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "fe80::/10", "::1/128"}

// overlaps says whether a network has any trusted address in it.
func (t trustList) overlaps(n *net.IPNet) bool {
	for _, m := range t.nets {
		if m.Contains(n.IP) || n.Contains(m.IP) {
			return true
		}
	}
	return false
}

func (t *trustList) add(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return
		}
		if ip.To4() != nil {
			s += "/32"
		} else {
			s += "/128"
		}
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return
	}
	t.nets = append(t.nets, n)
	if n.IP.To4() != nil {
		t.v4 = append(t.v4, n.String())
	} else {
		t.v6 = append(t.v6, n.String())
	}
}

// trust is everyone never to block now: the local networks, the name servers and gateways, Cloudflare
// and the other proxies nginx takes visitors' addresses from, those logged in over SSH, addresses
// unblocked by hand, and the ones the defender is told to trust.
func (d *Defender) trust(s Situation) trustList {
	var t trustList
	for _, c := range alwaysTrusted {
		t.add(c)
	}
	for _, a := range s.Upstream {
		t.add(a)
	}
	for _, n := range cloudflare {
		t.add(n.String())
	}
	for _, p := range d.proxiesLocked() {
		t.add(p)
	}
	for _, src := range s.Sources {
		if src.SSH {
			t.add(src.Address)
		}
	}
	for addr := range d.pardoned {
		t.add(addr)
	}
	if d.Trusted != nil {
		for _, a := range d.Trusted() {
			t.add(a)
		}
	}
	return t
}
