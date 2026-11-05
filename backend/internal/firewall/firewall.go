// Package firewall is the firewall the dashboard manages: rules that allow, block or limit traffic by
// address and port, coming in or going out, checked in order with the first match deciding; and what
// happens to incoming traffic no rule decides, left to the machine's own firewall or dropped. The rules
// live in chains of their own (NIGHTJAR-RULES first in INPUT, NIGHTJAR-OUT first in OUTPUT), for IPv4
// and IPv6, are saved, and are put back in place when the program starts, since iptables forgets them
// on a reboot. It also reads what the rules caught, the traffic they dropped, which firewall the
// machine runs with its own rules, and which ports are open.
//
// It refuses what would cut off what must keep reaching the machine: the dashboard that asked, and
// whoever is logged in over SSH. Dropping what no rule allows keeps SSH, the dashboard's ports, ping
// and replies open, and goes back by itself unless it is kept within a minute, so a change that cuts
// the dashboard off undoes itself.
package firewall

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/ddos"
	"nightjar/internal/logx"
)

var log = logx.Component("Firewall")

// The chains the rules live in: incoming, then outgoing.
const (
	Chain    = "NIGHTJAR-RULES"
	OutChain = "NIGHTJAR-OUT"
)

// Rule is one rule: allow, block or limit traffic to a port or ports (empty for all) over tcp, udp
// or both, from an address or network (empty for anyone); for an outgoing rule, to that address. A
// limit drops what each address sends beyond its rate: new connections over tcp, packets over udp.
type Rule struct {
	ID     string `json:"id"`
	Action string `json:"action"` // "allow", "block" or "limit"
	// Direction is "out" for what this machine starts sending; empty is incoming.
	Direction string `json:"direction,omitempty"`
	// Source is the other end: where incoming traffic comes from, or where outgoing traffic goes.
	Source string `json:"source,omitempty"`
	Port   string `json:"port,omitempty"`  // "22", "8000:8100" or "80,443"
	Proto  string `json:"proto,omitempty"` // "tcp", "udp", or empty for both
	// Rate is a limit's rate for each address: "6/min", "20/sec" or "100/hour".
	Rate string `json:"rate,omitempty"`
	Note string `json:"note,omitempty"`
	// Paused rules are kept but not in place.
	Paused bool    `json:"paused,omitempty"`
	Added  float64 `json:"added"`
}

// Out says whether a rule is for outgoing traffic.
func (r Rule) Out() bool { return r.Direction == "out" }

// Port is a port something listens on.
type Port struct {
	Port    uint16 `json:"port"`
	Proto   string `json:"proto"`
	Program string `json:"program,omitempty"`
	// Local is a port open on loopback only, which the network cannot reach anyway.
	Local bool `json:"local,omitempty"`
	// Allowed is a port anyone may reach whatever the default: a rule allows everyone to it, or it is
	// kept open.
	Allowed bool `json:"allowed,omitempty"`
}

// Open is a port kept open to everyone however the rest is decided: SSH, the dashboard's.
type Open struct {
	Port uint16 `json:"port"`
	What string `json:"what"`
}

// Hits is what one rule caught since the rules were last put in place.
type Hits struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// Drop is one packet the firewall dropped, from the kernel's log. Rule is the rule's ID, or "default"
// for incoming traffic no rule allowed.
type Drop struct {
	At        float64 `json:"at"`
	Rule      string  `json:"rule"`
	Direction string  `json:"direction,omitempty"`
	Source    string  `json:"source"`
	Dest      string  `json:"dest,omitempty"`
	Proto     string  `json:"proto,omitempty"`
	Port      uint16  `json:"port,omitempty"`
}

// Pending is a change of the default waiting to be kept; at Until it goes back by itself.
type Pending struct {
	Default string  `json:"default"`
	Until   float64 `json:"until"`
}

// Status is the machine's firewall as the dashboard shows it.
type Status struct {
	// Full is a firewall that knows outgoing rules, their order, pausing and the default; agents from
	// before it answer without it.
	Full bool `json:"full"`
	// Tool is the firewall the machine runs: "ufw", "firewalld", or "iptables" for plain rules.
	Tool string `json:"tool"`
	// Policy is what the machine's INPUT does with what no rule decides: "ACCEPT" or "DROP".
	Policy string `json:"policy,omitempty"`
	// Default is what happens here to incoming traffic no rule decides: "" leaves it to the
	// machine's own firewall, "drop" drops it.
	Default string   `json:"default"`
	Pending *Pending `json:"pending,omitempty"`
	// Open are the ports kept open to everyone whatever the default.
	Open      []Open `json:"open"`
	Rules     []Rule `json:"rules"`
	Listening []Port `json:"listening"`
	// Hits is what each rule caught, by ID, and "default" for what the default dropped, counted
	// from Since, when the rules were last put in place.
	Hits  map[string]Hits `json:"hits"`
	Since float64         `json:"since,omitempty"`
	// Drops are the latest packets dropped, newest first: a sample, at most a few a minute for each
	// rule. DropsNote says why there are none to show when the log could not be read.
	Drops     []Drop `json:"drops"`
	DropsNote string `json:"drops_note,omitempty"`
	// Native is the machine's own firewall rules, as its tool lists them.
	Native string `json:"native,omitempty"`
	// Problem says why rules cannot be set here.
	Problem string `json:"problem,omitempty"`
}

// Reach is an address that must keep reaching a port here: the dashboard, an SSH login.
type Reach struct {
	Address string
	Ports   []uint16
	What    string
}

// Manager keeps the rules.
type Manager struct {
	// Load and Save read and keep the rules between runs.
	Load func() []byte
	Save func([]byte) error
	// Exec runs iptables and the firewall tools, with stdin; nil runs them. For tests.
	Exec func(ctx context.Context, stdin []byte, name string, args ...string) (string, error)
	// Proc is where /proc is; empty is /proc.
	Proc string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Open are the ports to keep open to everyone besides SSH's: the dashboard's.
	Open []Open
	// KeepWithin is how long a change of the default waits to be kept; zero is a minute.
	KeepWithin time.Duration
	// CheckEvery is how often the rules in place are checked, and put back if a tool that reloads
	// the whole firewall took them out, or the ports to keep open changed; zero is a minute.
	CheckEvery time.Duration

	mu      sync.Mutex
	started bool
	problem string
	ipv6    bool
	rules   []Rule
	def     string
	pending *pendingChange
	applied time.Time
	built   string // what was last put in place, for each family
}

type pendingChange struct {
	prev  string
	until time.Time
	timer *time.Timer
}

func (m *Manager) proc() string {
	if m.Proc != "" {
		return m.Proc
	}
	return "/proc"
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) run(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	if m.Exec != nil {
		return m.Exec(ctx, stdin, name, args...)
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
			lines := strings.Split(msg, "\n")
			err = errors.New(strings.TrimSpace(lines[len(lines)-1]))
		}
	}
	return string(out), err
}

// saved is how the rules are kept. Before version 2 the rules were not in order: blocks came first,
// then limits, then allows, which is the order they are put in when read.
type saved struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
	Default string `json:"default,omitempty"`
}

// Start loads the saved rules and puts them in place, then keeps them there until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startLocked(ctx)
	go m.keepInPlace(ctx)
}

// keepInPlace checks the rules every CheckEvery until ctx ends.
func (m *Manager) keepInPlace(ctx context.Context) {
	t := time.NewTicker(cmp.Or(m.CheckEvery, time.Minute))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.mu.Lock()
			m.checkLocked(ctx)
			m.mu.Unlock()
		}
	}
}

// checkLocked puts the rules back when they are not what was put in place: the ports to keep open
// changed (SSH moved to another), or a tool that reloads the whole firewall (netfilter-persistent,
// iptables-restore) took out the jumps to them.
func (m *Manager) checkLocked(ctx context.Context) {
	if !m.started || m.problem != "" {
		return
	}
	needIn, needOut, rules := m.wantLocked()
	if strings.Join(rules, "") != m.built {
		// The ports to keep open changed, or the last try to put the rules in place failed.
		log.Info("Putting the firewall rules in place again", "open", fmt.Sprint(m.openLocked()))
	} else {
		gone := false
		for _, cmd := range m.familiesLocked() {
			for _, c := range []struct {
				chain, hook string
				need        bool
			}{{Chain, "INPUT", needIn}, {OutChain, "OUTPUT", needOut}} {
				if c.need && !gone {
					_, err := m.run(ctx, nil, cmd, "-w", "-C", c.hook, "-j", c.chain)
					gone = err != nil
				}
			}
		}
		if !gone {
			return
		}
		log.Warn("The firewall rules were taken out of the firewall; putting them back")
	}
	if err := m.applyLocked(ctx); err != nil {
		log.Warn("Could not put the firewall rules back in place", "err", err)
	}
}

// Uninstall takes the rules out of the firewall for good, their chains and the jumps to them in both
// families, for when the program is removed. The saved rules are left to whoever removes them.
func (m *Manager) Uninstall(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		m.pending.timer.Stop()
		m.pending = nil
	}
	cmds := []string{"iptables"}
	if _, err := exec.LookPath("ip6tables"); err == nil || m.Exec != nil {
		cmds = append(cmds, "ip6tables")
	}
	for _, cmd := range cmds {
		m.takeOutLocked(ctx, cmd, Chain, "INPUT")
		m.takeOutLocked(ctx, cmd, OutChain, "OUTPUT")
	}
	m.started, m.problem, m.built = true, "The firewall rules were taken out for good.", ""
}

func (m *Manager) startLocked(ctx context.Context) {
	m.started = true
	if m.Load != nil {
		var s saved
		if b := m.Load(); len(b) > 0 && json.Unmarshal(b, &s) == nil {
			m.rules, m.def = s.Rules, s.Default
			if s.Version < 2 {
				slices.SortStableFunc(m.rules, func(a, b Rule) int { return cmp.Compare(rank(a.Action), rank(b.Action)) })
			}
		}
	}
	if m.def != "drop" {
		m.def = ""
	}
	if m.Exec == nil {
		if os.Geteuid() != 0 {
			m.problem = "This runs without root here, so it cannot change the firewall."
			return
		}
		if _, err := exec.LookPath("iptables-restore"); err != nil {
			m.problem = "iptables is not installed here."
			return
		}
		_, err := exec.LookPath("ip6tables-restore")
		m.ipv6 = err == nil
	} else {
		m.ipv6 = true
	}
	if err := m.applyLocked(ctx); err != nil {
		log.Warn("Could not put the firewall rules back in place", "err", err)
	}
	if len(m.rules) > 0 || m.def != "" {
		log.Info("Firewall rules in place", "rules", len(m.rules), "default", cmp.Or(m.def, "machine's own"))
	}
}

func rank(action string) int {
	return map[string]int{"block": 0, "limit": 1, "allow": 2}[action]
}

// Status reads the machine's firewall, its open ports, the rules set here and what they caught.
func (m *Manager) Status(ctx context.Context) Status {
	m.mu.Lock()
	if !m.started {
		m.startLocked(ctx)
	}
	st := Status{Full: true, Rules: slices.Clone(m.rules), Default: m.def, Problem: m.problem, Open: m.openLocked()}
	if m.pending != nil {
		st.Pending = &Pending{Default: m.def, Until: float64(m.pending.until.Unix())}
	}
	if !m.applied.IsZero() {
		st.Since = float64(m.applied.Unix())
	}
	ipv6 := m.ipv6
	m.mu.Unlock()
	if st.Rules == nil {
		st.Rules = []Rule{}
	}
	st.Tool = "iptables"
	if out, err := m.run(ctx, nil, "ufw", "status", "verbose"); err == nil && strings.Contains(out, "Status: active") {
		st.Tool, st.Native = "ufw", out
	} else if out, err := m.run(ctx, nil, "firewall-cmd", "--state"); err == nil && strings.TrimSpace(out) == "running" {
		st.Tool = "firewalld"
		st.Native, _ = m.run(ctx, nil, "firewall-cmd", "--list-all")
	}
	st.Hits = map[string]Hits{}
	if st.Problem == "" {
		if out, err := m.run(ctx, nil, "iptables", "-w", "-S"); err == nil {
			var own []string
			for line := range strings.Lines(out) {
				if f := strings.Fields(line); len(f) == 3 && f[0] == "-P" && f[1] == "INPUT" {
					st.Policy = f[2]
				}
				if !strings.Contains(line, "NIGHTJAR") {
					own = append(own, strings.TrimRight(line, "\n"))
				}
			}
			if st.Tool == "iptables" {
				st.Native = strings.Join(own, "\n")
			}
		}
		saves := []string{"iptables-save"}
		if ipv6 {
			saves = append(saves, "ip6tables-save")
		}
		for _, cmd := range saves {
			if out, err := m.run(ctx, nil, cmd, "-c", "-t", "filter"); err == nil {
				countHits(out, st.Hits)
			}
		}
		st.Drops, st.DropsNote = m.drops(ctx, st.Rules)
	}
	if st.Drops == nil {
		st.Drops = []Drop{}
	}
	st.Native = clip(st.Native, 300)
	if st.Listening = Listening(m.proc()); st.Listening == nil {
		st.Listening = []Port{}
	}
	for i, p := range st.Listening {
		st.Listening[i].Allowed = Allowed(p, st.Rules, st.Open)
	}
	return st
}

// openLocked are the ports kept open: SSH's, as they are now, and the dashboard's.
func (m *Manager) openLocked() []Open {
	out := []Open{}
	seen := map[uint16]bool{}
	for _, p := range ddos.SSHPorts(m.proc()) {
		if !seen[p] {
			seen[p] = true
			out = append(out, Open{Port: p, What: "SSH"})
		}
	}
	for _, o := range m.Open {
		if o.Port != 0 && !seen[o.Port] {
			seen[o.Port] = true
			out = append(out, o)
		}
	}
	return out
}

// Allowed says whether anyone may reach a port whatever the default: it is kept open, or the first
// rule for everyone that covers it allows it.
func Allowed(p Port, rules []Rule, open []Open) bool {
	if p.Proto == "tcp" && slices.ContainsFunc(open, func(o Open) bool { return o.Port == p.Port }) {
		return true
	}
	for _, r := range rules {
		if r.Paused || r.Out() || r.Source != "" || (r.Proto != "" && r.Proto != p.Proto) || !covers(r, p.Port) {
			continue
		}
		switch r.Action {
		case "allow":
			return true
		case "block":
			return false
		}
	}
	return false
}

// Add checks a rule, puts it in place and keeps it. keep are those who must keep reaching the machine
// besides whoever is logged in over SSH, which it finds itself. A new allow goes last; a new block or
// limit goes before the allows, so it is not lifted by one.
func (m *Manager) Add(ctx context.Context, r Rule, keep []Reach) (Rule, error) {
	r, err := m.check(r, keep)
	if err != nil {
		return r, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return r, err
	}
	if m.sameLocked(r, "") {
		return r, errors.New("there is already a rule like that")
	}
	if len(m.rules) >= 500 {
		return r, errors.New("there are 500 rules already; remove some first")
	}
	r.ID = strconv.FormatInt(m.now().UnixNano(), 36)
	r.Added = float64(m.now().Unix())
	at := len(m.rules)
	if r.Action != "allow" {
		if i := slices.IndexFunc(m.rules, func(o Rule) bool { return o.Action == "allow" && o.Out() == r.Out() }); i >= 0 {
			at = i
		}
	}
	if err := m.changeLocked(ctx, slices.Insert(slices.Clone(m.rules), at, r)); err != nil {
		return r, err
	}
	log.Info("Firewall rule added", "rule", Describe(r))
	return r, nil
}

// Update changes a rule in place, keeping its ID and where it stands: what it matches, its note, or
// whether it is paused.
func (m *Manager) Update(ctx context.Context, id string, r Rule, keep []Reach) (Rule, error) {
	r, err := m.check(r, keep)
	if err != nil {
		return r, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return r, err
	}
	i := slices.IndexFunc(m.rules, func(o Rule) bool { return o.ID == id })
	if i < 0 {
		return r, ErrNotFound
	}
	if m.sameLocked(r, id) {
		return r, errors.New("there is already a rule like that")
	}
	r.ID, r.Added = id, m.rules[i].Added
	rules := slices.Clone(m.rules)
	rules[i] = r
	if err := m.changeLocked(ctx, rules); err != nil {
		return r, err
	}
	log.Info("Firewall rule changed", "rule", Describe(r), "paused", r.Paused)
	return r, nil
}

// Order puts the rules in the order given, which must name each of them once.
func (m *Manager) Order(ctx context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return err
	}
	if len(ids) != len(m.rules) {
		return errors.New("the order must name every rule once; reload and try again")
	}
	byID := map[string]Rule{}
	for _, r := range m.rules {
		byID[r.ID] = r
	}
	rules := make([]Rule, 0, len(ids))
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			return errors.New("the order must name every rule once; reload and try again")
		}
		delete(byID, id)
		rules = append(rules, r)
	}
	return m.changeLocked(ctx, rules)
}

// Remove takes a rule out.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	if m.problem != "" {
		return errors.New(m.problem)
	}
	gone := m.rules[i]
	if err := m.changeLocked(ctx, slices.Delete(slices.Clone(m.rules), i, i+1)); err != nil {
		return err
	}
	log.Info("Firewall rule removed", "rule", Describe(gone))
	return nil
}

// SetDefault changes what happens to incoming traffic no rule decides: "drop" drops it, "" leaves it
// to the machine's own firewall. It is in place at once and goes back by itself unless Keep is called
// within KeepWithin.
func (m *Manager) SetDefault(ctx context.Context, def string) (Pending, error) {
	if def != "" && def != "drop" {
		return Pending{}, errors.New(`the default is "drop", or empty to leave it to the machine's firewall`)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return Pending{}, err
	}
	prev := m.def
	if m.pending != nil {
		prev = m.pending.prev
		m.pending.timer.Stop()
		m.pending = nil
	}
	m.def = def
	if err := m.applyLocked(ctx); err != nil {
		m.def = prev
		m.applyLocked(ctx)
		return Pending{}, err
	}
	if def == prev {
		m.saveLocked()
		return Pending{Default: def}, nil
	}
	wait := cmp.Or(m.KeepWithin, time.Minute)
	p := &pendingChange{prev: prev, until: m.now().Add(wait)}
	p.timer = time.AfterFunc(wait, func() { m.lapse(p) })
	m.pending = p
	log.Info("Firewall default changed; waiting to be kept", "default", cmp.Or(def, "machine's own"), "within", wait)
	return Pending{Default: def, Until: float64(p.until.Unix())}, nil
}

// Keep keeps a change of the default (keep) or puts it back now.
func (m *Manager) Keep(ctx context.Context, keep bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return errors.New("there is no change waiting; it may have gone back already")
	}
	m.pending.timer.Stop()
	prev := m.pending.prev
	m.pending = nil
	if keep {
		m.saveLocked()
		log.Info("Firewall default kept", "default", cmp.Or(m.def, "machine's own"))
		return nil
	}
	m.def = prev
	log.Info("Firewall default put back", "default", cmp.Or(m.def, "machine's own"))
	return m.applyLocked(context.WithoutCancel(ctx))
}

// lapse puts a change of the default back when it was not kept in time.
func (m *Manager) lapse(p *pendingChange) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != p {
		return
	}
	m.pending = nil
	m.def = p.prev
	log.Warn("Firewall default was not kept in time; put back", "default", cmp.Or(m.def, "machine's own"))
	if err := m.applyLocked(context.Background()); err != nil {
		log.Warn("Could not put the firewall default back", "err", err)
	}
}

// ErrNotFound is a rule that is not there.
var ErrNotFound = errors.New("no such rule")

// check puts a rule in one form and refuses one that would cut off those in keep or an SSH login.
func (m *Manager) check(r Rule, keep []Reach) (Rule, error) {
	r, err := Normalize(r)
	if err != nil || r.Paused || r.Out() {
		return r, err
	}
	return r, guard(r, append(keep, sshLogins(m.proc())...))
}

func (m *Manager) readyLocked(ctx context.Context) error {
	if !m.started {
		m.startLocked(ctx)
	}
	if m.problem != "" {
		return errors.New(m.problem)
	}
	return nil
}

// sameLocked says whether another rule (not skip) matches the same traffic the same way.
func (m *Manager) sameLocked(r Rule, skip string) bool {
	return slices.ContainsFunc(m.rules, func(o Rule) bool {
		return o.ID != skip && o.Action == r.Action && o.Direction == r.Direction && o.Source == r.Source && o.Port == r.Port && o.Proto == r.Proto && o.Rate == r.Rate
	})
}

// changeLocked puts rules in place and keeps them, or leaves things as they were if iptables turns
// them down.
func (m *Manager) changeLocked(ctx context.Context, rules []Rule) error {
	before := m.rules
	m.rules = rules
	if err := m.applyLocked(ctx); err != nil {
		m.rules = before
		m.applyLocked(ctx)
		return err
	}
	m.saveLocked()
	return nil
}

// saveLocked keeps the rules, and the default unless a change of it is still waiting to be kept: a
// restart before then comes back with the old one.
func (m *Manager) saveLocked() {
	if m.Save == nil {
		return
	}
	def := m.def
	if m.pending != nil {
		def = m.pending.prev
	}
	b, _ := json.Marshal(saved{Version: 2, Rules: m.rules, Default: def})
	if err := m.Save(b); err != nil {
		log.Warn("Could not keep the firewall rules", "err", err)
	}
}

// familiesLocked are the iptables commands for the families the rules go to.
func (m *Manager) familiesLocked() []string {
	if m.ipv6 {
		return []string{"iptables", "ip6tables"}
	}
	return []string{"iptables"}
}

// wantLocked is what applyLocked puts in place: whether each chain is needed, and the rules of each
// family in familiesLocked's order, none when neither chain is.
func (m *Manager) wantLocked() (needIn, needOut bool, rules []string) {
	open := []uint16{}
	for _, o := range m.openLocked() {
		open = append(open, o.Port)
	}
	needIn = m.def == "drop" || slices.ContainsFunc(m.rules, func(r Rule) bool { return !r.Paused && !r.Out() })
	needOut = slices.ContainsFunc(m.rules, func(r Rule) bool { return !r.Paused && r.Out() })
	if needIn || needOut {
		for _, cmd := range m.familiesLocked() {
			rules = append(rules, Build(m.rules, m.def, open, cmd == "ip6tables"))
		}
	}
	return needIn, needOut, rules
}

// takeOutLocked takes a chain out of its hook however many times it is there, and deletes it.
func (m *Manager) takeOutLocked(ctx context.Context, cmd, chain, hook string) {
	for range 10 {
		if _, err := m.run(ctx, nil, cmd, "-w", "-D", hook, "-j", chain); err != nil {
			break
		}
	}
	if _, err := m.run(ctx, nil, cmd, "-w", "-F", chain); err == nil {
		m.run(ctx, nil, cmd, "-w", "-X", chain)
	}
}

// applyLocked puts the rules in place, in both families, and takes away a chain with nothing in it.
func (m *Manager) applyLocked(ctx context.Context) error {
	needIn, needOut, rules := m.wantLocked()
	m.built = ""
	for i, cmd := range m.familiesLocked() {
		if needIn || needOut {
			if _, err := m.run(ctx, []byte(rules[i]), cmd+"-restore", "-w", "--noflush"); err != nil {
				return fmt.Errorf("%s turned the rules down: %w", cmd, err)
			}
		}
		for _, c := range []struct {
			chain, hook string
			need        bool
		}{{Chain, "INPUT", needIn}, {OutChain, "OUTPUT", needOut}} {
			if !c.need {
				m.takeOutLocked(ctx, cmd, c.chain, c.hook)
				continue
			}
			if _, err := m.run(ctx, nil, cmd, "-w", "-C", c.hook, "-j", c.chain); err != nil {
				if _, err := m.run(ctx, nil, cmd, "-w", "-I", c.hook, "1", "-j", c.chain); err != nil {
					return fmt.Errorf("could not send traffic through the rules: %w", err)
				}
			}
		}
	}
	m.applied, m.built = m.now(), strings.Join(rules, "")
	return nil
}

// Normalize checks a rule and puts it in one form: a lone address as itself, a network by its start,
// ports as "22", "8000:8100" or "80,443,8000:8100".
func Normalize(r Rule) (Rule, error) {
	r.Action = strings.ToLower(strings.TrimSpace(r.Action))
	r.Direction = strings.ToLower(strings.TrimSpace(r.Direction))
	r.Source, r.Port, r.Proto = strings.TrimSpace(r.Source), strings.TrimSpace(r.Port), strings.ToLower(strings.TrimSpace(r.Proto))
	r.Note = strings.TrimSpace(r.Note)
	if r.Direction == "in" {
		r.Direction = ""
	}
	if r.Direction != "" && r.Direction != "out" {
		return r, errors.New(`a rule is for incoming traffic, or "out" for outgoing`)
	}
	if r.Action != "allow" && r.Action != "block" && r.Action != "limit" {
		return r, errors.New(`a rule allows, blocks or limits`)
	}
	if r.Action == "limit" && r.Out() {
		return r, errors.New("limits are for incoming traffic; block or allow outgoing traffic instead")
	}
	if r.Action == "limit" {
		rate, err := normalizeRate(r.Rate)
		if err != nil {
			return r, err
		}
		r.Rate = rate
	} else {
		r.Rate = ""
	}
	if strings.EqualFold(r.Source, "any") || strings.EqualFold(r.Source, "anyone") || r.Source == "*" {
		r.Source = ""
	}
	if r.Source != "" {
		if ip := net.ParseIP(r.Source); ip != nil {
			r.Source = ip.String()
		} else if _, n, err := net.ParseCIDR(r.Source); err == nil {
			r.Source = n.String()
		} else {
			return r, fmt.Errorf("%q is not an address or a network like 203.0.113.0/24", r.Source)
		}
		if ip, _, _ := net.ParseCIDR(cidr(r.Source)); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			return r, errors.New("that is this machine itself")
		}
	}
	if r.Port != "" {
		port, err := normalizePorts(r.Port)
		if err != nil {
			return r, err
		}
		r.Port = port
	}
	if r.Proto != "" && r.Proto != "tcp" && r.Proto != "udp" {
		return r, errors.New("the protocol is tcp, udp, or both")
	}
	if r.Proto != "" && r.Port == "" {
		return r, errors.New("give a port for tcp or udp alone, or leave both")
	}
	if r.Action == "limit" && r.Port == "" {
		return r, errors.New("give the port of the service to limit")
	}
	if r.Source == "" && r.Port == "" {
		switch {
		case r.Out():
			return r, errors.New("a rule for all outgoing traffic would cut this machine off, or change nothing; give an address or a port")
		case r.Action == "block":
			return r, errors.New("blocking everyone from every port would cut this machine off; give an address or a port")
		default:
			return r, errors.New("allowing everyone to every port would open the whole machine; give an address or a port")
		}
	}
	if len(r.Note) > 200 {
		r.Note = r.Note[:200]
	}
	return r, nil
}

// normalizePorts reads "22", "8000-8100" or "80, 443, 8000-8100": at most 15 ports, a range counting
// as two, which is what iptables takes in one rule.
func normalizePorts(s string) (string, error) {
	var parts []string
	n := 0
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		lo, hi, ok := strings.Cut(strings.ReplaceAll(item, "-", ":"), ":")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := a, error(nil)
		if ok {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b {
			return "", fmt.Errorf("%q is not a port from 1 to 65535, a range like 8000-8100, or a list like 80,443", s)
		}
		p := strconv.Itoa(a)
		n++
		if b != a {
			p += ":" + strconv.Itoa(b)
			n++
		}
		if !slices.Contains(parts, p) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("%q is not a port", s)
	}
	if n > 15 {
		return "", errors.New("a rule takes at most 15 ports, a range counting as two; split it in two")
	}
	return strings.Join(parts, ","), nil
}

func cidr(s string) string {
	if strings.Contains(s, "/") {
		return s
	}
	if strings.Contains(s, ":") {
		return s + "/128"
	}
	return s + "/32"
}

// covers says whether a rule's ports include p.
func covers(r Rule, p uint16) bool {
	if r.Port == "" {
		return true
	}
	for item := range strings.SplitSeq(r.Port, ",") {
		lo, hi, ok := strings.Cut(item, ":")
		a, _ := strconv.Atoi(lo)
		b := a
		if ok {
			b, _ = strconv.Atoi(hi)
		}
		if int(p) >= a && int(p) <= b {
			return true
		}
	}
	return false
}

// minKeptPerMinute is the tightest limit allowed on the ports the dashboard and SSH logins use.
const minKeptPerMinute = 6

// guard refuses a block, or a limit too tight, that would catch one that must keep reaching the
// machine.
func guard(r Rule, keep []Reach) error {
	switch {
	case r.Action == "allow":
		return nil
	case r.Action == "limit" && perMinute(r.Rate) >= minKeptPerMinute:
		return nil
	}
	var n *net.IPNet
	if r.Source != "" {
		_, n, _ = net.ParseCIDR(cidr(r.Source))
	}
	for _, k := range keep {
		ip := net.ParseIP(k.Address)
		if ip == nil || (n != nil && !n.Contains(ip)) {
			continue
		}
		for _, p := range k.Ports {
			if covers(r, p) && (r.Proto == "" || r.Proto == "tcp") {
				if r.Action == "limit" {
					return fmt.Errorf("that limit is too tight for %s (%s, port %d); allow at least %d a minute", k.What, k.Address, p, minKeptPerMinute)
				}
				return fmt.Errorf("that would cut off %s (%s, port %d)", k.What, k.Address, p)
			}
		}
	}
	return nil
}

// sshLogins are the addresses logged in over SSH now, which a block must not catch: those sshd let in,
// not whoever has merely connected to it.
func sshLogins(proc string) []Reach {
	logins := ddos.SSHLogins(proc)
	if len(logins) == 0 {
		return nil
	}
	ports := ddos.SSHPorts(proc)
	var out []Reach
	for _, l := range logins {
		p := ports
		if l.Port != 0 && !slices.Contains(p, l.Port) {
			p = append(slices.Clone(ports), l.Port)
		}
		out = append(out, Reach{Address: l.Address, Ports: p, What: "an SSH login"})
	}
	return out
}

// Describe puts a rule in words: "block 203.0.113.7 on tcp port 22", "block outgoing to anyone on
// port 25".
func Describe(r Rule) string {
	who := "anyone"
	if r.Source != "" {
		who = r.Source
	}
	if r.Out() {
		who = "outgoing to " + who
	}
	what := "every port"
	if r.Port != "" {
		what = "port " + strings.ReplaceAll(strings.ReplaceAll(r.Port, ":", "-"), ",", ", ")
		if strings.Contains(r.Port, ",") {
			what = "ports " + strings.TrimPrefix(what, "port ")
		}
		if r.Proto != "" {
			what = r.Proto + " " + what
		}
	}
	if r.Action == "limit" {
		return fmt.Sprintf("limit %s on %s to %s each", who, what, r.Rate)
	}
	return fmt.Sprintf("%s %s on %s", r.Action, who, what)
}

// rateUnits are the units a limit's rate can be given in, and how many of each fit in a minute.
var rateUnits = map[string]struct {
	unit      string
	perMinute float64
}{
	"s": {"sec", 60}, "sec": {"sec", 60}, "second": {"sec", 60},
	"m": {"min", 1}, "min": {"min", 1}, "minute": {"min", 1},
	"h": {"hour", 1.0 / 60}, "hour": {"hour", 1.0 / 60},
}

// normalizeRate reads "6/min", "6 per minute" or "20/s" as "6/min" or "20/sec".
func normalizeRate(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " per ", "/")
	n, unit, ok := strings.Cut(strings.ReplaceAll(s, " ", ""), "/")
	u, known := rateUnits[strings.TrimSuffix(unit, "s")]
	if !known {
		u, known = rateUnits[unit]
	}
	count, err := strconv.Atoi(n)
	if !ok || !known || err != nil || count < 1 || count > 100000 {
		return "", fmt.Errorf("%q is not a rate like 6/min, 20/sec or 100/hour", s)
	}
	return strconv.Itoa(count) + "/" + u.unit, nil
}

// perMinute is a rate in a minute.
func perMinute(rate string) float64 {
	n, unit, _ := strings.Cut(rate, "/")
	count, _ := strconv.ParseFloat(n, 64)
	return count * rateUnits[unit].perMinute
}

// clip keeps the first n lines of s.
func clip(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append(lines[:n], fmt.Sprintf("… and %d more lines", len(lines)-n))
	}
	return strings.Join(lines, "\n")
}
