// Package incidents turns the alert history into incidents: each stretch of trouble, with what
// happened in the minutes before it and during it, and the likely cause. A tool that only checks
// from outside can say a service is down; Nightjar also sees the machine it runs on, so it can say
// why: the kernel ran out of memory, a disk filled, the service behind a check crashed, an update
// had just gone in.
package incidents

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"nightjar/internal/diagnose"
	"nightjar/internal/storage"
)

// Event is one alert in an incident's timeline.
type Event struct {
	ID       int64   `json:"id"`
	TS       float64 `json:"ts"`
	Area     string  `json:"area"`
	Severity string  `json:"severity"`
	Title    string  `json:"title"`
	Body     string  `json:"body,omitempty"`
	Subject  string  `json:"subject,omitempty"`
	// Role is the event's part: before (the lead-up), problem, recovery, action (Nightjar's own
	// repairs), or note (anything else that happened meanwhile).
	Role string `json:"role"`
}

// Cause is a reason for an incident, with how sure Nightjar is of it.
type Cause struct {
	// Kind is memory, disk, hardware, restart, crash, network, change, overload, check or first.
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Confidence string `json:"confidence"` // likely or possibly
	Event      int64  `json:"event,omitempty"`
	// Learned is set when the owner's feedback on earlier incidents shaped it.
	Learned bool `json:"learned,omitempty"`
}

// Affected is one thing that went wrong in an incident.
type Affected struct {
	Subject string   `json:"subject"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // check, service, drive or other
	Down    float64  `json:"down"`
	Up      *float64 `json:"up,omitempty"`
}

// Point is a minute of the machine's readings around an incident.
type Point struct {
	TS   float64  `json:"t"`
	CPU  *float64 `json:"cpu"`
	Mem  *float64 `json:"mem"`
	Disk *float64 `json:"disk"`
}

// Peaks are the highest readings from just before an incident to its end.
type Peaks struct {
	CPU  *float64 `json:"cpu"`
	Mem  *float64 `json:"mem"`
	Disk *float64 `json:"disk"`
}

// Incident is one stretch of trouble.
type Incident struct {
	ID       int64      `json:"id"` // its first problem's alert
	Started  float64    `json:"started"`
	Ended    *float64   `json:"ended"` // nil while something in it is still down
	Severity string     `json:"severity"`
	Title    string     `json:"title"`
	Cause    *Cause     `json:"cause"`
	Also     []Cause    `json:"also"`
	Affected []Affected `json:"affected"`
	Events   []Event    `json:"events"`
	Series   []Point    `json:"series"`
	Peaks    Peaks      `json:"peaks"`
	// What the owner said about it, its notice on the status page, and a draft of one.
	Feedback *Feedback `json:"feedback"`
	Notice   *Notice   `json:"notice"`
	Draft    *Notice   `json:"draft"`
}

const (
	// Join is how close problems must come to belong to one incident.
	Join = 15 * 60.0
	// Lead is how far before an incident its lead-up reaches.
	Lead = 15 * 60.0
	// burst is how soon after an incident starts a problem still counts as part of its start.
	burst = 120.0
	// Quiet is how long something may go without a word before it stops counting as down. Many
	// alerts have no recovery to close them (a drive's warning, a service that kept restarting but
	// never failed, a check deleted or renamed while down), while a check still down re-alerts at
	// least every ALERT_MAX_COOLDOWN (an hour by default): one quiet this long is over or forgotten.
	Quiet = 6 * 3600.0
)

func problem(n storage.Notification) bool {
	// Torrents stall all the time, and a repair that failed belongs to its outage; they are notes.
	// Whatever was raised during planned maintenance was expected.
	return (n.Severity == "warning" || n.Severity == "critical") && n.Area != "torrents" && role(n) != "action" && n.Status != "maintenance"
}

// role is a note's part: action for what Nightjar did about it (its repairs), else note.
func role(n storage.Notification) string {
	if strings.HasPrefix(n.Title, "Nightjar ") || strings.HasPrefix(n.Title, "Repair of ") {
		return "action"
	}
	return "note"
}

func event(n storage.Notification, role string) Event {
	return Event{ID: n.ID, TS: n.TS, Area: n.Area, Severity: n.Severity, Title: n.Title, Body: n.Body, Subject: n.Subject, Role: role}
}

// Label is how a subject reads: "check:plex" is plex, "unit:bazarr.service" is bazarr.
func Label(subject string) (label, kind string) {
	kind, name, ok := strings.Cut(subject, ":")
	if !ok {
		return subject, "other"
	}
	switch kind {
	case "check":
		return name, "check"
	case "unit":
		return strings.TrimSuffix(name, ".service"), "service"
	case "drive":
		return name, "drive"
	}
	return name, "other"
}

var rank = map[string]int{"info": 0, "ok": 0, "warning": 1, "critical": 2}

// Build finds the incidents in notifications (oldest first), reading the machine's minutes in
// samples, newest incident first. Something not yet recovered at now leaves its incident open,
// unless nothing has been heard of it for Quiet.
// What the owner taught it about earlier incidents weighs the causes.
func Build(notes []storage.Notification, samples []storage.Sample, now float64, learned Learned) []Incident {
	var out []*Incident
	var cur *Incident
	// open is what is still down, and the incident it went down in. It outlives that incident: a
	// check that stays down past Join keeps its incident open until it recovers (or goes quiet), and
	// a re-alert about it while it is down belongs to that incident.
	open := map[string]*Incident{}
	holds := func(inc *Incident) bool {
		for _, o := range open {
			if o == inc {
				return true
			}
		}
		return false
	}
	last := 0.0
	used := map[int64]bool{}
	// seen is each open subject's latest problem, and lastOf each incident's latest problem or
	// recovery, so a subject gone quiet can be let go and its incident closed where it went quiet.
	seen := map[string]float64{}
	lastOf := map[*Incident]float64{}
	expire := func(at float64) {
		for subject, inc := range open {
			if at-seen[subject] <= Quiet {
				continue
			}
			delete(open, subject)
			delete(seen, subject)
			if holds(inc) {
				continue
			}
			end := lastOf[inc]
			inc.Ended = &end
			// Notes it took in only because it looked still down belong to no incident.
			kept := inc.Events[:0]
			for _, e := range inc.Events {
				if e.TS > end+Join {
					delete(used, e.ID)
					continue
				}
				kept = append(kept, e)
			}
			inc.Events = kept
		}
	}
	finish := func() {
		if cur == nil {
			return
		}
		if holds(cur) {
			cur.Ended = nil
		} else if cur.Ended == nil {
			// Problems with nothing to recover from (a memory warning) end where they were raised.
			end := last
			cur.Ended = &end
		}
		out = append(out, cur)
		cur = nil
	}
	for _, n := range notes {
		expire(n.TS)
		switch {
		case problem(n):
			inc := open[n.Subject]
			if n.Subject == "" || inc == nil {
				if cur != nil && n.TS-last > Join {
					finish()
				}
				if cur == nil {
					cur = &Incident{ID: n.ID, Started: n.TS, Severity: n.Severity}
				}
				inc = cur
			}
			inc.Events = append(inc.Events, event(n, "problem"))
			if rank[n.Severity] > rank[inc.Severity] {
				inc.Severity = n.Severity
			}
			if n.Subject != "" {
				open[n.Subject], seen[n.Subject] = inc, n.TS
			}
			lastOf[inc] = n.TS
			inc.Ended = nil
			if inc == cur {
				last = n.TS
			}
			used[n.ID] = true
		case n.Severity == "ok" && open[n.Subject] != nil:
			inc := open[n.Subject]
			inc.Events = append(inc.Events, event(n, "recovery"))
			delete(open, n.Subject)
			delete(seen, n.Subject)
			lastOf[inc] = n.TS
			if inc == cur {
				last = n.TS
			}
			if !holds(inc) {
				end := n.TS
				inc.Ended = &end
			}
			used[n.ID] = true
		case cur != nil && (holds(cur) || n.TS-last <= Join):
			cur.Events = append(cur.Events, event(n, role(n)))
			used[n.ID] = true
		}
	}
	expire(now)
	finish()

	result := make([]Incident, 0, len(out))
	for i := len(out) - 1; i >= 0; i-- {
		inc := out[i]
		// The lead-up: what happened shortly before, that no earlier incident holds.
		var before []Event
		for _, n := range notes {
			if n.TS >= inc.Started-Lead && n.TS < inc.Started && !used[n.ID] {
				before = append(before, event(n, "before"))
			}
		}
		inc.Events = append(before, inc.Events...)
		inc.Affected = affected(inc.Events)
		inc.Title = title(inc)
		end := now
		if inc.Ended != nil {
			end = *inc.Ended
		}
		inc.Series, inc.Peaks = around(samples, inc.Started, end)
		causes := explain(inc.Events, inc.Affected, samples, inc.Started, learned)
		if len(causes) > 0 {
			inc.Cause = &causes[0]
			inc.Also = causes[1:min(len(causes), 3)]
		}
		if inc.Also == nil {
			inc.Also = []Cause{}
		}
		result = append(result, *inc)
	}
	return result
}

func affected(events []Event) []Affected {
	var out []Affected
	index := map[string]int{}
	for _, e := range events {
		key := cmp.Or(e.Subject, e.Title)
		switch e.Role {
		case "problem":
			if _, ok := index[key]; ok {
				continue
			}
			label, kind := e.Title, "other"
			if e.Subject != "" {
				label, kind = Label(e.Subject)
			}
			index[key] = len(out)
			out = append(out, Affected{Subject: e.Subject, Label: label, Kind: kind, Down: e.TS})
		case "recovery":
			if i, ok := index[key]; ok {
				t := e.TS
				out[i].Up = &t
			}
		}
	}
	return out
}

func title(inc *Incident) string {
	a := inc.Affected
	switch {
	case len(a) == 0:
		return "Trouble"
	case len(a) == 1:
		for _, e := range inc.Events {
			if e.Role == "problem" {
				return e.Title
			}
		}
	case len(a) == 2:
		return a[0].Label + " and " + a[1].Label
	}
	return fmt.Sprintf("%s, %s and %d more", a[0].Label, a[1].Label, len(a)-2)
}

// around is the machine's minutes from half an hour before start to ten minutes after end, at
// most 240 of them, and the peaks from ten minutes before start to end.
func around(samples []storage.Sample, start, end float64) ([]Point, Peaks) {
	var pts []Point
	var pk Peaks
	peak := func(p **float64, v *float64) {
		if v != nil && (*p == nil || *v > **p) {
			x := *v
			*p = &x
		}
	}
	for _, s := range samples {
		if s.TS >= start-1800 && s.TS <= end+600 {
			pts = append(pts, Point{TS: s.TS, CPU: s.CPU, Mem: s.Mem, Disk: s.Disk})
		}
		if s.TS >= start-600 && s.TS <= end {
			peak(&pk.CPU, cmp.Or(s.CPUMax, s.CPU))
			peak(&pk.Mem, s.Mem)
			peak(&pk.Disk, s.Disk)
		}
	}
	if len(pts) > 240 {
		step := int(math.Ceil(float64(len(pts)) / 240))
		thin := make([]Point, 0, 240)
		for i := 0; i < len(pts); i += step {
			thin = append(thin, pts[i])
		}
		pts = thin
	}
	if pts == nil {
		pts = []Point{}
	}
	return pts, pk
}

var (
	oomRE      = regexp.MustCompile(`(?i)out of memory|oom[- ]kill|killed process`)
	oomProcRE  = regexp.MustCompile(`(?i)killed process \d+ \(([^)]+)\)|oom[- ]kill(?:er|ed)?[^a-z]+(?:process\s+)?([a-z0-9_.-]{2,})`)
	hardwareRE = regexp.MustCompile(`(?i)hardware error|machine check|\bmce\b|i/o error|drive failing|smart.*fail`)
	restartRE  = regexp.MustCompile(`(?i)unexpected(ly)? reboot|rebooted|kernel panic|froze|frozen|stopped responding`)
	diskFullRE = regexp.MustCompile(`(?i)\bfull\b|no space left|out of space`)
	changeRE   = regexp.MustCompile(`(?i)\b(updated|installed|upgraded|removed|restarted|reinstalled)\b`)
	// A container check's own words when it goes down (docker.Client.Check).
	containerDownRE = regexp.MustCompile(`(?i)^container (\S+) (stopped[^:]*(?:: .*)?|keeps restarting[^.]*|is unhealthy[^.]*|restarted \d+ times[^.]*|does not exist)`)
	networkRE       = regexp.MustCompile(`(?i)no route to host|network is unreachable|i/o timeout|timed out|no such host|temporary failure in name resolution`)
)

func ago(seconds float64) string {
	m := int(math.Round(seconds / 60))
	if m <= 1 {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// related reports whether a systemd service and a check are likely the same program: "plexmediaserver"
// runs the check "plex", "sonarr" the check "sonarr-api".
func related(unit, check string) bool {
	u, c := strings.ToLower(unit), strings.ToLower(check)
	if len(u) < 3 || len(c) < 3 {
		return false
	}
	return strings.Contains(u, c) || strings.Contains(c, u)
}

// explain ranks the likely causes of an incident that began at start, strongest first.
func explain(events []Event, hit []Affected, samples []storage.Sample, start float64, learned Learned) []Cause {
	// The start: the lead-up, and the first two minutes of the incident.
	var window []Event
	var first *Event
	for i, e := range events {
		if e.TS <= start+burst {
			window = append(window, e)
		}
		if first == nil && e.Role == "problem" {
			first = &events[i]
		}
	}
	text := func(e Event) string { return e.Title + " " + e.Body }
	var checks []string
	for _, a := range hit {
		if a.Kind == "check" {
			checks = append(checks, a.Label)
		}
	}
	type candidate struct {
		weight int
		Cause
	}
	var found []candidate
	add := func(weight int, kind, confidence, text string, ev int64) {
		if slices.ContainsFunc(found, func(c candidate) bool { return c.Kind == kind }) {
			return
		}
		found = append(found, candidate{weight, Cause{Kind: kind, Text: text, Confidence: confidence, Event: ev}})
	}
	// down lists the checks that went down, for "... took plex down with it".
	down := func() string {
		switch len(checks) {
		case 0:
			return ""
		case 1:
			return checks[0]
		case 2:
			return checks[0] + " and " + checks[1]
		}
		return fmt.Sprintf("%s and %d other checks", checks[0], len(checks)-1)
	}
	with := func(s string) string {
		if d := down(); d != "" {
			return s + ", and " + d + " went down"
		}
		return s
	}

	for _, e := range window {
		t := text(e)
		switch {
		case hardwareRE.MatchString(t) && (e.Area == "system" || e.Area == "storage"):
			add(100, "hardware", "likely", "A hardware fault came first: "+e.Title, e.ID)
		case restartRE.MatchString(t) && e.Area == "system":
			add(95, "restart", "likely", with("The machine restarted or froze ("+e.Title+")"), e.ID)
		case oomRE.MatchString(t):
			proc := "a process"
			if m := oomProcRE.FindStringSubmatch(t); m != nil {
				proc = cmp.Or(m[1], m[2])
			}
			add(90, "memory", "likely", with("The machine ran out of memory and the kernel stopped "+proc+" to free some"), e.ID)
		case e.Area == "storage" && (e.Severity == "critical" || diskFullRE.MatchString(t)) && e.Role != "recovery":
			add(85, "disk", "likely", with(e.Title+", so programs could not write"), e.ID)
		}
	}

	// The machine's own readings in the ten minutes before.
	var mem, disk, cpu *float64
	for _, s := range samples {
		if s.TS >= start-600 && s.TS <= start+60 {
			for _, p := range []struct {
				dst **float64
				v   *float64
			}{{&mem, s.Mem}, {&disk, s.Disk}, {&cpu, cmp.Or(s.CPUMax, s.CPU)}} {
				if p.v != nil && (*p.dst == nil || *p.v > **p.dst) {
					v := *p.v
					*p.dst = &v
				}
			}
		}
	}
	if mem != nil && *mem >= 95 {
		conf := "possibly"
		if *mem >= 98 {
			conf = "likely"
		}
		add(80, "memory", conf, with(fmt.Sprintf("Memory was %.0f%% full just before", *mem)), 0)
	}
	if disk != nil && *disk >= 97 {
		add(78, "disk", "likely", with(fmt.Sprintf("The disk was %.0f%% full just before", *disk)), 0)
	}

	// A service crashing, especially the one behind a check that went down.
	for _, e := range window {
		if e.Area != "services" || e.Role != "problem" || !strings.HasPrefix(e.Subject, "unit:") {
			continue
		}
		unit, _ := Label(e.Subject)
		for _, c := range checks {
			if related(unit, c) {
				add(75, "crash", "likely", fmt.Sprintf("The %s service failed, and the %s check went down with it", unit, c), e.ID)
			}
		}
	}

	// A container check that went down, taking a check of the same program with it.
	for _, e := range window {
		m := containerDownRE.FindStringSubmatch(e.Body)
		if e.Area != "uptime" || e.Role != "problem" || m == nil {
			continue
		}
		self, _ := Label(e.Subject)
		if strings.Contains(m[2], "memory") {
			add(88, "memory", "likely", with("The "+m[1]+" container was killed for using too much memory"), e.ID)
		}
		for _, c := range checks {
			if c != self && related(m[1], c) {
				add(76, "crash", "likely", fmt.Sprintf("The %s container %s, and the %s check went down with it", m[1], m[2], c), e.ID)
			}
		}
	}

	// Many checks failing together point away from any one of them.
	var together []string
	for _, e := range events {
		if e.Role == "problem" && e.Area == "uptime" && e.TS <= start+burst && !slices.Contains(together, e.Subject) {
			together = append(together, e.Subject)
		}
	}
	if len(together) >= 3 {
		add(70, "network", "likely", fmt.Sprintf("%d checks failed within two minutes, so the network or this machine's connection was likely down", len(together)), 0)
	} else if first != nil && first.Area == "uptime" && len(together) >= 2 && networkRE.MatchString(first.Body) {
		add(60, "network", "possibly", "Checks timed out together, which points at the network", first.ID)
	}

	// A change made shortly before.
	for i := len(window) - 1; i >= 0; i-- {
		e := window[i]
		if e.Role == "before" && (e.Severity == "ok" || e.Severity == "info") && (e.Area == "updates" || changeRE.MatchString(e.Title)) {
			add(55, "change", "possibly", fmt.Sprintf("It began %s after: %s", ago(start-e.TS), e.Title), e.ID)
			break
		}
	}

	if cpu != nil && *cpu >= 95 {
		add(45, "overload", "possibly", fmt.Sprintf("The processor was at %.0f%% just before", *cpu), 0)
	}

	if first != nil && first.Area == "uptime" {
		if m := containerDownRE.FindStringSubmatch(first.Body); m != nil {
			add(42, "crash", "possibly", fmt.Sprintf("The %s container %s", m[1], m[2]), first.ID)
		}
	}

	// A service failing on its own, with nothing above to blame.
	for _, e := range window {
		if e.Area == "services" && e.Role == "problem" && strings.HasPrefix(e.Subject, "unit:") {
			unit, _ := Label(e.Subject)
			add(40, "crash", "possibly", with("The "+unit+" service failed first"), e.ID)
			break
		}
	}

	if first != nil && first.Area == "uptime" {
		if f, ok := diagnose.Text(first.Body); ok {
			label, _ := Label(first.Subject)
			add(30, "check", "possibly", fmt.Sprintf("%s: %s. %s", label, f.Title, f.Fix), first.ID)
		}
	}
	if first != nil {
		add(10, "first", "possibly", first.Title+" came first", first.ID)
	}

	// What the owner said about earlier incidents of the same things: a cause they confirmed counts
	// for more, one they rejected for less, and a cause they keep naming is offered even without
	// other signs this time.
	if len(learned) > 0 {
		score := func(kind string) int {
			n := 0
			for _, a := range hit {
				n += learned[cmp.Or(a.Subject, a.Label)][kind]
			}
			return n
		}
		for i := range found {
			if s := score(found[i].Kind); s != 0 {
				found[i].weight += max(-40, min(40, s*8))
				found[i].Learned = s > 0
			}
		}
		for _, a := range hit {
			kind, best := "", 0
			for k, n := range learned[cmp.Or(a.Subject, a.Label)] {
				if n > best || (n == best && k < kind) {
					kind, best = k, n
				}
			}
			if best >= 4 && habit[kind] != "" && !slices.ContainsFunc(found, func(f candidate) bool { return f.Kind == kind }) {
				found = append(found, candidate{50, Cause{Kind: kind, Confidence: "possibly", Learned: true,
					Text: fmt.Sprintf("On this machine, %s going down has usually been %s, going by your feedback on earlier incidents", a.Label, habit[kind])}})
			}
		}
	}

	slices.SortStableFunc(found, func(a, b candidate) int { return b.weight - a.weight })
	out := make([]Cause, len(found))
	for i, c := range found {
		out[i] = c.Cause
	}
	return out
}

// Explain gives the likely cause of a problem being raised now, from the alerts of the last
// minutes, before the alert itself is stored: for "Why" in the alert.
func Explain(notes []storage.Notification, samples []storage.Sample, now storage.Notification, learned Learned) *Cause {
	// The alert is not stored yet; an ID no stored one has tells it apart.
	now.ID = -1
	all := append(slices.Clone(notes), now)
	incs := Build(all, samples, now.TS, learned)
	for _, inc := range incs {
		if !slices.ContainsFunc(inc.Events, func(e Event) bool { return e.ID == now.ID && e.TS == now.TS && e.Role == "problem" }) {
			continue
		}
		// Only a cause beyond the alert itself says anything new.
		if inc.Cause != nil && inc.Cause.Kind != "first" && inc.Cause.Event != now.ID {
			return inc.Cause
		}
		for _, c := range inc.Also {
			if c.Kind != "first" && c.Event != now.ID && c.Kind != "check" {
				return &c
			}
		}
	}
	return nil
}
