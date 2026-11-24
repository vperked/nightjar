// Package tailscale manages this machine's Tailscale: whether it is connected and as whom, the other
// machines on the tailnet, and the settings the dashboard can change (exit node, subnet routes,
// Tailscale SSH, MagicDNS, shields up, the machine's name). It drives the tailscale command, which is
// Tailscale's stable interface to tailscaled.
//
// Some changes can cut the dashboard off from the machine: routing everything through an exit node
// sends the answers to whoever reached the machine from outside the tailnet that way too, and shields
// up turns away everyone reaching it over the tailnet. Like the firewall's default drop, those go back
// by themselves unless kept within two minutes. Disconnecting or logging out cannot be undone from
// here, so they are refused when asked for over Tailscale itself.
package tailscale

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/logx"
)

var log = logx.Component("Tailscale")

// State is this machine's Tailscale as the dashboard shows it.
type State struct {
	// Installed is the tailscale command being here; Running is tailscaled answering it.
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	// Backend is tailscaled's state: NeedsLogin, NeedsMachineAuth, Stopped, Starting or Running.
	Backend string `json:"backend"`
	// LoggedIn is a login to a tailnet being in place, connected or not.
	LoggedIn bool `json:"logged_in"`
	// AuthURL is where to log in, while tailscaled waits for that.
	AuthURL string `json:"auth_url,omitempty"`
	Version string `json:"version,omitempty"`
	Tailnet string `json:"tailnet,omitempty"`
	// MagicDNS is the tailnet's DNS suffix, when MagicDNS is on.
	MagicDNS string   `json:"magic_dns,omitempty"`
	Self     *Node    `json:"self,omitempty"`
	Peers    []Node   `json:"peers"`
	Health   []string `json:"health"`
	Settings Current  `json:"settings"`
	Pending  *Pending `json:"pending,omitempty"`
	Problem  string   `json:"problem,omitempty"`
	// Now is this machine's clock as it answered, unix seconds: Pending.Until is on it.
	Now int64 `json:"now"`
}

// Node is a machine on the tailnet.
type Node struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	DNSName string   `json:"dns_name,omitempty"`
	OS      string   `json:"os,omitempty"`
	IPs     []string `json:"ips"`
	User    string   `json:"user,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Online  bool     `json:"online"`
	// Active is traffic flowing to it lately.
	Active bool `json:"active"`
	// LastSeen is when it was last online, unix seconds; zero when online or never.
	LastSeen int64 `json:"last_seen,omitempty"`
	// Direct is the address it is reached at directly; Relay is the DERP relay used otherwise.
	Direct string `json:"direct,omitempty"`
	Relay  string `json:"relay,omitempty"`
	RX     int64  `json:"rx"`
	TX     int64  `json:"tx"`
	// ExitNode is this machine sending its internet traffic through it; ExitNodeOption is it
	// offering to carry that.
	ExitNode       bool `json:"exit_node"`
	ExitNodeOption bool `json:"exit_node_option"`
	// Routes are the subnets it routes for the tailnet.
	Routes []string `json:"routes,omitempty"`
	// KeyExpiry is when its key expires, unix seconds; zero when it never does.
	KeyExpiry int64 `json:"key_expiry,omitempty"`
	Expired   bool  `json:"expired,omitempty"`
}

// Current is the settings in place.
type Current struct {
	AcceptRoutes      bool     `json:"accept_routes"`
	AcceptDNS         bool     `json:"accept_dns"`
	SSH               bool     `json:"ssh"`
	ShieldsUp         bool     `json:"shields_up"`
	AdvertiseExitNode bool     `json:"advertise_exit_node"`
	AdvertiseRoutes   []string `json:"advertise_routes"`
	// ExitNode is the ID of the machine carrying this one's internet traffic; empty is none.
	ExitNode         string `json:"exit_node"`
	ExitNodeAllowLAN bool   `json:"exit_node_allow_lan"`
	// Hostname is the name asked for on the tailnet; empty is the machine's own.
	Hostname string `json:"hostname"`
}

// Settings is a change of settings: only those given change.
type Settings struct {
	AcceptRoutes      *bool     `json:"accept_routes,omitempty"`
	AcceptDNS         *bool     `json:"accept_dns,omitempty"`
	SSH               *bool     `json:"ssh,omitempty"`
	ShieldsUp         *bool     `json:"shields_up,omitempty"`
	AdvertiseExitNode *bool     `json:"advertise_exit_node,omitempty"`
	AdvertiseRoutes   *[]string `json:"advertise_routes,omitempty"`
	// ExitNode is a machine's ID (or Tailscale address); empty stops using one.
	ExitNode         *string `json:"exit_node,omitempty"`
	ExitNodeAllowLAN *bool   `json:"exit_node_allow_lan,omitempty"`
	Hostname         *string `json:"hostname,omitempty"`
}

// Pending is a change waiting to be kept; at Until it goes back by itself.
type Pending struct {
	Until int64 `json:"until"`
	// What says what goes back, in words.
	What string `json:"what"`
}

// Manager manages Tailscale. Its methods are safe for concurrent use.
type Manager struct {
	// Exec runs tailscale; nil runs it. For tests.
	Exec func(ctx context.Context, name string, args ...string) (string, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// KeepWithin is how long a risky change waits to be kept; zero is two minutes.
	KeepWithin time.Duration
	// CheckEvery is how often Run looks for a change not kept in time; zero is five seconds.
	CheckEvery time.Duration
	// LoginWait is how long a login waits for tailscaled to give a link; zero is five seconds.
	LoginWait time.Duration

	mu      sync.Mutex
	pending *pending
}

// pending is a risky change waiting to be kept, and the flags that undo it.
type pending struct {
	until  time.Time
	what   string
	revert []string
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// errNotInstalled is the tailscale command not being found.
var errNotInstalled = errors.New("Tailscale is not installed on this machine")

// run runs tailscale and gives its output; an error carries the last line it printed.
func (m *Manager) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if m.Exec != nil {
		return m.Exec(ctx, "tailscale", args...)
	}
	name := "tailscale"
	if _, err := exec.LookPath(name); err != nil {
		if _, err := os.Stat("/usr/bin/tailscale"); err != nil {
			return "", errNotInstalled
		}
		name = "/usr/bin/tailscale"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// tailscale asks before cutting off an SSH session over Tailscale when it thinks it is in one; the
	// dashboard is not, whatever the environment it was started from says.
	cmd.Env = withoutSSH(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			lines := strings.Split(msg, "\n")
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(lines[len(lines)-1]))
		}
	}
	return string(out), err
}

func withoutSSH(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(v string) bool {
		return strings.HasPrefix(v, "SSH_CLIENT=") || strings.HasPrefix(v, "SSH_CONNECTION=") || strings.HasPrefix(v, "SSH_TTY=")
	})
}

// ---------- reading ----------

// rawStatus is the part of "tailscale status --json" read here.
type rawStatus struct {
	Version        string
	BackendState   string
	AuthURL        string
	Self           *rawPeer
	Health         []string
	MagicDNSSuffix string
	CurrentTailnet *struct {
		Name            string
		MagicDNSSuffix  string
		MagicDNSEnabled bool
	}
	Peer map[string]*rawPeer
	User map[string]struct {
		LoginName   string
		DisplayName string
	}
}

type rawPeer struct {
	ID             string
	HostName       string
	DNSName        string
	OS             string
	UserID         int64
	TailscaleIPs   []string
	Tags           []string
	PrimaryRoutes  []string
	CurAddr        string
	Relay          string
	RxBytes        int64
	TxBytes        int64
	LastSeen       time.Time
	Online         bool
	Active         bool
	ExitNode       bool
	ExitNodeOption bool
	KeyExpiry      *time.Time
	Expired        bool
}

// rawPrefs is the part of "tailscale debug prefs" read here.
type rawPrefs struct {
	RouteAll               bool
	ExitNodeID             string
	ExitNodeIP             string
	ExitNodeAllowLANAccess bool
	CorpDNS                bool
	RunSSH                 bool
	WantRunning            bool
	LoggedOut              bool
	ShieldsUp              bool
	Hostname               string
	AdvertiseRoutes        []string
}

// exitRoutes are the routes an exit node advertises.
var exitRoutes = []string{"0.0.0.0/0", "::/0"}

// State is Tailscale here as it is now.
func (m *Manager) State(ctx context.Context) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, _, _ := m.stateLocked(ctx)
	return st
}

// stateLocked reads Tailscale, and gives what it read for a change to work from.
func (m *Manager) stateLocked(ctx context.Context) (State, *rawStatus, *rawPrefs) {
	st := State{Peers: []Node{}, Health: []string{}, Settings: Current{AdvertiseRoutes: []string{}}, Now: m.now().Unix()}
	if p := m.pending; p != nil {
		st.Pending = &Pending{Until: p.until.Unix(), What: p.what}
	}
	// The settings are read alongside the status: the page waits on both, and each is a round trip
	// to tailscaled.
	type read struct {
		out string
		err error
	}
	prefsRead := make(chan read, 1)
	go func() {
		out, err := m.run(ctx, 15*time.Second, "debug", "prefs")
		prefsRead <- read{out, err}
	}()
	out, err := m.run(ctx, 15*time.Second, "status", "--json")
	if errors.Is(err, errNotInstalled) {
		st.Problem = errNotInstalled.Error() + "."
		return st, nil, nil
	}
	st.Installed = true
	var raw rawStatus
	if jerr := json.Unmarshal([]byte(jsonPart(out)), &raw); jerr != nil || raw.BackendState == "" {
		st.Problem = "tailscaled is not running"
		if err != nil {
			st.Problem = notRunning(out, err)
		}
		return st, nil, nil
	}
	st.Running = raw.BackendState != "NoState"
	st.Backend = raw.BackendState
	st.AuthURL = raw.AuthURL
	st.Version, _, _ = strings.Cut(raw.Version, "-")
	st.Health = append(st.Health, raw.Health...)
	if t := raw.CurrentTailnet; t != nil {
		st.Tailnet = t.Name
		if t.MagicDNSEnabled {
			st.MagicDNS = t.MagicDNSSuffix
		}
	}
	user := func(id int64) string {
		u, ok := raw.User[strconv.FormatInt(id, 10)]
		if !ok {
			return ""
		}
		return cmp.Or(u.LoginName, u.DisplayName)
	}
	if raw.Self != nil && raw.Self.ID != "" {
		n := nodeOf(raw.Self, user)
		st.Self = &n
	}
	for _, p := range raw.Peer {
		if p != nil {
			st.Peers = append(st.Peers, nodeOf(p, user))
		}
	}
	slices.SortFunc(st.Peers, func(a, b Node) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	pr := <-prefsRead
	pout, err := pr.out, pr.err
	var prefs rawPrefs
	if err != nil || json.Unmarshal([]byte(jsonPart(pout)), &prefs) != nil {
		st.Problem = "Could not read Tailscale's settings"
		if err != nil {
			st.Problem += ": " + err.Error()
		}
		return st, &raw, nil
	}
	switch raw.BackendState {
	case "Stopped", "Starting", "Running", "NeedsMachineAuth":
		st.LoggedIn = !prefs.LoggedOut
	}
	st.Settings = currentOf(&prefs, &raw)
	return st, &raw, &prefs
}

// jsonPart is the JSON in tailscale's output, past any warning printed before it.
func jsonPart(out string) string {
	if i := strings.IndexByte(out, '{'); i > 0 {
		return out[i:]
	}
	return out
}

// notRunning says why tailscaled did not answer, in words.
func notRunning(out string, err error) string {
	msg := err.Error() + " " + out
	if strings.Contains(msg, "failed to connect") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such file") || strings.Contains(msg, "is tailscaled running") {
		return "Tailscale is installed, but tailscaled is not running. Start it (systemctl enable --now tailscaled) on the System services page."
	}
	return "tailscaled did not answer: " + err.Error()
}

func nodeOf(p *rawPeer, user func(int64) string) Node {
	n := Node{
		ID:             p.ID,
		Name:           p.HostName,
		DNSName:        strings.TrimSuffix(p.DNSName, "."),
		OS:             p.OS,
		IPs:            append([]string{}, p.TailscaleIPs...),
		User:           user(p.UserID),
		Tags:           p.Tags,
		Online:         p.Online,
		Active:         p.Active,
		Relay:          p.Relay,
		Direct:         p.CurAddr,
		RX:             p.RxBytes,
		TX:             p.TxBytes,
		ExitNode:       p.ExitNode,
		ExitNodeOption: p.ExitNodeOption,
		Expired:        p.Expired,
	}
	if len(p.Tags) > 0 {
		n.User = "" // tagged machines belong to the tailnet, not whoever added them
	}
	for _, r := range p.PrimaryRoutes {
		if !slices.Contains(exitRoutes, r) {
			n.Routes = append(n.Routes, r)
		}
	}
	if !p.Online && !p.LastSeen.IsZero() && p.LastSeen.Year() > 1 {
		n.LastSeen = p.LastSeen.Unix()
	}
	if p.KeyExpiry != nil && !p.KeyExpiry.IsZero() {
		n.KeyExpiry = p.KeyExpiry.Unix()
	}
	return n
}

func currentOf(p *rawPrefs, raw *rawStatus) Current {
	c := Current{
		AcceptRoutes:     p.RouteAll,
		AcceptDNS:        p.CorpDNS,
		SSH:              p.RunSSH,
		ShieldsUp:        p.ShieldsUp,
		ExitNodeAllowLAN: p.ExitNodeAllowLANAccess,
		Hostname:         p.Hostname,
		AdvertiseRoutes:  []string{},
		ExitNode:         p.ExitNodeID,
	}
	exits := 0
	for _, r := range p.AdvertiseRoutes {
		if slices.Contains(exitRoutes, r) {
			exits++
			continue
		}
		c.AdvertiseRoutes = append(c.AdvertiseRoutes, r)
	}
	c.AdvertiseExitNode = exits == len(exitRoutes)
	if c.ExitNode == "" && p.ExitNodeIP != "" {
		// Set by address: name the machine that has it, when it is on the tailnet.
		c.ExitNode = p.ExitNodeIP
		if peer := peerByAddr(raw, p.ExitNodeIP); peer != nil {
			c.ExitNode = peer.ID
		}
	}
	return c
}

func peerByAddr(raw *rawStatus, addr string) *rawPeer {
	if raw == nil {
		return nil
	}
	for _, p := range raw.Peer {
		if p != nil && slices.Contains(p.TailscaleIPs, addr) {
			return p
		}
	}
	return nil
}

// exitAddr is the address to give tailscale for an exit node set by ID: the machine's IPv4 address,
// else its first. Empty when it is not on the tailnet.
func exitAddr(raw *rawStatus, prefs *rawPrefs) string {
	if prefs.ExitNodeIP != "" {
		return prefs.ExitNodeIP
	}
	if prefs.ExitNodeID == "" || raw == nil {
		return ""
	}
	for _, p := range raw.Peer {
		if p != nil && p.ID == prefs.ExitNodeID {
			return firstAddr(p)
		}
	}
	return ""
}

func firstAddr(p *rawPeer) string {
	for _, ip := range p.TailscaleIPs {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			return ip
		}
	}
	if len(p.TailscaleIPs) > 0 {
		return p.TailscaleIPs[0]
	}
	return ""
}

// ---------- changing ----------

// OverTailscale says whether an address is one Tailscale hands out: someone asking from there is
// reaching this machine over the tailnet.
func OverTailscale(addr string) bool {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return netip.MustParsePrefix("100.64.0.0/10").Contains(a) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a)
}

// errOverTailscale is a change that would cut off whoever asked for it, asked for over Tailscale.
func errOverTailscale(what string) error {
	return fmt.Errorf("you are reaching this machine over Tailscale, so %s would cut you off with no way to undo it from here; do it from the local network or another way in", what)
}

// Up connects to the tailnet again after Down.
func (m *Manager) Up(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, _, prefs := m.stateLocked(ctx)
	if err := usable(st, prefs); err != nil {
		return err
	}
	if !st.LoggedIn {
		return errors.New("log in to a tailnet first")
	}
	if st.Backend == "Running" {
		return nil
	}
	// No flags: "tailscale up" alone brings a logged-in machine back up as it was set.
	if _, err := m.run(ctx, 20*time.Second, "up"); err != nil {
		return fmt.Errorf("tailscale up: %v", err)
	}
	log.Info("Connected to the tailnet")
	return nil
}

// Down disconnects from the tailnet, keeping the login. via is who asked.
func (m *Manager) Down(ctx context.Context, via string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if OverTailscale(via) {
		return errOverTailscale("disconnecting")
	}
	st, _, prefs := m.stateLocked(ctx)
	if err := usable(st, prefs); err != nil {
		return err
	}
	if _, err := m.run(ctx, 20*time.Second, "down", "--accept-risk=lose-ssh"); err != nil {
		return fmt.Errorf("tailscale down: %v", err)
	}
	m.pending = nil
	log.Info("Disconnected from the tailnet")
	return nil
}

// Logout logs out of the tailnet, and expires this machine's key. via is who asked.
func (m *Manager) Logout(ctx context.Context, via string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if OverTailscale(via) {
		return errOverTailscale("logging out")
	}
	st, _, prefs := m.stateLocked(ctx)
	if err := usable(st, prefs); err != nil {
		return err
	}
	if _, err := m.run(ctx, 20*time.Second, "logout"); err != nil {
		return fmt.Errorf("tailscale logout: %v", err)
	}
	m.pending = nil
	log.Info("Logged out of the tailnet")
	return nil
}

// authKey is what an auth key looks like: tskey-auth-…, or an OAuth client secret (tskey-client-…).
var authKey = regexp.MustCompile(`^tskey-[A-Za-z0-9-]+(\?[A-Za-z0-9=&_-]*)?$`)

// Login logs in to a tailnet: with an auth key, at once; without one, tailscaled gives a link to
// open (State's AuthURL) and finishes by itself once someone logs in there.
func (m *Manager) Login(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, _, prefs := m.stateLocked(ctx)
	if err := usable(st, prefs); err != nil {
		return err
	}
	if st.LoggedIn {
		return errors.New("already logged in; log out first to use another account")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		// tailscale prints the link and waits for the login, which tailscaled carries on with
		// regardless; the wait is cut short, and the link read back from its status.
		wait := cmp.Or(m.LoginWait, 5*time.Second)
		_, err := m.run(ctx, wait+10*time.Second, "login", "--timeout="+wait.String())
		if err != nil && !strings.Contains(err.Error(), "timeout") {
			return fmt.Errorf("tailscale login: %v", err)
		}
		return nil
	}
	if !authKey.MatchString(key) {
		return errors.New("that is not an auth key; they start with tskey-")
	}
	// The key goes in a file only root can read, so it is never on a command line for ps to show.
	f, err := os.CreateTemp("", "nightjar-tailscale-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, werr := f.WriteString(key)
	if cerr := f.Close(); werr != nil || cerr != nil {
		return errors.New("could not hand the key to tailscale")
	}
	if _, err := m.run(ctx, 25*time.Second, "login", "--auth-key=file:"+f.Name(), "--timeout=20s"); err != nil {
		return fmt.Errorf("tailscale login: %v", err)
	}
	log.Info("Logged in to the tailnet with an auth key")
	return nil
}

// usable says why Tailscale cannot be changed, if it cannot.
func usable(st State, prefs *rawPrefs) error {
	if !st.Installed || !st.Running || prefs == nil {
		return errors.New(strings.TrimSuffix(cmp.Or(st.Problem, "Tailscale is not running"), "."))
	}
	return nil
}

// hostname is what Tailscale takes as a machine's name.
var hostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Set changes settings. Using an exit node, and shields up, go back by themselves unless kept with
// Keep in time.
func (m *Manager) Set(ctx context.Context, s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, raw, prefs := m.stateLocked(ctx)
	if err := usable(st, prefs); err != nil {
		return err
	}
	if !st.LoggedIn {
		return errors.New("log in to a tailnet first")
	}
	cur := st.Settings
	var args []string
	flag := func(name string, v bool) { args = append(args, fmt.Sprintf("--%s=%t", name, v)) }
	if s.AcceptRoutes != nil {
		flag("accept-routes", *s.AcceptRoutes)
	}
	if s.AcceptDNS != nil {
		flag("accept-dns", *s.AcceptDNS)
	}
	if s.SSH != nil {
		flag("ssh", *s.SSH)
		if !*s.SSH {
			args = append(args, "--accept-risk=lose-ssh")
		}
	}
	if s.Hostname != nil {
		name := strings.TrimSpace(*s.Hostname)
		if name != "" && !hostname.MatchString(name) {
			return errors.New("a machine's name is letters, digits and dashes, up to 63 of them, not starting or ending with a dash")
		}
		args = append(args, "--hostname="+name)
	}
	if s.AdvertiseExitNode != nil || s.AdvertiseRoutes != nil {
		// Both go together: tailscale keeps them in one list.
		exit, routes := cur.AdvertiseExitNode, cur.AdvertiseRoutes
		if s.AdvertiseExitNode != nil {
			exit = *s.AdvertiseExitNode
		}
		if s.AdvertiseRoutes != nil {
			var err error
			if routes, err = cleanRoutes(*s.AdvertiseRoutes); err != nil {
				return err
			}
		}
		flag("advertise-exit-node", exit)
		args = append(args, "--advertise-routes="+strings.Join(routes, ","))
	}

	// What can cut the dashboard off, and what puts it back.
	var risky []string
	revert := []string{}
	exit, allowLAN := cur.ExitNode, cur.ExitNodeAllowLAN
	if s.ExitNode != nil {
		want := strings.TrimSpace(*s.ExitNode)
		addr := ""
		if want != "" {
			peer := findPeer(raw, want)
			switch {
			case peer == nil:
				return errors.New("no machine on the tailnet by that name")
			case !peer.ExitNodeOption:
				return fmt.Errorf("%s does not offer to be an exit node; turn that on there, and approve it in Tailscale's admin console", peer.HostName)
			}
			addr, want = firstAddr(peer), peer.ID
		}
		args = append(args, "--exit-node="+addr)
		exit = want
	}
	if s.ExitNodeAllowLAN != nil {
		flag("exit-node-allow-lan-access", *s.ExitNodeAllowLAN)
		allowLAN = *s.ExitNodeAllowLAN
	}
	if exit != "" && (exit != cur.ExitNode || (!allowLAN && cur.ExitNodeAllowLAN)) {
		risky = append(risky, "the exit node")
		revert = append(revert, "--exit-node="+exitAddr(raw, prefs), fmt.Sprintf("--exit-node-allow-lan-access=%t", cur.ExitNodeAllowLAN))
	}
	if s.ShieldsUp != nil {
		flag("shields-up", *s.ShieldsUp)
		if *s.ShieldsUp && !cur.ShieldsUp {
			risky = append(risky, "shields up")
			revert = append(revert, "--shields-up=false")
		}
	}
	if len(args) == 0 {
		return nil
	}
	if _, err := m.run(ctx, 20*time.Second, append([]string{"set"}, args...)...); err != nil {
		return fmt.Errorf("tailscale set: %v", err)
	}
	if len(risky) > 0 {
		until := m.now().Add(cmp.Or(m.KeepWithin, 2*time.Minute))
		if p := m.pending; p != nil {
			// Going back undoes everything since the last kept state, so what the change still
			// waiting would put back wins over this one's own undo.
			merged := slices.Clone(p.revert)
			for _, f := range revert {
				name, _, _ := strings.Cut(f, "=")
				if !slices.ContainsFunc(merged, func(g string) bool { return strings.HasPrefix(g, name+"=") }) {
					merged = append(merged, f)
				}
			}
			revert, risky = merged, mergeWhat(p.what, risky)
		}
		m.pending = &pending{until: until, what: strings.Join(risky, " and "), revert: revert}
		log.Info("Tailscale change made; it goes back unless kept", "what", m.pending.what, "until", until.Format(time.TimeOnly))
	}
	return nil
}

func mergeWhat(was string, now []string) []string {
	out := strings.Split(was, " and ")
	for _, w := range now {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// findPeer is the machine on the tailnet with an ID, address or name.
func findPeer(raw *rawStatus, want string) *rawPeer {
	if raw == nil {
		return nil
	}
	for _, p := range raw.Peer {
		if p != nil && (p.ID == want || slices.Contains(p.TailscaleIPs, want)) {
			return p
		}
	}
	for _, p := range raw.Peer {
		if p != nil && strings.EqualFold(p.HostName, want) {
			return p
		}
	}
	return nil
}

// cleanRoutes checks subnets to route, and writes them as Tailscale does.
func cleanRoutes(in []string) ([]string, error) {
	out := []string{}
	if len(in) > 64 {
		return nil, errors.New("that is more subnets than one machine should route")
	}
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		p, err := netip.ParsePrefix(r)
		if err != nil {
			return nil, fmt.Errorf("%q is not a subnet; write one like 192.168.1.0/24", r)
		}
		if p.Bits() == 0 {
			return nil, errors.New("to route all internet traffic, offer to be an exit node instead")
		}
		if p.Masked() != p {
			return nil, fmt.Errorf("%s has host bits set; the subnet is %s", r, p.Masked())
		}
		if !slices.Contains(out, p.String()) {
			out = append(out, p.String())
		}
	}
	return out, nil
}

// Keep keeps the change waiting to be kept (keep), or puts it back now.
func (m *Manager) Keep(ctx context.Context, keep bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return errors.New("nothing is waiting to be kept")
	}
	if keep {
		log.Info("Tailscale change kept", "what", m.pending.what)
		m.pending = nil
		return nil
	}
	return m.revertLocked(ctx)
}

// revertLocked undoes the change waiting to be kept.
func (m *Manager) revertLocked(ctx context.Context) error {
	p := m.pending
	if p == nil {
		return nil
	}
	if _, err := m.run(ctx, 20*time.Second, append([]string{"set"}, p.revert...)...); err != nil {
		return fmt.Errorf("could not put %s back: %v", p.what, err)
	}
	m.pending = nil
	log.Info("Tailscale change put back", "what", p.what)
	return nil
}

// Lapse puts back a change not kept in time.
func (m *Manager) Lapse(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil || m.now().Before(m.pending.until) {
		return
	}
	log.Warn("Tailscale change not kept in time; putting it back", "what", m.pending.what)
	if err := m.revertLocked(ctx); err != nil {
		log.Warn("Could not put the Tailscale change back; trying again", "err", err)
	}
}

// Run puts back changes not kept in time until ctx ends, and one still waiting when it does.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(m.CheckEvery, 5*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if m.pending != nil {
				log.Warn("Stopping while a Tailscale change waits to be kept; putting it back", "what", m.pending.what)
				stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				m.revertLocked(stop)
				cancel()
			}
			m.mu.Unlock()
			return
		case <-t.C:
			m.Lapse(ctx)
		}
	}
}
