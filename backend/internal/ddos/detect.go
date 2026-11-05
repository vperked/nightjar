// Package ddos watches for denial-of-service attacks on this machine and names how they are made:
// a SYN flood, a UDP or ping flood, a flood of connections or of web requests, or traffic enough to
// fill the line. It reads the kernel's own counters every few seconds, learns what is normal here,
// and calls it an attack when a method is both far above normal and above a floor no ordinary day
// reaches.
package ddos

import (
	"cmp"
	"slices"
)

// Methods, as an attack's report names them.
const (
	SYNFlood        = "syn_flood"
	UDPFlood        = "udp_flood"
	ICMPFlood       = "icmp_flood"
	ConnectionFlood = "connection_flood"
	HTTPFlood       = "http_flood"
	Volumetric      = "volumetric"
)

// Method describes one way of attacking, and how this detector sees it.
type Method struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// How says what the attacker does and what it costs, for someone who has not met it before.
	How  string `json:"how"`
	Unit string `json:"unit"`
}

// Methods lists every method in the order a report shows them.
var Methods = []Method{
	{SYNFlood, "SYN flood", "Opens TCP connections and never finishes them, to fill the queue of connections waiting to be accepted so real visitors are turned away.", "per second"},
	{UDPFlood, "UDP flood", "Sprays UDP packets, often at ports nothing listens on, to use up bandwidth and the CPU spent answering them.", "packets/s"},
	{ICMPFlood, "Ping flood", "Sends ping requests faster than they can be answered, to use up bandwidth and CPU.", "pings/s"},
	{ConnectionFlood, "Connection flood", "Opens and holds a great many connections, often from few addresses, until the server runs out of room for more.", "connections"},
	{HTTPFlood, "HTTP flood", "Sends web requests that look ordinary, many times a second, to keep the web server and the apps behind it busy.", "requests/s"},
	{Volumetric, "Volumetric", "Sends more traffic than the line can carry, so everything on it slows or stops.", "of the link"},
}

// Sample is one reading: rates over the interval since the last, and who is connected now.
type Sample struct {
	At           float64 // unix seconds
	SynCookies   float64 // per second
	ListenDrops  float64
	SynRecv      int // half-open connections now
	UDPIn        float64
	UDPNoPorts   float64
	ICMPEchos    float64
	Established  int
	Requests     float64 // web requests per second
	RxPackets    float64
	RxBits       float64
	LinkBits     float64 // the interface's speed, 0 when unknown
	TopSources   []Source
	MaxPerSource int // the most connections one address holds
}

// Source is one address taking part, with what it is doing.
type Source struct {
	Address     string `json:"address"`
	Established int    `json:"established"`
	SynRecv     int    `json:"syn_recv"`
	Requests    int    `json:"requests"`
	// Network names a source that is a proxy in front of this server (Cloudflare): its traffic is many
	// visitors', so blocking it would block them all.
	Network string `json:"network,omitempty"`
	// SSH is an address logged in over SSH, never limited or blocked.
	SSH bool `json:"ssh,omitempty"`
}

func (s Source) weight() int { return s.Established + s.SynRecv + s.Requests }

// Finding is one method seen in a sample: how strong it is, against what counts as an attack.
type Finding struct {
	Method    string  `json:"method"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Normal    float64 `json:"normal"`
	// Expected says what running on the machine explains it ("apt-get is installing"), in which case it
	// is shown but not counted as an attack.
	Expected string `json:"expected,omitempty"`
}

// Attacking says whether any finding is an attack rather than something expected.
func Attacking(fs []Finding) bool {
	return slices.ContainsFunc(fs, func(f Finding) bool { return f.Expected == "" })
}

// floors are what each method must at least reach to count, whatever is normal here: an ordinary
// day on a home server stays well under them.
var floors = map[string]float64{
	SYNFlood:        100, // SYN cookies a second, or 512 half-open connections
	UDPFlood:        20000,
	ICMPFlood:       1000,
	ConnectionFlood: 3000, // or 300 from one address
	HTTPFlood:       300,
	Volumetric:      0.9, // of the link
}

// times is how far above normal a method must be.
const times = 10

// learnAfter is how many samples a baseline needs before multiples of it mean anything; until then
// only the floors apply.
const learnAfter = 12

// Baselines are what is normal on this machine, learned from calm samples.
type Baselines struct {
	n                                      int
	syn, udp, icmp, estab, requests, rxPkt float64
}

const alpha = 0.02 // each calm sample's weight: normal follows a change over a few minutes

func ewma(old, v float64, n int) float64 {
	if n == 0 {
		return v
	}
	return old + alpha*(v-old)
}

func (b *Baselines) learn(s Sample) {
	b.syn = ewma(b.syn, s.SynCookies+s.ListenDrops, b.n)
	b.udp = ewma(b.udp, s.UDPIn, b.n)
	b.icmp = ewma(b.icmp, s.ICMPEchos, b.n)
	b.estab = ewma(b.estab, float64(s.Established), b.n)
	b.requests = ewma(b.requests, s.Requests, b.n)
	b.rxPkt = ewma(b.rxPkt, s.RxPackets, b.n)
	b.n++
}

// threshold is what a value must reach: the floor, or ten times normal once normal is known.
func (b *Baselines) threshold(method string, normal float64) float64 {
	t := floors[method]
	if b.n >= learnAfter {
		t = max(t, normal*times)
	}
	return t
}

// Detect says which methods a sample shows, marking those that what is running explains.
func (b *Baselines) Detect(s Sample, busy Busy) []Finding {
	out := b.detect(s)
	for i, f := range out {
		switch {
		case f.Method == Volumetric && busy.Installing != "":
			out[i].Expected = busy.Installing + " is downloading"
		case f.Method == Volumetric && busy.Torrents != "":
			out[i].Expected = busy.Torrents + " is transferring"
		case f.Method == UDPFlood && busy.Torrents != "" && s.UDPNoPorts < 5000:
			// Torrent traffic reaches the client's own port; a flood mostly hits closed ones.
			out[i].Expected = busy.Torrents + " is transferring (uTP is UDP)"
		}
	}
	return out
}

func (b *Baselines) detect(s Sample) []Finding {
	var out []Finding
	add := func(method string, value, normal float64) {
		if t := b.threshold(method, normal); value >= t {
			out = append(out, Finding{Method: method, Value: value, Threshold: t, Normal: normal})
		}
	}
	syn := s.SynCookies + s.ListenDrops
	if s.SynRecv >= 512 && syn < floors[SYNFlood] {
		// Half-open connections piling up without cookies yet (they are off, or the queue is large).
		out = append(out, Finding{Method: SYNFlood, Value: float64(s.SynRecv), Threshold: 512})
	} else {
		add(SYNFlood, syn, b.syn)
	}
	add(UDPFlood, s.UDPIn, b.udp)
	if s.UDPNoPorts >= 5000 && !slices.ContainsFunc(out, func(f Finding) bool { return f.Method == UDPFlood }) {
		out = append(out, Finding{Method: UDPFlood, Value: s.UDPNoPorts, Threshold: 5000, Normal: 0})
	}
	add(ICMPFlood, s.ICMPEchos, b.icmp)
	if s.MaxPerSource >= 300 {
		out = append(out, Finding{Method: ConnectionFlood, Value: float64(s.MaxPerSource), Threshold: 300})
	} else {
		add(ConnectionFlood, float64(s.Established), b.estab)
	}
	add(HTTPFlood, s.Requests, b.requests)
	if s.LinkBits > 0 && s.RxBits/s.LinkBits >= floors[Volumetric] {
		out = append(out, Finding{Method: Volumetric, Value: s.RxBits / s.LinkBits, Threshold: floors[Volumetric]})
	}
	slices.SortFunc(out, func(a, b Finding) int { return cmp.Compare(order(a.Method), order(b.Method)) })
	return out
}

func order(id string) int {
	return slices.IndexFunc(Methods, func(m Method) bool { return m.ID == id })
}
