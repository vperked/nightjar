package agent

// The agent keeps its machine's software up to date the way the dashboard keeps its own: the same
// update manager (package updates), with its targets in a file in the agent's state folder and its
// commands run outside the agent's sandbox (UnitRunner). The dashboard manages it through these
// routes, which mirror its own /api/updates, and polls them to announce what it finds. They take
// the token like readings do, and change things only over the HTTPS the token pins.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"nightjar/internal/collect"
	"nightjar/internal/storage/record"
	"nightjar/internal/updates"
)

// UpdatesPath is where the dashboard manages updates.
const UpdatesPath = "/v1/updates"

// updatesFile keeps the targets, their state and the settings.
const updatesFile = StateDir + "/updates.json"

// UpdateSettings are the agent's update schedule, set from the dashboard (there is no settings page
// on a server, nor a config file for it).
type UpdateSettings struct {
	Enabled bool `json:"enabled"`
	// Interval is the seconds between checks.
	Interval int          `json:"interval"`
	Window   UpdateWindow `json:"window"`
	// Notify has the dashboard announce updates that wait for it.
	Notify bool `json:"notify"`
}

// UpdateWindow is when automatic updates may start, in minutes after the server's local midnight
// (240 is 04:00); the same start and end means any time.
type UpdateWindow struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Label string `json:"label,omitempty"`
}

// DefaultUpdateSettings check every 6 hours and install automatic updates from 04:00 to 06:00.
func DefaultUpdateSettings() UpdateSettings {
	return UpdateSettings{Enabled: true, Interval: 21600, Window: UpdateWindow{Start: 240, End: 360}, Notify: true}
}

// Limits on the settings: the dashboard's own for its checks, and a day's minutes.
const (
	minUpdateInterval = 300
	maxUpdateInterval = 2592000
	dayMinutes        = 24 * 60
)

// TimeZone is the server's clock, which the window runs on.
type TimeZone struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"` // seconds east of UTC, now
}

// UpdatesView is what GET UpdatesPath answers: the dashboard's own GET /api/updates, plus notify
// and the time zone.
type UpdatesView struct {
	Enabled          bool             `json:"enabled"`
	Interval         int              `json:"interval"`
	Window           UpdateWindow     `json:"window"`
	CommandsEditable bool             `json:"commands_editable"`
	Kinds            []updates.Kind   `json:"kinds"`
	Checking         bool             `json:"checking"`
	LastCheck        float64          `json:"last_check"`
	Targets          []updates.Status `json:"targets"`
	Notify           bool             `json:"notify"`
	TimeZone         TimeZone         `json:"time_zone"`
}

// SettingsStore keeps targets and the settings with them: an updates.FileStore.
type SettingsStore interface {
	updates.Store
	Settings() json.RawMessage
	SaveSettings(json.RawMessage) error
}

// Updates is the agent's update manager with its settings.
type Updates struct {
	Manager *updates.Manager
	store   SettingsStore
	detect  func() []updates.Spec

	mu       sync.Mutex
	settings UpdateSettings
}

// NewUpdates starts the update manager on store; detect finds what to keep updated (never the
// agent itself, which the dashboard updates). On its first start it keeps what detect finds.
func NewUpdates(store SettingsStore, opt updates.Options, detect func() []updates.Spec) *Updates {
	u := &Updates{store: store, settings: DefaultUpdateSettings()}
	u.detect = func() []updates.Spec {
		if detect == nil {
			return nil
		}
		return slices.DeleteFunc(detect(), func(s updates.Spec) bool { return s.Self })
	}
	if raw := store.Settings(); len(raw) > 0 {
		st := u.settings
		if err := json.Unmarshal(raw, &st); err != nil || st.validate() != nil {
			slog.Warn("update settings unreadable; using the defaults", "err", err)
		} else {
			u.settings = st
		}
	}
	opt.Store = store
	u.Manager = updates.New(opt, u.detect, false)
	return u
}

func (s UpdateSettings) validate() error {
	switch {
	case s.Interval < minUpdateInterval || s.Interval > maxUpdateInterval:
		return errors.New("interval must be between 300 seconds and 30 days")
	case s.Window.Start < 0 || s.Window.Start >= dayMinutes || s.Window.End < 0 || s.Window.End >= dayMinutes:
		return errors.New("the window's start and end are minutes after midnight, from 0 to 1439")
	}
	return nil
}

func (s UpdateSettings) window() updates.Window {
	return updates.MinuteWindow(s.Window.Start, s.Window.End)
}

// UpdateSettingsChange is a new schedule from the dashboard; what it leaves out stays as it was.
type UpdateSettingsChange struct {
	Enabled  *bool    `json:"enabled,omitempty"`
	Interval *float64 `json:"interval,omitempty"`
	Window   *struct {
		Start *int `json:"start,omitempty"`
		End   *int `json:"end,omitempty"`
	} `json:"window,omitempty"`
	Notify *bool `json:"notify,omitempty"`
}

func (c UpdateSettingsChange) apply(st UpdateSettings) (UpdateSettings, error) {
	if c.Enabled != nil {
		st.Enabled = *c.Enabled
	}
	if c.Interval != nil {
		if *c.Interval != float64(int(*c.Interval)) {
			return st, errors.New("interval must be whole seconds")
		}
		st.Interval = int(*c.Interval)
	}
	if c.Window != nil && c.Window.Start != nil {
		st.Window.Start = *c.Window.Start
	}
	if c.Window != nil && c.Window.End != nil {
		st.Window.End = *c.Window.End
	}
	if c.Notify != nil {
		st.Notify = *c.Notify
	}
	return st, nil
}

// Settings are the update schedule now.
func (u *Updates) Settings() UpdateSettings {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.settings
}

// SaveSettings validates and keeps a new schedule.
func (u *Updates) SaveSettings(s UpdateSettings) error {
	s.Window.Label = ""
	if err := s.validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(s)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.store.SaveSettings(raw); err != nil {
		return err
	}
	u.settings = s
	return nil
}

// View is the agent's updates as GET UpdatesPath answers them.
func (u *Updates) View(now time.Time) UpdatesView {
	st := u.Settings()
	st.Window.Label = st.window().String()
	checking, last := u.Manager.Activity()
	name, offset := collect.TimeZone(now)
	return UpdatesView{Enabled: st.Enabled, Interval: st.Interval, Window: st.Window, CommandsEditable: true, Kinds: updates.Kinds,
		Checking: checking, LastCheck: last, Targets: u.Manager.Statuses(), Notify: st.Notify, TimeZone: TimeZone{Name: name, Offset: offset}}
}

// Tick hands the manager the clock, as the dashboard's monitoring loop does for its own.
func (u *Updates) Tick(now time.Time) {
	st := u.Settings()
	if st.Enabled {
		u.Manager.Tick(now, time.Duration(st.Interval)*time.Second, st.window())
	}
}

// Run ticks every minute until ctx ends.
func (u *Updates) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	u.Tick(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			u.Tick(now)
		}
	}
}

// Busy names the target being updated now, "" when none: the agent does not replace itself then,
// which would cut the update off.
func (u *Updates) Busy() string {
	if u == nil {
		return ""
	}
	id := u.Manager.Applying()
	if id == "" {
		return ""
	}
	if st, ok := u.Manager.Get(id); ok {
		return st.Name
	}
	return id
}

// UseUpdates gives a sampler updates to manage, as Serve does; for tests.
func (s *Sampler) UseUpdates(u *Updates) { s.updates = u }

// DetectWait is how long finding what to keep updated may take; it runs a few dozen commands. The
// dashboard waits as long for the answer.
const DetectWait = 2 * time.Minute

func updatesRoutes(mux *http.ServeMux, s *Sampler, authorized func(*http.Request) bool, secure bool) {
	fail := func(w http.ResponseWriter, status int, msg string) {
		writeJSON(w, status, map[string]string{"error": msg})
	}
	guard := func(w http.ResponseWriter, r *http.Request, change bool) bool {
		switch {
		case !authorized(r):
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, http.StatusUnauthorized, "unauthorized")
		case change && !secure:
			fail(w, http.StatusForbidden, "this agent serves plain HTTP; reinstall it with HTTPS to manage its updates from the dashboard")
		case s.updates == nil:
			fail(w, http.StatusNotFound, "this agent does not manage updates")
		default:
			return true
		}
		return false
	}
	refuse := func(w http.ResponseWriter, err error) {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, updates.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, updates.ErrExists), errors.Is(err, updates.ErrBusy):
			status = http.StatusConflict
		case errors.Is(err, record.ErrUnavailable):
			status = http.StatusServiceUnavailable
		}
		fail(w, status, err.Error())
	}
	mux.HandleFunc("GET "+UpdatesPath, func(w http.ResponseWriter, r *http.Request) {
		if guard(w, r, false) {
			writeJSON(w, http.StatusOK, s.updates.View(time.Now()))
		}
	})
	// save adds a target (no id in the path) or replaces the one in it.
	save := func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			fail(w, http.StatusBadRequest, "could not read the target")
			return
		}
		spec, err := updates.ParseSpec(body)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		// The agent itself is updated by the dashboard, never as a target.
		spec.Self = false
		id := r.PathValue("target")
		st, err := s.updates.Manager.Save(id, spec)
		if err != nil {
			refuse(w, err)
			return
		}
		code := http.StatusOK
		if id == "" {
			code = http.StatusCreated
		}
		writeJSON(w, code, map[string]any{"target": st})
	}
	mux.HandleFunc("POST "+UpdatesPath, save)
	mux.HandleFunc("PUT "+UpdatesPath+"/{target}", save)
	mux.HandleFunc("DELETE "+UpdatesPath+"/{target}", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		if err := s.updates.Manager.Delete(r.PathValue("target")); err != nil {
			refuse(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	check := func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		if id := r.PathValue("target"); id != "" {
			if _, ok := s.updates.Manager.Get(id); !ok {
				refuse(w, updates.ErrNotFound)
				return
			}
			s.updates.Manager.Check(id)
		} else {
			s.updates.Manager.CheckAll()
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	}
	mux.HandleFunc("POST "+UpdatesPath+"/check", check)
	mux.HandleFunc("POST "+UpdatesPath+"/{target}/check", check)
	mux.HandleFunc("POST "+UpdatesPath+"/{target}/update", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		if err := s.updates.Manager.Apply(r.PathValue("target")); err != nil {
			refuse(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	})
	mux.HandleFunc("POST "+UpdatesPath+"/detect", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		// Detection runs a few dozen commands: longer than the server's write timeout.
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(DetectWait))
		added := s.updates.Manager.AddDetected(s.updates.detect())
		if added == nil {
			added = []updates.Status{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"added": added})
	})
	mux.HandleFunc("PUT "+UpdatesPath+"/settings", func(w http.ResponseWriter, r *http.Request) {
		if !guard(w, r, true) {
			return
		}
		var body UpdateSettingsChange
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, `expected {"enabled": true, "interval": 21600, "window": {"start": 240, "end": 360}, "notify": true}`)
			return
		}
		st, err := body.apply(s.updates.Settings())
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.updates.SaveSettings(st); err != nil {
			status := http.StatusBadRequest
			if st.validate() == nil {
				status = http.StatusInternalServerError // it was fine; keeping it failed
			}
			fail(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.updates.View(time.Now()))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// startUpdates opens the agent's update targets and starts its manager, whose commands run outside
// the agent's sandbox. Detection runs in the agent itself: it only looks.
func startUpdates() *Updates {
	store, err := updates.OpenFileStore(updatesFile)
	if err != nil {
		slog.Warn("update targets", "err", err)
	}
	runner := &UnitRunner{}
	return NewUpdates(store, updates.Options{Run: runner.Run, Settle: runner.Settle}, func() []updates.Spec { return updates.Detect(updates.Exec) })
}
