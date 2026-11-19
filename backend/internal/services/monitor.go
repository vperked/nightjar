package services

import (
	"cmp"
	"sync"

	"nightjar/internal/diagnose"
	"nightjar/internal/storage"
)

const (
	maxHistory  = 1440
	RecentBeats = 60
)

// Status values.
const (
	StatusUnknown = "unknown"
	StatusUp      = "up"
	StatusDown    = "down"
	StatusPaused  = "paused"
	// StatusMaintenance is a check resting in planned maintenance; it is only ever shown.
	StatusMaintenance = "maintenance"
)

type beat struct {
	ts      float64
	up      bool
	latency *float64
}

// State is one service's status across cycles.
type State struct {
	Spec                Spec
	Status              string
	ConsecutiveFailures int
	LastCheck           float64
	LastChange          *float64
	LastResult          *Result
	runNow              bool // set when the check changed, so it runs on the next cycle
	running             bool // claimed by a check that has not reported back
	stale               bool // the running check was for a type or target since changed: its result is dropped
	history             []beat
	slots               slotRing
}

// Monitor tracks every configured service. It is safe for concurrent use by the
// check loop and the HTTP API.
type Monitor struct {
	mu     sync.RWMutex
	order  []string
	states map[string]*State
	store  *storage.Store
	// heartbeats are what each heartbeat check has heard, by token.
	heartbeats map[string]*Pings
	// renamed is the new name of a check renamed while it ran, by its old name, so the result
	// reported under the old name still lands (and frees the check to run again).
	renamed map[string]string
	// Resting, when set, says whether a check is in planned maintenance at now: it does not run,
	// so it neither alerts nor counts against uptime, and shows as "maintenance".
	Resting func(name string, now float64) bool
}

// NewMonitor builds a monitor; with an available store it resumes each service's last
// known status and recent beats, so a restart neither re-alerts nor hides an outage.
func NewMonitor(specs []Spec, store *storage.Store) *Monitor {
	m := &Monitor{states: map[string]*State{}}
	if store.Available() {
		m.store = store
	}
	for _, spec := range specs {
		m.order = append(m.order, spec.Name)
		m.states[spec.Name] = m.restore(spec)
	}
	return m
}

// restore builds a service's state from its stored history, if any.
func (m *Monitor) restore(spec Spec) *State {
	st := &State{Spec: spec, Status: StatusUnknown, slots: make(slotRing, slotCount)}
	if m.store == nil {
		return st
	}
	for _, c := range m.store.Counts(spec.Name, slotSeconds, slotCount) {
		st.slots.merge(c)
	}
	if isUp, ok := m.store.LastStatus(spec.Name); ok {
		st.Status = StatusDown
		if isUp {
			st.Status = StatusUp
		}
	}
	for _, p := range m.store.Series(spec.Name, 0, RecentBeats) {
		st.history = append(st.history, beat{p.TS, p.Up, p.LatencyMS})
	}
	return st
}

func (m *Monitor) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.states[name]
	return ok
}

// State returns a copy of one service's state (the zero State for an unknown name).
func (m *Monitor) State(name string) State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.states[name]
	if !ok {
		return State{}
	}
	cp := *st
	cp.history = nil
	return cp
}

// defaultRetryInterval is how soon a failed check runs again while it is not yet down, unless
// the check sets its own, so an outage is confirmed after Retries quick re-checks rather than
// Retries whole intervals.
const defaultRetryInterval = 20.0

// due reports whether the check should run at now.
func (st *State) due(now float64) bool {
	wait := st.Spec.Interval
	if st.ConsecutiveFailures > 0 && st.Status != StatusDown {
		wait = min(wait, cmp.Or(st.Spec.RetryInterval, defaultRetryInterval))
	}
	return st.runNow || now-st.LastCheck >= wait
}

// Claimed is one claimed run of a check. Its result goes back with Report, to the check it was claimed
// from, whatever that check is called by then: a name can pass to another check while it runs.
type Claimed struct {
	Spec Spec
	st   *State
}

// Claim returns the active checks that are due and not already running, and marks them running
// until their result is recorded, so a slow check is never started twice.
func (m *Monitor) Claim(now float64) []Spec {
	runs := m.ClaimRuns(now)
	due := make([]Spec, len(runs))
	for i, r := range runs {
		due[i] = r.Spec
	}
	return due
}

// ClaimRuns is Claim, for results reported with Report rather than by name.
func (m *Monitor) ClaimRuns(now float64) []Claimed {
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Claimed
	for _, name := range m.order {
		st := m.states[name]
		if !st.Spec.Paused && !st.running && st.due(now) && (m.Resting == nil || !m.Resting(name, now)) {
			st.running = true
			due = append(due, Claimed{Spec: st.Spec, st: st})
		}
	}
	return due
}

// Record folds a result into the service's state. A service goes down only after
// Retries consecutive failures beyond the first, so one dropped request does not alert.
// It returns "down" or "up" when the status changed (the first result only reports "down").
// Results for a check deleted or paused while it ran, or whose type or target changed, are
// dropped; one renamed while it ran is recorded under its new name.
func (m *Monitor) Record(name string, r Result, now float64) string {
	_, change, _ := m.RecordAs(name, r, now)
	return change
}

// RecordAs is Record, also saying the name the result went to (the new one, for a check renamed
// while it ran) and whether it was recorded at all: a dropped result must not alert or repair, since
// the check it was for has been paused, deleted or changed since. A name can pass to another check
// while one runs, so a result can only be told apart by name so far; the loop uses Report.
func (m *Monitor) RecordAs(name string, r Result, now float64) (to, change string, recorded bool) {
	m.mu.Lock()
	st, ok := m.states[name]
	// A check of this name that is not running has no result coming: this one is for the check
	// renamed away from it.
	if to, moved := m.renamed[name]; moved && (!ok || !st.running) {
		delete(m.renamed, name)
		name = to
		st, ok = m.states[name]
	}
	if !ok {
		m.mu.Unlock()
		return name, "", false
	}
	change, recorded = m.foldLocked(st, r, now)
	m.mu.Unlock()
	if recorded && m.store != nil {
		m.store.Record(name, now, r.Up, r.LatencyMS, r.Detail)
	}
	return name, change, recorded
}

// Report records a claimed run's result on the check it was claimed from, under that check's name
// now. It returns the check as it now is, for the alert, and whether the result was recorded at
// all: one for a check deleted, paused or retargeted while it ran is dropped, and must neither
// alert nor repair.
func (m *Monitor) Report(run Claimed, r Result, now float64) (spec Spec, change string, recorded bool) {
	m.mu.Lock()
	st := run.st
	if st == nil || m.states[st.Spec.Name] != st {
		m.mu.Unlock()
		return run.Spec, "", false // deleted while it ran (a new check may have its name)
	}
	name := st.Spec.Name
	// The result has come home: nothing still waits for it under a name it had before.
	for from, to := range m.renamed {
		if to == name {
			delete(m.renamed, from)
		}
	}
	change, recorded = m.foldLocked(st, r, now)
	spec = st.Spec
	m.mu.Unlock()
	if recorded && m.store != nil {
		m.store.Record(name, now, r.Up, r.LatencyMS, r.Detail)
	}
	return spec, change, recorded
}

// foldLocked ends st's run and folds its result into st, unless the check was paused or changed
// while it ran. m.mu is held.
func (m *Monitor) foldLocked(st *State, r Result, now float64) (change string, recorded bool) {
	stale := st.stale
	st.running, st.stale = false, false
	if st.Spec.Paused || stale {
		return "", false
	}
	st.LastCheck, st.runNow = now, false
	st.LastResult = &r
	st.history = append(st.history, beat{now, r.Up, r.LatencyMS})
	st.slots.record(now, r)
	if len(st.history) > maxHistory {
		st.history = st.history[len(st.history)-maxHistory:]
	}
	previous := st.Status
	if r.Up {
		st.ConsecutiveFailures = 0
		st.Status = StatusUp
	} else {
		st.ConsecutiveFailures++
		if st.ConsecutiveFailures > st.Spec.Retries {
			st.Status = StatusDown
		}
	}
	if st.Status != previous {
		st.LastChange = &now
		if st.Status == StatusDown {
			change = StatusDown
		} else if previous == StatusDown {
			change = StatusUp
		}
	}
	return change, true
}

// Uptime is the percent up since `since` (at 5-minute resolution, up to 30 days back).
func (m *Monitor) Uptime(name string, since float64) *float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if st, ok := m.states[name]; ok {
		return st.slots.uptime(since)
	}
	return nil
}

// History returns results from `since` to `until`, oldest first, summarised into at most `points` buckets.
// Ranges whose buckets are at least one 5-minute slot wide (7d, 30d) come from the in-memory slots;
// shorter ranges need per-check detail and read the store.
func (m *Monitor) History(name string, since, until float64, points int) []storage.Point {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.states[name]
	if !ok {
		return nil
	}
	if (until-since)/float64(points) >= slotSeconds && since >= until-slotSpan {
		return st.slots.buckets(since, until, points)
	}
	if m.store != nil {
		return m.store.Buckets(name, since, until, points)
	}
	var out []storage.Point
	for _, b := range st.history {
		if b.ts >= since {
			p := storage.Point{TS: b.ts, Up: b.up, LatencyMS: b.latency}
			if !b.up {
				p.Fails = 1
			}
			out = append(out, p)
		}
	}
	return out
}

// Prune drops stored history older than the cutoff.
func (m *Monitor) Prune(olderThan float64) int64 {
	if m.store == nil {
		return 0
	}
	return m.store.Prune(olderThan)
}

// Beat is one recent check for the dashboard strip.
type Beat struct {
	TS        float64  `json:"ts"`
	Up        bool     `json:"up"`
	LatencyMS *float64 `json:"latency_ms"`
}

// Summary is one service's row in the API. Viewers read it too, so its Target is only what may be
// shown of the check's (see ShownTarget): empty for a heartbeat.
type Summary struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Target     string   `json:"target"`
	Status     string   `json:"status"`
	LatencyMS  *float64 `json:"latency_ms"`
	Detail     string   `json:"detail"`
	LastCheck  *float64 `json:"last_check"`
	LastChange *float64 `json:"last_change"`
	Uptime24h  *float64 `json:"uptime_24h"`
	Uptime7d   *float64 `json:"uptime_7d"`
	Uptime30d  *float64 `json:"uptime_30d"`
	Recent     []Beat   `json:"recent"`
	// Cause explains a down check's detail in plain words, when it is a known kind of failure.
	Cause *diagnose.Finding `json:"cause,omitempty"`
	// A heartbeat's schedule and what it has heard.
	Schedule string `json:"schedule,omitempty"`
	Pings    *Pings `json:"pings,omitempty"`
	// Repair is what Nightjar does when the check goes down.
	Repair *Repair `json:"repair,omitempty"`
	// Server is the id of the watched server it runs on, if any.
	Server string `json:"server,omitempty"`
}

// Summaries returns every service's status, in configuration order.
func (m *Monitor) Summaries(now float64) []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := make([]Summary, 0, len(m.order))
	for _, name := range m.order {
		st := m.states[name]
		row := Summary{Name: name, Type: st.Spec.Type, Target: ShownTarget(st.Spec), Status: st.Status, LastChange: st.LastChange, Recent: []Beat{}, Repair: st.Spec.Repair, Server: st.Spec.Server}
		if st.Spec.Type == "heartbeat" {
			row.Schedule = st.Spec.Schedule
			if p, ok := m.heartbeats[st.Spec.Target]; ok {
				cp := *p
				row.Pings = &cp
			}
		}
		if st.Spec.Paused {
			row.Status = StatusPaused
		} else if m.Resting != nil && m.Resting(name, now) {
			row.Status = StatusMaintenance
		}
		if st.LastResult != nil {
			row.LatencyMS, row.Detail = st.LastResult.LatencyMS, st.LastResult.Detail
			if f, ok := diagnose.Text(row.Detail); ok && !st.LastResult.Up {
				row.Cause = &f
			}
		}
		if st.LastCheck > 0 {
			lc := st.LastCheck
			row.LastCheck = &lc
		}
		start := max(0, len(st.history)-RecentBeats)
		for _, b := range st.history[start:] {
			row.Recent = append(row.Recent, Beat{b.ts, b.up, b.latency})
		}
		row.Uptime24h = st.slots.uptime(now - 86400)
		row.Uptime7d = st.slots.uptime(now - 7*86400)
		row.Uptime30d = st.slots.uptime(now - 30*86400)
		rows = append(rows, row)
	}
	return rows
}
