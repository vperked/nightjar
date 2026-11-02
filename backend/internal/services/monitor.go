package services

import (
	"sync"

	"servermonitor/internal/storage"
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

// Claim returns the active checks that are due and not already running, and marks them running
// until their result is recorded, so a slow check is never started twice.
func (m *Monitor) Claim(now float64) []Spec {
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []Spec
	for _, name := range m.order {
		st := m.states[name]
		if !st.Spec.Paused && !st.running && (st.runNow || now-st.LastCheck >= st.Spec.Interval) {
			st.running = true
			due = append(due, st.Spec)
		}
	}
	return due
}

// Record folds a result into the service's state. A service goes down only after
// Retries consecutive failures beyond the first, so one dropped request does not alert.
// It returns "down" or "up" when the status changed (the first result only reports "down").
// Results for a check deleted or paused while it ran are dropped.
func (m *Monitor) Record(name string, r Result, now float64) string {
	m.mu.Lock()
	st, ok := m.states[name]
	if ok {
		st.running = false
	}
	if !ok || st.Spec.Paused {
		m.mu.Unlock()
		return ""
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
	change := ""
	if st.Status != previous {
		st.LastChange = &now
		if st.Status == StatusDown {
			change = StatusDown
		} else if previous == StatusDown {
			change = StatusUp
		}
	}
	m.mu.Unlock()

	if m.store != nil {
		m.store.Record(name, now, r.Up, r.LatencyMS, r.Detail)
	}
	return change
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

// Summary is one service's row in the API.
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
}

// Summaries returns every service's status, in configuration order.
func (m *Monitor) Summaries(now float64) []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := make([]Summary, 0, len(m.order))
	for _, name := range m.order {
		st := m.states[name]
		row := Summary{Name: name, Type: st.Spec.Type, Target: st.Spec.Target, Status: st.Status, LastChange: st.LastChange, Recent: []Beat{}}
		if st.Spec.Paused {
			row.Status = StatusPaused
		}
		if st.LastResult != nil {
			row.LatencyMS, row.Detail = st.LastResult.LatencyMS, st.LastResult.Detail
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
