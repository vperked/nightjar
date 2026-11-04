// Package maintenance keeps planned downtime: one-off windows ("Sunday 2:00 to 3:00, updating
// the NAS") and weekly ones ("every night 4:00 to 4:30, backups"). While a window is on, the checks
// it covers do not run, so they neither alert, nor repair, nor count against uptime; a window over
// everything also holds back every other alert. A window can be shown on the status page.
package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/storage"
)

const key = "maintenance.windows"

// Window is one planned stretch of downtime: one-off, from Start to End (unix seconds), or weekly,
// on Days (0 is Sunday) from From to To ("02:00", local time; To before From runs past midnight).
type Window struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Checks []string `json:"checks"` // empty covers everything Nightjar watches
	Start  float64  `json:"start,omitempty"`
	End    float64  `json:"end,omitempty"`
	Days   []int    `json:"days,omitempty"`
	From   string   `json:"from,omitempty"`
	To     string   `json:"to,omitempty"`
	Public bool     `json:"public"`         // shown on the status page
	Note   string   `json:"note,omitempty"` // what visitors read
}

// Span is one occurrence of a window.
type Span struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

var clockRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// Weekly reports whether the window repeats every week.
func (w Window) Weekly() bool { return len(w.Days) > 0 }

// Validate tidies a window and says what is wrong with it.
func (w *Window) Validate() error {
	w.Title, w.Note = strings.TrimSpace(w.Title), strings.TrimSpace(w.Note)
	if w.Title == "" || len(w.Title) > 80 {
		return errors.New("a window needs a title of up to 80 characters")
	}
	if len(w.Note) > 400 {
		return errors.New("the note for visitors is longer than 400 characters")
	}
	if w.Checks == nil {
		w.Checks = []string{}
	}
	if w.Weekly() {
		slices.Sort(w.Days)
		w.Days = slices.Compact(w.Days)
		for _, d := range w.Days {
			if d < 0 || d > 6 {
				return errors.New("days run from 0 (Sunday) to 6 (Saturday)")
			}
		}
		if !clockRE.MatchString(w.From) || !clockRE.MatchString(w.To) || w.From == w.To {
			return errors.New(`a weekly window needs a start and an end time such as "02:00" and "03:30"`)
		}
		w.Start, w.End = 0, 0
		return nil
	}
	if w.Start <= 0 || w.End <= w.Start {
		return errors.New("a window needs a start, and an end after it")
	}
	if w.End-w.Start > 14*86400 {
		return errors.New("a window may last at most 14 days")
	}
	w.From, w.To = "", ""
	return nil
}

func minutes(clock string) int {
	var h, m int
	fmt.Sscanf(clock, "%d:%d", &h, &m)
	return h*60 + m
}

// At is the occurrence that contains now or, failing that, the next one to begin within a week.
func (w Window) At(now time.Time) (Span, bool) {
	t := float64(now.Unix())
	if !w.Weekly() {
		if w.End > t {
			return Span{w.Start, w.End}, true
		}
		return Span{}, false
	}
	from, to := minutes(w.From), minutes(w.To)
	length := to - from
	if length <= 0 {
		length += 24 * 60
	}
	// Yesterday's may still be running past midnight; then today's and the week's after.
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for d := -1; d <= 7; d++ {
		day := midnight.AddDate(0, 0, d)
		if !slices.Contains(w.Days, int(day.Weekday())) {
			continue
		}
		start := day.Add(time.Duration(from) * time.Minute)
		end := start.Add(time.Duration(length) * time.Minute)
		if end.After(now) {
			return Span{float64(start.Unix()), float64(end.Unix())}, true
		}
	}
	return Span{}, false
}

// Active reports whether the window is on at now.
func (w Window) Active(now time.Time) bool {
	s, ok := w.At(now)
	return ok && s.Start <= float64(now.Unix())
}

// Covers reports whether the window covers the check.
func (w Window) Covers(check string) bool {
	return len(w.Checks) == 0 || slices.Contains(w.Checks, check)
}

// Manager keeps the windows in the store. Its methods are safe for concurrent use.
type Manager struct {
	store *storage.Store
	Now   func() time.Time

	mu   sync.RWMutex
	list []Window
}

func New(store *storage.Store) *Manager {
	m := &Manager{store: store, Now: time.Now, list: []Window{}}
	if raw := store.Meta(key); raw != "" {
		json.Unmarshal([]byte(raw), &m.list)
	}
	return m
}

// List returns every window, soonest first, with ones that are over last.
func (m *Manager) List() []Window {
	if m == nil {
		return []Window{}
	}
	m.mu.RLock()
	out := slices.Clone(m.list)
	m.mu.RUnlock()
	now := m.Now()
	at := func(w Window) float64 {
		if s, ok := w.At(now); ok {
			return s.Start
		}
		return 1e18 - w.End
	}
	slices.SortStableFunc(out, func(a, b Window) int {
		x, y := at(a), at(b)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return out
}

func (m *Manager) persist() error {
	data, _ := json.Marshal(m.list)
	return m.store.SetMeta(key, string(data))
}

// Save adds a window (with no ID) or replaces the one with its ID.
func (m *Manager) Save(w Window) (Window, error) {
	if err := w.Validate(); err != nil {
		return Window{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.ID == "" {
		b := make([]byte, 6)
		rand.Read(b)
		w.ID = hex.EncodeToString(b)
		m.list = append(m.list, w)
	} else {
		i := slices.IndexFunc(m.list, func(x Window) bool { return x.ID == w.ID })
		if i < 0 {
			return Window{}, ErrNotFound
		}
		m.list[i] = w
	}
	return w, m.persist()
}

var ErrNotFound = errors.New("no maintenance window with that id")

// Delete removes a window.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.list, func(x Window) bool { return x.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	m.list = slices.Delete(m.list, i, i+1)
	return m.persist()
}

// Rename keeps windows pointing at a check after it is renamed.
func (m *Manager) Rename(oldName, newName string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for i := range m.list {
		if j := slices.Index(m.list[i].Checks, oldName); j >= 0 {
			m.list[i].Checks[j] = newName
			changed = true
		}
	}
	if changed {
		m.persist()
	}
}

// For is the window on at now that covers the check, if any.
func (m *Manager) For(check string, now time.Time) (Window, bool) {
	if m == nil {
		return Window{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, w := range m.list {
		if w.Covers(check) && w.Active(now) {
			return w, true
		}
	}
	return Window{}, false
}

// Holds reports whether an alert about subject is held back at now: a check's by any window
// covering it, anything else only by a window over everything.
func (m *Manager) Holds(subject string, now time.Time) bool {
	if m == nil {
		return false
	}
	if name, ok := strings.CutPrefix(subject, "check:"); ok {
		_, on := m.For(name, now)
		return on
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, w := range m.list {
		if len(w.Checks) == 0 && w.Active(now) {
			return true
		}
	}
	return false
}

// Occurrence is a window's current or next time.
type Occurrence struct {
	Window Window  `json:"window"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	On     bool    `json:"on"`
}

// Upcoming lists the windows on now or beginning within the next days, soonest first.
func (m *Manager) Upcoming(now time.Time, days int) []Occurrence {
	out := []Occurrence{}
	limit := float64(now.Unix()) + float64(days)*86400
	for _, w := range m.List() {
		if s, ok := w.At(now); ok && s.Start <= limit {
			out = append(out, Occurrence{Window: w, Start: s.Start, End: s.End, On: s.Start <= float64(now.Unix())})
		}
	}
	return out
}

// Prune drops one-off windows that ended more than a month ago.
func (m *Manager) Prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := float64(now.Unix()) - 30*86400
	before := len(m.list)
	m.list = slices.DeleteFunc(m.list, func(w Window) bool { return !w.Weekly() && w.End < cut })
	if len(m.list) != before {
		m.persist()
	}
}
