package ddos

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attack is one attack from start to end: the methods it used at their strongest, the most traffic it
// brought, and the addresses most involved.
type Attack struct {
	ID      string    `json:"id"`
	Start   float64   `json:"start"`
	End     float64   `json:"end,omitempty"` // 0 while it goes on
	Methods []Finding `json:"methods"`       // each at its peak
	Kinds   []Kind    `json:"kinds"`         // the exact types, from sampled packets and the web log
	PeakRx  float64   `json:"peak_rx_bits"`
	PeakPPS float64   `json:"peak_rx_packets"`
	Sources []Source  `json:"sources"`
}

// View is the watcher's state for the dashboard: calm or under attack, what each method reads now,
// what is running that explains traffic, and the attacks seen.
type View struct {
	Attacking bool      `json:"attacking"`
	Current   *Attack   `json:"current,omitempty"`
	Findings  []Finding `json:"findings"` // this moment's, expected ones included
	Busy      Busy      `json:"busy"`
	RxBits    float64   `json:"rx_bits"`
	RxPackets float64   `json:"rx_packets"`
	Learning  bool      `json:"learning"` // still learning what is normal; only the floors apply
	History   []Attack  `json:"history"`  // newest first
	Methods   []Method  `json:"methods"`
	// Defense is what is being done about it, when the watcher defends.
	Defense *DefenseView `json:"defense,omitempty"`
}

// Watcher samples every Interval and keeps track of attacks.
type Watcher struct {
	Interval time.Duration
	// WebLogs are web server access logs to count requests in (nginx's combined format).
	WebLogs []string
	// Busy says what is running that explains traffic; nil reads /proc.
	Busy func() Busy
	// OnStart and OnEnd hear when an attack begins and ends (with its full record).
	OnStart, OnEnd func(Attack)
	// Proc is where /proc is, for tests.
	Proc string
	// Defender, when set, answers attacks in the firewall.
	Defender *Defender

	mu      sync.Mutex
	base    Baselines
	prev    Counters
	prevAt  time.Time
	tails   []*LogTail
	streak  int // attacking samples in a row (or, negative, calm ones)
	current *Attack
	history []Attack
	view    View
	sources map[string]*Source
	// kinds are the exact types the last packet sample showed; capturing is whether one is being
	// taken, and captured when the last was.
	kinds     []Kind
	capturing bool
	captured  time.Time
}

// sampleEvery is how often packets are sampled while an attack goes on.
const sampleEvery = 30 * time.Second

// maxHistory is how many attacks the watcher remembers.
const maxHistory = 30

// maxSources is how many addresses an attack keeps track of; beyond it those that did the least are
// forgotten, so a long attack from ever-changing addresses does not grow without end.
const maxSources = 2000

// Restore gives the watcher the attacks it saw before a restart.
func (w *Watcher) Restore(history []Attack) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.history = slices.Clone(history)
}

// History is the attacks seen, newest first, for keeping.
func (w *Watcher) History() []Attack {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.history)
}

// View is the latest state.
func (w *Watcher) View() View {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.view
	v.History = slices.Clone(w.history)
	if w.current != nil {
		c := *w.current
		v.Current = &c
	}
	if v.Findings == nil {
		v.Findings = []Finding{}
	}
	if v.History == nil {
		v.History = []Attack{}
	}
	v.Methods = Methods
	if w.Defender != nil {
		d := w.Defender.View()
		v.Defense = &d
	}
	return v
}

// Run samples until ctx ends, then takes the defender's rules away: nothing would watch them after,
// to lift its blocks or undo its measures once the attack is over.
func (w *Watcher) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	for _, p := range w.WebLogs {
		w.tails = append(w.tails, &LogTail{Path: p})
	}
	if w.Defender != nil {
		w.Defender.Start(ctx)
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.sample(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			if w.Defender != nil {
				w.Defender.Stop(context.WithoutCancel(ctx))
			}
			return
		case now := <-t.C:
			w.sample(ctx, now)
		}
	}
}

func (w *Watcher) proc() string {
	if w.Proc != "" {
		return w.Proc
	}
	return "/proc"
}

func (w *Watcher) sample(ctx context.Context, now time.Time) {
	iface := defaultInterface(w.proc())
	c := readCounters(iface)
	table := readTable(w.proc())
	requests := 0
	web := NewWebStats()
	for _, t := range w.tails {
		n, _ := t.Requests(table.BySource, web)
		requests += n
	}
	busy := Busy{}
	if w.Busy != nil {
		busy = w.Busy()
	} else {
		busy = ReadBusy(w.proc())
	}
	w.Step(now, c, table, requests, linkBits(iface), busy)
	w.mu.Lock()
	start := w.streak > 0 && !w.capturing && now.Sub(w.captured) >= sampleEvery
	if start {
		w.capturing = true
	}
	w.mu.Unlock()
	if start {
		go w.sampleKinds(iface, web, table.Web)
	}
	if w.Defender != nil {
		s := w.Situation(table, requests)
		s.OpenUDP = openUDP(w.proc())
		s.Conntrack, s.SynCookiesOff = tracking(w.proc())
		s.Upstream = upstream(w.proc())
		w.Defender.Update(ctx, s)
	}
}

// Situation is what the defender needs to know of this moment: whether an attack is under way, how
// it is made, and the busiest addresses with their requests over the last interval.
func (w *Watcher) Situation(table Table, requests int) Situation {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Situation{Attacking: w.current != nil, Interval: w.Interval.Seconds(), Requests: requests}
	for _, f := range w.view.Findings {
		if f.Expected == "" {
			s.Methods = append(s.Methods, f.Method)
		}
	}
	s.Kinds = w.kinds
	if w.current != nil && len(w.current.Kinds) > 0 {
		s.Kinds = mergeKinds(w.current.Kinds, w.kinds)
	}
	s.Sources, _ = topSources(table, 200)
	return s
}

// sampleKinds reads a second of packet headers and works out the exact kinds of flood, adding what
// the web log shows of an HTTP flood.
func (w *Watcher) sampleKinds(iface string, web *WebStats, webConns int) {
	var kinds []Kind
	if p, err := Capture(iface, time.Second, 20000); err == nil {
		kinds = p.Kinds()
	}
	kinds = append(kinds, HTTPKinds(web, webConns)...)
	w.SetKinds(kinds)
}

// SetKinds records the exact kinds a sample showed, for the attack under way (or about to start).
func (w *Watcher) SetKinds(kinds []Kind) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capturing, w.captured = false, time.Now()
	if len(kinds) == 0 {
		return
	}
	w.kinds = kinds
	if w.current != nil {
		w.current.Kinds = mergeKinds(w.current.Kinds, kinds)
	}
}

// mergeKinds keeps each kind at the largest share it has had.
func mergeKinds(have, more []Kind) []Kind {
	out := slices.Clone(have)
	for _, k := range more {
		if i := slices.IndexFunc(out, func(h Kind) bool { return h.ID == k.ID }); i >= 0 {
			if k.Share > out[i].Share {
				out[i] = k
			}
			continue
		}
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b Kind) int {
		switch {
		case a.Share > b.Share:
			return -1
		case a.Share < b.Share:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// HTTPKinds names an HTTP flood by what its requests ask for and how they arrive. web is an
// interval's requests from the log; webConns the connections open to the web ports now.
func HTTPKinds(web *WebStats, webConns int) []Kind {
	if web == nil {
		return nil
	}
	var out []Kind
	add := func(id, name, detail, how string, share float64) {
		out = append(out, Kind{ID: id, Name: name, Detail: detail, How: how, Share: min(1, share)})
	}
	total := float64(web.Requests)
	// Slow requests: connections held open, sending a request too slowly to finish. They make few
	// log lines (nginx writes 408 when it gives up on one), so the open connections tell it too.
	slow := web.Statuses[408] + web.ByRequest["- -"]
	if webConns >= 1000 && float64(web.Requests) < float64(webConns)/20 {
		add("http_slowloris", "Slowloris", fmt.Sprintf("%d connections, few requests", webConns),
			"Opens many connections to the web server and sends each request a byte at a time, so they never finish and the server runs out of room.", 1)
	} else if total >= 100 && float64(slow) >= 0.3*total {
		name, how := "Slowloris", "Sends each request a byte at a time, so it never finishes; the server waits on each until it gives up."
		if topMethod(web) == "POST" {
			name, how = "Slow POST (R.U.D.Y.)", "Starts uploads and sends the body a byte at a time, holding each connection open for as long as the server waits."
		}
		add("http_slow", name, fmt.Sprintf("%.0f%% timed out", 100*float64(slow)/total), how, float64(slow)/total)
	}
	if total < 100 {
		return out
	}
	top, most := topOf(web.ByRequest)
	method, path, _ := strings.Cut(top, " ")
	share := float64(most) / total
	notFound := float64(web.Statuses[404]) / total
	denied := float64(web.Statuses[401]+web.Statuses[403]) / total
	switch {
	case web.Queries[path] >= 100 && float64(web.Queries[path]) >= 0.5*float64(most):
		add("http_cache_bust", "Cache-busting flood", "on "+path+", with random queries",
			"Asks for one page with a different random query string each time, so no cache can answer and every request reaches the app.", share)
	case strings.Contains(path, "xmlrpc.php") && share >= 0.3:
		add("http_xmlrpc", "WordPress XML-RPC flood", "on "+path,
			"Calls WordPress's XML-RPC endpoint over and over, often to amplify through pingbacks or to try passwords in bulk.", share)
	case method == "POST" && share >= 0.3 && denied >= 0.3:
		add("http_login", "Login flood", "on "+path+", turned away",
			"Posts usernames and passwords to a sign-in page at speed (credential stuffing), which also loads the server with password checks.", share)
	case share >= 0.3:
		add("http_one", "HTTP "+method+" flood", "on "+path,
			"The same request over and over, often a page that is slow to build (a search, a login), to keep the apps behind it busy.", share)
	case notFound >= 0.5:
		add("http_random", "Random URL flood", fmt.Sprintf("%.0f%% not found", notFound*100),
			"Asks for pages at random, which do not exist, so nothing can be cached and the server looks up each one.", notFound)
	default:
		add("http_many", "HTTP flood (many pages)", "",
			"Requests spread over many pages, to look like real visitors while keeping the web server busy.", 1)
	}
	// One user agent, or none, behind most of it marks a single tool.
	if agent, n := topOf(web.Agents); float64(n) >= 0.8*total {
		detail := "every request says it is " + agent
		if agent == "" {
			detail = "no user agent at all"
		}
		out[len(out)-1].Detail = strings.TrimPrefix(out[len(out)-1].Detail+"; "+detail, "; ")
	}
	return out
}

func topOf(m map[string]int) (string, int) {
	top, most := "", 0
	for k, n := range m {
		if n > most || (n == most && k < top) {
			top, most = k, n
		}
	}
	return top, most
}

func topMethod(web *WebStats) string {
	byMethod := map[string]int{}
	for req, n := range web.ByRequest {
		m, _, _ := strings.Cut(req, " ")
		byMethod[m] += n
	}
	m, _ := topOf(byMethod)
	return m
}

// Step takes one reading: the counters' totals, the connection table and the web requests logged
// since the last. Run calls it; tests call it directly.
func (w *Watcher) Step(now time.Time, c Counters, table Table, requests int, link float64, busy Busy) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.prevAt.IsZero() {
		w.prev, w.prevAt = c, now
		return
	}
	dt := now.Sub(w.prevAt).Seconds()
	if dt <= 0 {
		return
	}
	rate := func(cur, old uint64) float64 {
		if cur < old {
			return 0 // the counter wrapped or reset
		}
		return float64(cur-old) / dt
	}
	s := Sample{
		At:          float64(now.Unix()),
		SynCookies:  rate(c.SynCookies, w.prev.SynCookies),
		ListenDrops: rate(c.ListenDrops, w.prev.ListenDrops),
		SynRecv:     table.SynRecv,
		UDPIn:       rate(c.UDPIn, w.prev.UDPIn),
		UDPNoPorts:  rate(c.UDPNoPorts, w.prev.UDPNoPorts),
		ICMPEchos:   rate(c.ICMPEchos, w.prev.ICMPEchos),
		Established: table.Established,
		Requests:    float64(requests) / dt,
		RxPackets:   rate(c.RxPackets, w.prev.RxPackets),
		RxBits:      rate(c.RxBytes, w.prev.RxBytes) * 8,
		LinkBits:    link,
	}
	w.prev, w.prevAt = c, now
	s.TopSources, s.MaxPerSource = topSources(table, 10)

	findings := w.base.Detect(s, busy)
	attacking := Attacking(findings)
	if len(findings) == 0 {
		w.base.learn(s) // only calm moments teach what normal is
	}
	w.view = View{Attacking: w.current != nil, Findings: findings, Busy: busy, RxBits: s.RxBits, RxPackets: s.RxPackets, Learning: w.base.n < learnAfter}

	switch {
	case attacking && w.streak < 0:
		w.streak = 1
	case attacking:
		w.streak++
	case w.streak > 0:
		w.streak = -1
	default:
		w.streak--
	}
	if attacking {
		if w.current == nil && w.streak >= 2 {
			w.current = &Attack{ID: strconv.FormatInt(now.UnixMilli(), 36), Start: s.At}
			w.sources = map[string]*Source{}
		}
		if w.current != nil {
			w.record(s, findings)
		}
	}
	if w.current != nil && w.streak <= -6 {
		w.current.End = s.At
		ended := *w.current
		w.history = append([]Attack{ended}, w.history...)
		if len(w.history) > maxHistory {
			w.history = w.history[:maxHistory]
		}
		w.current, w.kinds = nil, nil
		if w.OnEnd != nil {
			go w.OnEnd(ended)
		}
	}
	w.view.Attacking = w.current != nil
}

// record adds a sample to the attack under way: each method's peak and the addresses taking part.
func (w *Watcher) record(s Sample, findings []Finding) {
	a := w.current
	first := len(a.Methods) == 0
	if first && len(w.kinds) > 0 {
		a.Kinds = slices.Clone(w.kinds)
	}
	for _, f := range findings {
		if f.Expected != "" {
			continue
		}
		i := slices.IndexFunc(a.Methods, func(m Finding) bool { return m.Method == f.Method })
		if i < 0 {
			a.Methods = append(a.Methods, f)
		} else if f.Value > a.Methods[i].Value {
			a.Methods[i] = f
		}
	}
	a.PeakRx, a.PeakPPS = max(a.PeakRx, s.RxBits), max(a.PeakPPS, s.RxPackets)
	for _, src := range s.TopSources {
		got := w.sources[src.Address]
		if got == nil {
			cp := src
			w.sources[src.Address] = &cp
			continue
		}
		got.Established, got.SynRecv = max(got.Established, src.Established), max(got.SynRecv, src.SynRecv)
		got.Requests += src.Requests
	}
	all := make([]Source, 0, len(w.sources))
	for _, src := range w.sources {
		all = append(all, *src)
	}
	slices.SortFunc(all, func(x, y Source) int {
		if d := y.weight() - x.weight(); d != 0 {
			return d
		}
		return strings.Compare(x.Address, y.Address)
	})
	if len(all) > maxSources {
		for _, src := range all[maxSources/2:] {
			delete(w.sources, src.Address)
		}
	}
	a.Sources = all[:min(10, len(all))]
	slices.SortFunc(a.Methods, func(x, y Finding) int { return order(x.Method) - order(y.Method) })
	if first && w.OnStart != nil {
		started := *a
		started.Methods, started.Kinds = slices.Clone(a.Methods), slices.Clone(a.Kinds)
		go w.OnStart(started)
	}
}

// topSources are the addresses with the most connections and requests, and the most one holds.
func topSources(t Table, n int) ([]Source, int) {
	out := make([]Source, 0, len(t.BySource))
	most := 0
	for addr, p := range t.BySource {
		most = max(most, p.Established)
		src := Source{Address: addr, Established: p.Established, SynRecv: p.SynRecv, Requests: p.Requests, SSH: p.SSH}
		if ip := net.ParseIP(addr); ip != nil && isCloudflare(ip) {
			src.Network = "Cloudflare"
		}
		out = append(out, src)
	}
	slices.SortFunc(out, func(x, y Source) int {
		if d := y.weight() - x.weight(); d != 0 {
			return d
		}
		return strings.Compare(x.Address, y.Address)
	})
	return out[:min(n, len(out))], most
}

// defaultInterface is the one the default route leaves by, from /proc/net/route.
func defaultInterface(proc string) string {
	b, err := os.ReadFile(proc + "/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

// linkBits is an interface's speed in bits a second, 0 when it does not say (Wi-Fi, virtual).
func linkBits(iface string) float64 {
	if iface == "" {
		return 0
	}
	b, err := os.ReadFile("/sys/class/net/" + iface + "/speed")
	if err != nil {
		return 0
	}
	mbps, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil || mbps <= 0 {
		return 0
	}
	return mbps * 1e6
}

// Describe puts an attack's methods in words, for alerts: "SYN flood and UDP flood".
func Describe(a Attack) string {
	var names []string
	for _, k := range a.Kinds {
		names = append(names, k.Name)
	}
	for _, f := range a.Methods {
		if len(a.Kinds) > 0 {
			break // the exact kinds say it better
		}
		if i := order(f.Method); i >= 0 {
			names = append(names, Methods[i].Name)
		}
	}
	switch len(names) {
	case 0:
		return "an attack"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// FormatValue says a finding's strength in its method's unit.
func FormatValue(f Finding) string {
	if f.Method == Volumetric {
		return fmt.Sprintf("%.0f%% %s", f.Value*100, Methods[order(f.Method)].Unit)
	}
	i := order(f.Method)
	if i < 0 {
		return strconv.FormatFloat(f.Value, 'f', 0, 64)
	}
	return humanize(f.Value) + " " + Methods[i].Unit
}

func humanize(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e4:
		return fmt.Sprintf("%.0fk", v/1e3)
	case v >= 1e3:
		return fmt.Sprintf("%.1fk", v/1e3)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}
