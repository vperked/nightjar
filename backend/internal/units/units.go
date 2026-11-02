// Package units watches systemd services: it notices services that fail or keep restarting and
// recover, and lets the dashboard read their logs and restart them.
package units

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Unit is one systemd service as the dashboard shows it.
type Unit struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Active      string  `json:"active"` // active, failed, activating, deactivating, inactive
	Sub         string  `json:"sub"`    // running, exited, auto-restart, dead…
	Enabled     string  `json:"enabled,omitempty"`
	Result      string  `json:"result,omitempty"` // why it last stopped: success, exit-code, signal, core-dump…
	ExitStatus  int     `json:"exit_status,omitempty"`
	Restarts    int     `json:"restarts"`
	Since       float64 `json:"since,omitempty"` // when it entered its current state
	// Flapping is how many times it restarted in the last ten minutes, once that reaches the alert level.
	Flapping int  `json:"flapping,omitempty"`
	Ignored  bool `json:"ignored,omitempty"`
}

// Failed reports whether the service is down after failing (or stuck restarting).
func (u Unit) Failed() bool { return u.Active == "failed" || u.Sub == "auto-restart" }

// EventKind is what changed about a service.
type EventKind int

const (
	EventFailed EventKind = iota
	EventRecovered
	EventFlapping
)

// Event is a change worth an alert.
type Event struct {
	Kind     EventKind
	Unit     Unit
	Downtime time.Duration // for EventRecovered
	Restarts int           // for EventFlapping: restarts within the window
}

// Runner runs systemctl and journalctl; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

const flapWindow = 10 * time.Minute

// Monitor polls systemd and remembers enough to tell new failures, recoveries and restart loops apart.
type Monitor struct {
	Run Runner
	// AnnounceExisting reports services already failed at the first poll (a fresh start, perhaps after
	// a reboot); a monitor restarting itself to apply settings leaves it off, having said so before.
	AnnounceExisting bool

	mu       sync.Mutex
	units    map[string]Unit
	failedAt map[string]time.Time
	restarts map[string][]restartObs
	flapped  map[string]bool
	polled   bool
}

type restartObs struct {
	at time.Time
	n  int
}

func New() *Monitor {
	return &Monitor{Run: execRunner, units: map[string]Unit{}, failedAt: map[string]time.Time{}, restarts: map[string][]restartObs{}, flapped: map[string]bool{}}
}

// properties read for every service that is not idle.
var properties = "Id,Description,ActiveState,SubState,UnitFileState,Result,ExecMainStatus,NRestarts,StateChangeTimestampMonotonic,Transient"

// Poll reads every loaded service and returns what changed since the last poll. ignore holds unit
// names or shell patterns (apt-*) never to alert on; flapAt is the restarts within ten minutes
// that count as a restart loop. The first poll only learns the current state, unless
// AnnounceExisting asks it to report services that are already failed.
func (m *Monitor) Poll(ctx context.Context, now time.Time, ignore []string, flapAt int) ([]Event, error) {
	units, err := m.read(ctx, now)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	first := !m.polled
	m.polled = true
	var events []Event
	seen := map[string]bool{}
	for _, u := range units {
		u.Ignored = ignored(u.Name, ignore)
		seen[u.Name] = true
		events = append(events, m.flapping(&u, now, flapAt)...)
		events = append(events, m.transition(u, now, first)...)
		m.units[u.Name] = u
	}
	m.forgetGone(seen)
	return events, nil
}

// read lists every loaded service, with details for those that are not idle (inactive and never
// failed services need none). Transient units (systemd-run) are someone's one-off command, not a
// service, and are left out.
func (m *Monitor) read(ctx context.Context, now time.Time) ([]Unit, error) {
	listed, err := m.Run(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain", "--output=json")
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var rows []struct{ Unit, Load, Active string }
	if err := json.Unmarshal(listed, &rows); err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var busy []string
	for _, r := range rows {
		if r.Load == "loaded" && r.Active != "inactive" {
			busy = append(busy, r.Unit)
		}
	}
	details := map[string]Unit{}
	if len(busy) > 0 {
		out, err := m.Run(ctx, "systemctl", append([]string{"show", "--property=" + properties, "--"}, busy...)...)
		if err != nil {
			return nil, fmt.Errorf("systemctl show: %w", err)
		}
		details = parseShow(out, now)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var units []Unit
	for _, r := range rows {
		if r.Load != "loaded" {
			continue
		}
		u, ok := details[r.Unit]
		if !ok {
			// Idle: keep what an earlier poll learned about it.
			prev := m.units[r.Unit]
			u = Unit{Name: r.Unit, Active: r.Active, Sub: "dead", Description: prev.Description, Enabled: prev.Enabled}
		}
		if u.Name != "" {
			units = append(units, u)
		}
	}
	return units, nil
}

// flapping notes the unit's restart count and reports a restart loop once it restarts flapAt times
// within the window, then not again until it has settled.
func (m *Monitor) flapping(u *Unit, now time.Time, flapAt int) []Event {
	obs := append(m.restarts[u.Name], restartObs{now, u.Restarts})
	for len(obs) > 1 && now.Sub(obs[0].at) > flapWindow {
		obs = obs[1:]
	}
	m.restarts[u.Name] = obs
	n := u.Restarts - obs[0].n
	if flapAt <= 0 || n < flapAt {
		if u.Restarts == obs[0].n {
			m.flapped[u.Name] = false
		}
		return nil
	}
	u.Flapping = n
	announce := !m.flapped[u.Name] && !u.Ignored
	m.flapped[u.Name] = true
	if !announce {
		return nil
	}
	return []Event{{Kind: EventFlapping, Unit: *u, Restarts: n}}
}

// transition reports a service that just failed or just came back.
func (m *Monitor) transition(u Unit, now time.Time, first bool) []Event {
	prev, known := m.units[u.Name]
	switch {
	case u.Failed() && (!known || !prev.Failed()):
		m.failedAt[u.Name] = now
		if !u.Ignored && (!first || m.AnnounceExisting) {
			return []Event{{Kind: EventFailed, Unit: u}}
		}
	case !u.Failed() && known && prev.Failed():
		down := now.Sub(m.failedAt[u.Name])
		delete(m.failedAt, u.Name)
		if !u.Ignored {
			return []Event{{Kind: EventRecovered, Unit: u, Downtime: down}}
		}
	}
	return nil
}

// forgetGone drops services that no longer exist.
func (m *Monitor) forgetGone(seen map[string]bool) {
	for name := range m.units {
		if !seen[name] {
			delete(m.units, name)
			delete(m.failedAt, name)
			delete(m.restarts, name)
			delete(m.flapped, name)
		}
	}
}

// parseShow reads `systemctl show` output: one block of key=value lines per unit.
func parseShow(out []byte, now time.Time) map[string]Unit {
	units := map[string]Unit{}
	uptime := monotonicNow()
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		kv := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				kv[k] = v
			}
		}
		if kv["Id"] == "" {
			continue
		}
		u := Unit{Description: kv["Description"], Active: kv["ActiveState"], Sub: kv["SubState"], Enabled: kv["UnitFileState"], Result: kv["Result"]}
		if kv["Transient"] != "yes" {
			u.Name = kv["Id"]
		}
		u.ExitStatus, _ = strconv.Atoi(kv["ExecMainStatus"])
		u.Restarts, _ = strconv.Atoi(kv["NRestarts"])
		// Monotonic microseconds since boot avoid parsing systemd's localized timestamps.
		if us, err := strconv.ParseInt(kv["StateChangeTimestampMonotonic"], 10, 64); err == nil && us > 0 && uptime > 0 {
			u.Since = float64(now.Unix()) - (uptime - float64(us)/1e6)
		}
		units[kv["Id"]] = u
	}
	return units
}

func ignored(name string, patterns []string) bool {
	short := strings.TrimSuffix(name, ".service")
	for _, p := range patterns {
		p = strings.TrimSuffix(strings.TrimSpace(p), ".service")
		if ok, _ := path.Match(p, short); ok {
			return true
		}
	}
	return false
}

// Units lists the services worth showing: failed or restarting ones first, then running ones,
// then the rest of the enabled ones, each by name.
func (m *Monitor) Units() []Unit {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Unit, 0, len(m.units))
	for _, u := range m.units {
		if u.Active == "inactive" && u.Enabled != "enabled" {
			continue
		}
		out = append(out, u)
	}
	rank := func(u Unit) int {
		switch {
		case u.Failed() && !u.Ignored:
			return 0
		case u.Flapping > 0:
			return 1
		case u.Active == "active" || u.Active == "activating" || u.Active == "reloading":
			return 2
		}
		return 3
	}
	slices.SortFunc(out, func(a, b Unit) int {
		if d := rank(a) - rank(b); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Get returns one known service.
func (m *Monitor) Get(name string) (Unit, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.units[name]
	return u, ok
}

// ErrUnknown means the unit is not a service the monitor has seen; only those can be managed.
var ErrUnknown = errors.New("no such service")

// Logs returns the service's last lines from the journal, oldest first.
func (m *Monitor) Logs(ctx context.Context, name string, lines int) ([]string, error) {
	if _, ok := m.Get(name); !ok {
		return nil, ErrUnknown
	}
	out, err := m.Run(ctx, "journalctl", "--unit="+name, "--lines="+strconv.Itoa(lines), "--no-pager", "--output=short-iso", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return []string{}, nil
	}
	return strings.Split(text, "\n"), nil
}

// Control starts, stops or restarts a known service and waits for systemd to finish the job.
func (m *Monitor) Control(ctx context.Context, action, name string) error {
	if !slices.Contains([]string{"start", "stop", "restart"}, action) {
		return fmt.Errorf("unknown action %q", action)
	}
	if _, ok := m.Get(name); !ok {
		return ErrUnknown
	}
	out, err := m.Run(ctx, "systemctl", action, "--", name)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return errors.New(strings.TrimSpace(string(exitErr.Stderr)))
		}
		return fmt.Errorf("systemctl %s: %v %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Refresh re-reads one service after the dashboard changed it, without raising events: whoever
// restarted it already knows. A later failure is still announced as usual.
func (m *Monitor) Refresh(ctx context.Context, name string) (Unit, error) {
	if _, ok := m.Get(name); !ok {
		return Unit{}, ErrUnknown
	}
	out, err := m.Run(ctx, "systemctl", "show", "--property="+properties, "--", name)
	if err != nil {
		return Unit{}, fmt.Errorf("systemctl show: %w", err)
	}
	u, ok := parseShow(out, time.Now())[name]
	if !ok || u.Name == "" {
		return Unit{}, ErrUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.units[name]
	u.Ignored, u.Flapping = prev.Ignored, prev.Flapping
	if !u.Failed() {
		delete(m.failedAt, name)
	} else if !prev.Failed() {
		m.failedAt[name] = time.Now()
	}
	m.units[name] = u
	return u, nil
}
