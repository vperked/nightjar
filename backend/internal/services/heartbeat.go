package services

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A heartbeat check watches a job that runs on its own (a backup, a cron job): the job pings
// /ping/<token> when it finishes (or /start, /fail, or its exit code), and the check is down when
// a run is missed, reports failure, or takes too long. It is judged against the job's schedule,
// so a nightly backup is late at ten past three, not an hour after its last ping.

const heartbeatInterval = 30.0

var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Schedule is when a job is meant to run.
type Schedule struct {
	Every time.Duration // "every 1h": runs that often, from whenever it last ran
	cron  *cron
}

// ParseSchedule reads a five-field cron line ("0 3 * * *"), a shortcut (@hourly, @daily,
// @weekly, @monthly) or "every <duration>" ("every 15m", "every 6h", "every 1d").
func ParseSchedule(s string) (Schedule, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return Schedule{}, errors.New(`a heartbeat needs a schedule: cron ("0 3 * * *"), @daily, or "every 1h"`)
	}
	if rest, ok := strings.CutPrefix(s, "every "); ok {
		rest = strings.TrimSpace(rest)
		if days, ok := strings.CutSuffix(rest, "d"); ok {
			if n, err := strconv.Atoi(days); err == nil {
				rest = fmt.Sprintf("%dh", n*24)
			}
		}
		d, err := time.ParseDuration(rest)
		if err != nil || d < time.Minute || d > 31*24*time.Hour {
			return Schedule{}, fmt.Errorf(`"every" takes a duration from 1m to 31d, such as 15m, 6h or 1d`)
		}
		return Schedule{Every: d}, nil
	}
	if alias, ok := map[string]string{"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *"}[s]; ok {
		s = alias
	}
	c, err := parseCron(s)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{cron: c}, nil
}

// String says the schedule in words, for alerts.
func (s Schedule) String() string {
	if s.cron == nil {
		return "every " + span(s.Every.Seconds())
	}
	return "on schedule " + s.cron.text
}

// cron is a parsed five-field line: which minutes, hours, days, months and weekdays match.
type cron struct {
	text                       string
	min, hour, dom, month, dow [64]bool
	anyDom, anyDow             bool
}

func parseCron(s string) (*cron, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return nil, errors.New(`a cron schedule has five fields: minute hour day month weekday, such as "0 3 * * *"`)
	}
	c := &cron{text: strings.Join(f, " "), anyDom: f[2] == "*", anyDow: f[4] == "*"}
	for i, spec := range []struct {
		set    *[64]bool
		lo, hi int
		name   string
	}{{&c.min, 0, 59, "minute"}, {&c.hour, 0, 23, "hour"}, {&c.dom, 1, 31, "day"}, {&c.month, 1, 12, "month"}, {&c.dow, 0, 7, "weekday"}} {
		if err := cronField(f[i], spec.lo, spec.hi, spec.set); err != nil {
			return nil, fmt.Errorf("cron %s %q: %v", spec.name, f[i], err)
		}
	}
	// Sunday is 0 or 7.
	if c.dow[7] {
		c.dow[0] = true
	}
	return c, nil
}

func cronField(field string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(field, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return errors.New("the step after / must be a positive number")
			}
			step = n
		}
		from, to := lo, hi
		if rangePart != "*" {
			a, b, isRange := strings.Cut(rangePart, "-")
			x, err := strconv.Atoi(a)
			if err != nil {
				return errors.New("expected a number, a range such as 1-5, or *")
			}
			from, to = x, x
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return errors.New("expected a range such as 1-5")
				}
			} else if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("must be within %d-%d", lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

func (c *cron) matchesDay(t time.Time) bool {
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	// As in cron: when both day fields are restricted, either one matching is enough.
	switch {
	case c.anyDom && c.anyDow:
		return true
	case c.anyDom:
		return dow
	case c.anyDow:
		return dom
	}
	return dom || dow
}

// prev is the latest scheduled minute at or before t, within the last year and a bit.
func (c *cron) prev(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute)
	limit := t.AddDate(-1, -1, 0)
	for t.After(limit) {
		if !c.month[int(t.Month())] || !c.matchesDay(t) {
			// Back to the last minute of the day before.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if !c.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Add(-time.Minute)
			continue
		}
		if c.min[t.Minute()] {
			return t, true
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}, false
}

// next is the first scheduled minute after t.
func (c *cron) next(t time.Time) (time.Time, bool) {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 1, 0)
	for t.Before(limit) {
		if !c.month[int(t.Month())] || !c.matchesDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if c.min[t.Minute()] {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// Pings is what a heartbeat has heard, in unix seconds (0 for never).
type Pings struct {
	Seen   float64 `json:"seen"` // when Nightjar began listening, for a job not yet run
	Start  float64 `json:"start"`
	OK     float64 `json:"ok"`
	Fail   float64 `json:"fail"`
	Exit   int     `json:"exit"`
	Output string  `json:"output,omitempty"`
}

// maxOutput is how much of what a job sends with its ping is kept.
const maxOutput = 4000

func pingKey(token string) string { return "heartbeat:" + token }

// pings is the heartbeat's record, from memory or the store, started now if it is new.
// The caller holds m.mu.
func (m *Monitor) pings(token string, now float64) *Pings {
	if m.heartbeats == nil {
		m.heartbeats = map[string]*Pings{}
	}
	if p, ok := m.heartbeats[token]; ok {
		return p
	}
	p := &Pings{Seen: now}
	if m.store != nil {
		raw, err := m.store.MetaValue(pingKey(token))
		if raw != "" {
			json.Unmarshal([]byte(raw), p)
		} else if err == nil {
			// Kept at once, so a restart does not move when Nightjar began listening for a job
			// that never pings, which would keep putting off its late or missed verdict. A record
			// that could not be read is not written over: it may hold the job's pings.
			data, _ := json.Marshal(p)
			m.store.SetMeta(pingKey(token), string(data))
		}
	}
	m.heartbeats[token] = p
	return p
}

// Ping records a heartbeat's ping: kind is "" (finished well), "start", "fail", or an exit
// code. It returns the check's name, or false when no heartbeat has the token.
func (m *Monitor) Ping(token, kind, output string, now float64) (string, bool) {
	m.mu.Lock()
	var st *State
	for _, name := range m.order {
		if s := m.states[name]; s.Spec.Type == "heartbeat" && s.Spec.Target == token {
			st = s
			break
		}
	}
	if st == nil {
		m.mu.Unlock()
		return "", false
	}
	p := m.pings(token, now)
	if len(output) > maxOutput {
		output = output[len(output)-maxOutput:]
	}
	switch kind {
	case "start":
		p.Start = now
	case "", "0":
		p.OK, p.Exit = now, 0
		if output != "" || kind == "0" {
			p.Output = output
		}
	default:
		p.Fail, p.Exit, p.Output = now, 1, output
		if n, err := strconv.Atoi(kind); err == nil {
			p.Exit = n
		}
	}
	// It is judged again at once, so a failure alerts now rather than at the next look.
	st.runNow = true
	data, _ := json.Marshal(p)
	name := st.Spec.Name
	m.mu.Unlock()
	if m.store != nil {
		m.store.SetMeta(pingKey(token), string(data))
	}
	return name, true
}

// Offline tells the heartbeats that Nightjar was not listening from `from` to `to` (its machine was
// off, or Nightjar was stopped). A ping sent then was lost, and a job on Nightjar's own machine
// could not run, so a run whose ping was due in that gap is not held against the job: Nightjar
// counts as having begun listening again at `to`. It returns the heartbeats so excused.
func (m *Monitor) Offline(from, to float64) []string {
	if from <= 0 || to <= from {
		return nil
	}
	m.mu.Lock()
	var excused []string
	keep := map[string]string{}
	for _, name := range m.order {
		s := m.states[name].Spec
		if s.Type != "heartbeat" {
			continue
		}
		p := m.pings(s.Target, to)
		if !missable(s, *p, from, to) {
			continue
		}
		p.Seen = to
		data, _ := json.Marshal(p)
		keep[pingKey(s.Target)] = string(data)
		excused = append(excused, name)
	}
	m.mu.Unlock()
	if m.store != nil {
		for k, v := range keep {
			m.store.SetMeta(k, v)
		}
	}
	return excused
}

// missable reports whether a ping s was waiting for could have come while Nightjar was not
// listening, from `from` to `to`.
func missable(s Spec, p Pings, from, to float64) bool {
	sched, err := ParseSchedule(s.Schedule)
	if err != nil || p.Seen >= to {
		return false
	}
	if sched.cron == nil {
		// Its next ping was due in the gap, give or take the grace.
		due := max(p.OK, p.Seen) + sched.Every.Seconds()
		return due+s.Grace >= from && due-s.Grace <= to
	}
	// The first run that could still report at `from`, given the grace; it may report a minute early.
	t, ok := sched.cron.next(time.Unix(int64(from-s.Grace), 0).Add(-time.Minute))
	due := float64(t.Unix()) - 60
	return ok && due <= to && p.OK < due
}

// Heartbeat judges a heartbeat check from its pings at now.
func (m *Monitor) Heartbeat(s Spec, now float64) Result {
	m.mu.Lock()
	p := *m.pings(s.Target, now)
	m.mu.Unlock()
	return judge(s, p, now)
}

func judge(s Spec, p Pings, now float64) Result {
	sched, err := ParseSchedule(s.Schedule)
	if err != nil {
		return down(err.Error())
	}
	clock := func(t float64) string { return time.Unix(int64(t), 0).Format("Mon 15:04") }
	var runtime *float64
	if p.Start > 0 && p.OK >= p.Start && p.OK-p.Start < 7*86400 {
		ms := (p.OK - p.Start) * 1000
		runtime = &ms
	}
	result := func(up bool, detail string) Result { return Result{Up: up, LatencyMS: runtime, Detail: detail} }

	if p.Fail > 0 && p.Fail >= p.OK && p.Fail >= p.Start {
		detail := fmt.Sprintf("the job reported a failure %s ago", span(now-p.Fail))
		if p.Exit != 0 && p.Exit != 1 {
			detail = fmt.Sprintf("the job exited with code %d %s ago", p.Exit, span(now-p.Fail))
		}
		if line := lastLine(p.Output); line != "" {
			detail += ": " + line
		}
		return result(false, detail)
	}
	running := p.Start > 0 && p.Start > p.OK && p.Start > p.Fail
	if running && s.MaxRuntime > 0 && now-p.Start > s.MaxRuntime {
		return result(false, fmt.Sprintf("started %s ago and still running; it may take %s", span(now-p.Start), span(s.MaxRuntime)))
	}
	since := func() string {
		if p.OK == 0 {
			return "no ping yet"
		}
		return "last ran " + span(now-p.OK) + " ago"
	}

	if sched.cron == nil {
		ref := max(p.OK, p.Seen)
		due := ref + sched.Every.Seconds()
		if now > due+s.Grace {
			return result(false, fmt.Sprintf("late: %s, and it runs %s", since(), sched))
		}
		if running {
			return result(true, fmt.Sprintf("running since %s; %s", clock(p.Start), since()))
		}
		return result(true, fmt.Sprintf("%s; next due by %s", since(), clock(due+s.Grace)))
	}

	// The latest run that should have reported by now, given the grace.
	prevT, ok := sched.cron.prev(time.Unix(int64(now-s.Grace), 0))
	nextT, _ := sched.cron.next(time.Unix(int64(now), 0))
	next := "next run " + nextT.Format("Mon 15:04")
	if !ok || float64(prevT.Unix()) < p.Seen {
		// No run was due since Nightjar started listening.
		if running {
			return result(true, "running since "+clock(p.Start))
		}
		return result(true, fmt.Sprintf("%s; %s", since(), next))
	}
	prev := float64(prevT.Unix())
	// A run may report a little before its minute, if the clocks differ.
	if p.OK >= prev-60 {
		return result(true, fmt.Sprintf("%s; %s", since(), next))
	}
	if running && p.Start >= prev-60 {
		return result(true, fmt.Sprintf("running since %s; %s", clock(p.Start), next))
	}
	return result(false, fmt.Sprintf("missed the run due %s: %s", prevT.Format("Mon 15:04"), since()))
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}

// span is a duration in words: 45s, 12 min, 3h 10m, 2d 4h.
func span(seconds float64) string {
	s := int(math.Round(math.Max(0, seconds)))
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%d min", s/60)
	case s < 86400:
		if s%3600/60 == 0 {
			return fmt.Sprintf("%dh", s/3600)
		}
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
	if s%86400/3600 == 0 {
		return fmt.Sprintf("%dd", s/86400)
	}
	return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
}
