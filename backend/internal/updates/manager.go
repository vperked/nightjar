package updates

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/logx"
	"nightjar/internal/storage"
)

var log = logx.Component("Updates")

const (
	maxRuns      = 5
	lastCheckKey = "updates.last_check"
	seededKey    = "updates.seeded"
	// recipesKey is the version of the update recipes last filled into saved targets.
	recipesKey = "updates.recipes"
	recipes    = "4"
	// probeTimeout is the longest one check may take.
	probeTimeout = 15 * time.Minute
)

// Options configure a Manager. Zero values get working defaults.
type Options struct {
	Store       *storage.Store
	Run         Runner
	HTTP        *http.Client
	GitHubAPI   string
	GitHubToken string
	// Timeout is the longest one update may take.
	Timeout time.Duration
	// Poll is how often a restarting service is looked at.
	Poll time.Duration
	// Alerts receives update news; nil logs only.
	Alerts discord.Sender
	// NotifyAvailable announces updates that wait for the dashboard.
	NotifyAvailable bool
	// Restart restarts the monitor after it updated itself.
	Restart func()
}

// State is a target's last known state, kept across restarts.
type State struct {
	Result
	Checked float64 `json:"checked,omitempty"`
	Error   string  `json:"error,omitempty"`
	// Notified and Attempted name the update already announced and already auto-installed,
	// so neither happens again until a newer one appears.
	Notified  string `json:"notified,omitempty"`
	Attempted string `json:"attempted,omitempty"`
	Runs      []Run  `json:"runs,omitempty"` // newest first
}

// Run is one update attempt.
type Run struct {
	At     float64 `json:"at"`
	From   string  `json:"from,omitempty"`
	To     string  `json:"to,omitempty"`
	OK     bool    `json:"ok"`
	Auto   bool    `json:"auto"`
	Detail string  `json:"detail,omitempty"`
	// Log is the end of what the update did and printed.
	Log []string `json:"log,omitempty"`
}

// Status is a target as the dashboard sees it. The API key never leaves the server.
type Status struct {
	Spec
	State
	APIKeySet bool   `json:"api_key_set,omitempty"`
	CanUpdate bool   `json:"can_update"`
	Busy      string `json:"busy,omitempty"` // "checking", "updating" or "queued"
	// Live is what the update under way has done so far.
	Live *Live `json:"live,omitempty"`
}

type target struct {
	spec  Spec
	state State
	busy  string
	live  *liveLog // while it updates
}

type job struct {
	key  string // "check", "check:<id>" or "apply:<id>"
	id   string
	auto bool
}

// Manager checks and updates targets one at a time, on a worker of its own.
type Manager struct {
	opt Options

	mu        sync.Mutex
	order     []string
	targets   map[string]*target
	arrAPI    map[string]string
	queued    map[string]bool
	jobs      chan job
	running   sync.WaitGroup
	lastCheck float64
	checking  bool
	applying  string // the target being updated now, if any
	stopping  bool   // Wait was called: no job starts after it
}

// New loads the stored targets. On the first start it stores what detect finds; legacyAuto turns on
// automatic OS package updates for monitors that had SYSTEM_UPDATE_APPLY set.
func New(opt Options, detect func() []Spec, legacyAuto bool) *Manager {
	if opt.Run == nil {
		opt.Run = Exec
	}
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	opt.GitHubAPI = cmp.Or(opt.GitHubAPI, "https://api.github.com")
	opt.Timeout = cmp.Or(opt.Timeout, time.Hour)
	opt.Poll = cmp.Or(opt.Poll, 2*time.Second)
	m := &Manager{opt: opt, targets: map[string]*target{}, arrAPI: map[string]string{}, queued: map[string]bool{}, jobs: make(chan job, 64)}
	for _, t := range opt.Store.UpdateTargets() {
		spec, err := ParseSpec(t.Spec)
		if err != nil {
			log.Warn("Skipping stored update target", "id", t.ID, "err", err)
			continue
		}
		tg := &target{spec: spec}
		json.Unmarshal(t.State, &tg.state)
		m.order = append(m.order, spec.ID)
		m.targets[spec.ID] = tg
	}
	m.lastCheck, _ = strconv.ParseFloat(opt.Store.Meta(lastCheckKey), 64)
	if opt.Store.Available() && opt.Store.Meta(seededKey) == "" && detect != nil {
		for _, s := range detect() {
			s.Auto = s.Auto || (s.Kind == KindSystem && legacyAuto)
			if m.add(s) == nil {
				log.Info("Found something to keep updated", "name", s.Name, "kind", s.Kind)
			}
		}
		opt.Store.SetMeta(seededKey, "1")
		opt.Store.SetMeta(recipesKey, recipes)
	}
	// Targets saved before Nightjar knew how to update them get the way now, once.
	if opt.Store.Available() && opt.Store.Meta(recipesKey) != recipes && detect != nil {
		m.fillRecipes(detect())
		opt.Store.SetMeta(recipesKey, recipes)
	}
	go m.work()
	return m
}

// ---------- reading ----------

// Statuses lists every target in display order.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.status(m.targets[id]))
	}
	return out
}

func (m *Manager) status(t *target) Status {
	s := Status{Spec: t.spec, State: t.state, APIKeySet: t.spec.APIKey != "", CanUpdate: t.spec.CanUpdate(), Busy: t.busy}
	s.Spec.APIKey = ""
	s.Runs = slices.Clone(t.state.Runs)
	if t.live != nil {
		s.Live = t.live.view()
	}
	return s
}

// Get returns one target.
func (m *Manager) Get(id string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok {
		return Status{}, false
	}
	return m.status(t), true
}

// Applying is the target being updated now ("" when none): its download looks like a surge of traffic.
func (m *Manager) Applying() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.applying
}

// Activity reports whether a check round is running and when the last one finished.
func (m *Manager) Activity() (checking bool, lastCheck float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.checking, m.lastCheck
}

// ---------- changing ----------

// Save adds a target (oldID "") or replaces the one called oldID. An empty API key keeps the stored one.
func (m *Manager) Save(oldID string, spec Spec) (Status, error) {
	if err := spec.normalize(); err != nil {
		return Status{}, err
	}
	if !m.opt.Store.Available() {
		return Status{}, storage.ErrUnavailable
	}
	if oldID == "" {
		if err := m.add(spec); err != nil {
			return Status{}, err
		}
		m.Check(spec.ID)
		st, _ := m.Get(spec.ID)
		return st, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[oldID]
	switch {
	case !ok:
		return Status{}, ErrNotFound
	case t.busy == "updating":
		return Status{}, ErrBusy
	case spec.ID != oldID:
		return Status{}, errors.New("a target's id cannot change")
	}
	if spec.APIKey == "" {
		spec.APIKey = t.spec.APIKey
	}
	// The dashboard does not send the release download a recipe checks; with the same command, it
	// stays (without it, the command refuses to install an unchecked download).
	if spec.Asset == "" && spec.UpdateCommand == t.spec.UpdateCommand {
		spec.Asset, spec.AssetRepo = t.spec.Asset, t.spec.AssetRepo
	}
	data, _ := json.Marshal(spec)
	if err := m.opt.Store.SaveUpdateTarget(spec.ID, data); err != nil {
		return Status{}, err
	}
	changed := spec.Kind != t.spec.Kind || spec.Commands() != t.spec.Commands() || spec.Package != t.spec.Package || spec.Repo != t.spec.Repo || spec.URL != t.spec.URL
	t.spec = spec
	delete(m.arrAPI, spec.ID)
	if changed {
		t.state = State{Runs: t.state.Runs}
		m.enqueueLocked(job{key: "check:" + spec.ID, id: spec.ID})
	}
	return m.status(t), nil
}

func (m *Manager) add(spec Spec) error {
	if err := spec.normalize(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.targets[spec.ID]; taken {
		return ErrExists
	}
	// The monitor has one checkout: one kept under an older name (servermonitor) is the same one.
	if spec.Self && slices.ContainsFunc(m.order, func(id string) bool { return m.targets[id].spec.Self }) {
		return ErrExists
	}
	data, _ := json.Marshal(spec)
	if err := m.opt.Store.SaveUpdateTarget(spec.ID, data); err != nil {
		return err
	}
	m.order = append(m.order, spec.ID)
	m.targets[spec.ID] = &target{spec: spec}
	return nil
}

// Delete removes a target.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	switch {
	case !ok:
		return ErrNotFound
	case t.busy == "updating":
		return ErrBusy
	}
	if err := m.opt.Store.DeleteUpdateTarget(id); err != nil {
		return err
	}
	delete(m.targets, id)
	m.order = slices.DeleteFunc(m.order, func(o string) bool { return o == id })
	return nil
}

// AddDetected stores the detected targets not already present and returns them.
func (m *Manager) AddDetected(detected []Spec) []Status {
	m.fillRecipes(detected)
	var added []Status
	for _, s := range detected {
		if m.add(s) == nil {
			st, _ := m.Get(s.ID)
			added = append(added, st)
			m.Check(s.ID)
		}
	}
	return added
}

// fillRecipes gives a saved target that cannot update the update command detection now knows for
// it, with the version command and restart that go with it. A target that has an update command,
// one set up by hand, is left alone.
func (m *Manager) fillRecipes(detected []Spec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range detected {
		t := m.targets[d.ID]
		if t == nil || d.UpdateCommand == "" || t.spec.Kind != d.Kind || t.busy != "" {
			continue
		}
		// One set up by hand stays; one from an older recipe gets the newer. One from this recipe
		// gets what goes with it now: who runs it, and the download it checks.
		same := t.spec.UpdateCommand == d.UpdateCommand
		if t.spec.UpdateCommand != "" && !same && !fromOlderRecipe(t.spec.UpdateCommand, d.UpdateCommand) {
			continue
		}
		if same && (d.Kind != KindRelease || (t.spec.Dir == d.Dir && t.spec.Asset == d.Asset && t.spec.AssetRepo == d.AssetRepo)) {
			continue
		}
		spec := t.spec
		spec.UpdateCommand, spec.VersionCommand, spec.Restart = d.UpdateCommand, d.VersionCommand, d.Restart
		spec.Unit = cmp.Or(spec.Unit, d.Unit)
		if d.Kind == KindRelease {
			spec.Dir, spec.Asset, spec.AssetRepo = d.Dir, d.Asset, d.AssetRepo
		}
		data, _ := json.Marshal(spec)
		if err := m.opt.Store.SaveUpdateTarget(spec.ID, data); err != nil {
			log.Warn("Could not give a target its update command", "target", spec.Name, "err", err)
			continue
		}
		t.spec = spec
		log.Info("Can now update", "target", spec.Name)
	}
}

// fromOlderRecipe says whether a saved command is the recipe's command as an older Nightjar wrote it.
func fromOlderRecipe(saved, recipe string) bool {
	if saved == recipe {
		return false
	}
	upgraded := saved
	for _, r := range superseded {
		upgraded = strings.ReplaceAll(upgraded, r[0], r[1])
	}
	return upgraded == recipe
}

// ---------- scheduling ----------

// CheckAll queues a check of every target that is not paused.
func (m *Manager) CheckAll() { m.enqueue(job{key: "check"}) }

// Check queues a check of one target.
func (m *Manager) Check(id string) { m.enqueue(job{key: "check:" + id, id: id}) }

// Apply queues an update of one target.
func (m *Manager) Apply(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	switch {
	case !ok:
		return ErrNotFound
	case !t.spec.CanUpdate():
		return errors.New("this target has no update command; it is only checked")
	}
	m.enqueueLocked(job{key: "apply:" + id, id: id})
	return nil
}

// Tick runs from the monitoring loop: it starts a check round when one is due (and at the start of
// each update window), then queues automatic updates while the window is open. The monitor's own
// update goes last, since it restarts the process.
func (m *Manager) Tick(now time.Time, interval time.Duration, window Window) {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := time.Unix(int64(m.lastCheck), 0)
	open := window.Contains(now)
	hasAuto := slices.ContainsFunc(m.order, func(id string) bool { return m.targets[id].spec.Auto })
	// A round under way counts: lastCheck moves only when it ends.
	if !m.checking && (now.Sub(last) >= interval || (open && hasAuto && !window.Always() && last.Before(window.Opened(now)))) {
		m.enqueueLocked(job{key: "check"})
	}
	if !open || m.checking || m.queued["check"] {
		return
	}
	var self *target
	for _, id := range m.order {
		t := m.targets[id]
		if !t.spec.Auto || t.spec.Paused || !t.state.Available || t.busy != "" || t.state.Attempted == t.state.key() || !t.spec.CanUpdate() {
			continue
		}
		if t.spec.Self {
			self = t
			continue
		}
		m.enqueueLocked(job{key: "apply:" + id, id: id, auto: true})
	}
	if self != nil && !slices.ContainsFunc(m.order, func(id string) bool { return m.targets[id].busy != "" }) {
		m.enqueueLocked(job{key: "apply:" + self.spec.ID, id: self.spec.ID, auto: true})
	}
}

func (m *Manager) enqueue(j job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enqueueLocked(j)
}

func (m *Manager) enqueueLocked(j job) {
	if m.queued[j.key] {
		return
	}
	select {
	case m.jobs <- j:
		m.queued[j.key] = true
		if t := m.targets[j.id]; t != nil && t.busy == "" {
			t.busy = "queued"
		}
	default:
		log.Warn("Update queue is full; dropping", "job", j.key)
	}
}

// Wait stops new jobs from starting and blocks until the running one ends, so shutdown does not
// cut it short. A check gets timeout. An update gets as long as it may take, however long that is:
// apt or dpkg stopped halfway leaves the machine's packages broken, and a restart replaces this
// process, which stops it at its next line of output.
func (m *Manager) Wait(timeout time.Duration) {
	m.mu.Lock()
	m.stopping = true
	if t := m.targets[m.applying]; t != nil {
		timeout = max(timeout, m.opt.Timeout+probeTimeout+5*time.Minute)
		log.Info("Waiting for the update under way to finish before stopping", "target", t.spec.Name)
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Warn("Stopping while an update is still running")
	}
}

func (m *Manager) work() {
	for j := range m.jobs {
		m.mu.Lock()
		delete(m.queued, j.key)
		if m.stopping {
			if t := m.targets[j.id]; t != nil && t.busy == "queued" {
				t.busy = ""
			}
			m.mu.Unlock()
			continue
		}
		m.running.Add(1)
		// Marked with the job taken, so Wait cannot miss an update about to start.
		if strings.HasPrefix(j.key, "apply:") {
			m.applying = j.id
		}
		// And a check round as running from here, so a Tick in between does not queue another.
		if j.key == "check" {
			m.checking = true
		}
		m.mu.Unlock()
		func() {
			defer m.running.Done()
			defer func() {
				if p := recover(); p != nil {
					log.Error("Update job panicked", "job", j.key, "panic", fmt.Sprint(p))
				}
			}()
			switch {
			case j.key == "check":
				m.checkAll()
			case strings.HasPrefix(j.key, "check:"):
				if st, isNew := m.checkOne(&round{latest: map[string]string{}}, j.id); isNew {
					m.announce([]Status{st})
				}
			default:
				defer func() {
					m.mu.Lock()
					m.applying = ""
					m.mu.Unlock()
				}()
				m.apply(j.id, j.auto)
			}
		}()
	}
}

// ---------- jobs ----------

func (m *Manager) begin(id, busy string) (Spec, State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok {
		return Spec{}, State{}, false
	}
	t.busy = busy
	return t.spec, t.state, true
}

// finish stores a target's new state, unless it was deleted meanwhile.
func (m *Manager) finish(id string, update func(*State)) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.targets[id]
	if !ok {
		return State{}
	}
	update(&t.state)
	if m.queued["apply:"+id] || m.queued["check:"+id] {
		t.busy = "queued"
	} else {
		t.busy = ""
	}
	data, _ := json.Marshal(t.state)
	if err := m.opt.Store.SaveUpdateState(id, data); err != nil {
		log.Warn("Could not store update state", "id", id, "err", err)
	}
	return t.state
}

func (m *Manager) checkAll() {
	m.mu.Lock()
	m.checking = true
	ids := slices.Clone(m.order)
	m.mu.Unlock()
	r := &round{latest: map[string]string{}}
	var news []Status
	for _, id := range ids {
		if st, isNew := m.checkOne(r, id); isNew {
			news = append(news, st)
		}
	}
	now := float64(time.Now().Unix())
	m.mu.Lock()
	m.checking, m.lastCheck = false, now
	m.mu.Unlock()
	m.opt.Store.SetMeta(lastCheckKey, strconv.FormatFloat(now, 'f', 0, 64))
	m.announce(news)
}

// checkOne checks a target and reports whether it found an update not announced before that
// waits for the dashboard.
func (m *Manager) checkOne(r *round, id string) (Status, bool) {
	spec, _, ok := m.begin(id, "checking")
	if !ok || spec.Paused {
		m.finish(id, func(*State) {})
		return Status{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	res, err := m.probe(ctx, r, spec)
	isNew := false
	state := m.finish(id, func(s *State) {
		s.Checked = float64(time.Now().Unix())
		s.Error = ""
		if err != nil {
			s.Error = err.Error()
			if res.Installed != "" {
				s.Installed = res.Installed
			}
			return
		}
		s.Result = res
		if res.Available && s.Notified != res.key() {
			s.Notified = res.key()
			isNew = !spec.Auto
		}
	})
	if err != nil {
		log.Warn("Update check failed", "target", spec.Name, "err", err)
	} else if res.Available {
		log.Info("Update available", "target", spec.Name, "change", change(spec, res))
	}
	return Status{Spec: spec, State: state}, isNew
}

func (m *Manager) apply(id string, auto bool) {
	spec, before, ok := m.begin(id, "updating")
	if !ok {
		return
	}
	// Check first, so it installs what is on offer now rather than at the last check.
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	if res, err := m.probe(ctx, &round{latest: map[string]string{}}, spec); err == nil {
		before.Result = res
	}
	cancel()
	if !before.Available {
		m.finish(id, func(s *State) { s.Result, s.Checked, s.Error = before.Result, float64(time.Now().Unix()), "" })
		log.Info("Nothing to update", "target", spec.Name)
		return
	}
	m.mu.Lock()
	live := newLiveLog()
	if t := m.targets[id]; t != nil {
		t.state.Attempted = before.key()
		t.live = live
	}
	m.mu.Unlock()

	log.Info("Updating", "target", spec.Name, "change", change(spec, before.Result), "auto", auto)
	ctx, cancel = context.WithTimeout(context.Background(), m.opt.Timeout+5*time.Minute)
	defer cancel()
	ctx = withLive(ctx, live)
	live.note("Updating " + spec.Name + ": " + change(spec, before.Result))
	detail, err := m.install(ctx, spec, before.Result, auto)
	if err == nil {
		live.note("Checking it is running")
		err = m.verifyRunning(ctx, spec)
	}
	live.note("Checking the version now installed")
	after, probeErr := m.probe(ctx, &round{latest: map[string]string{}}, spec)
	run := Run{At: float64(time.Now().Unix()), From: before.Installed, To: after.Installed, OK: err == nil, Auto: auto, Detail: detail}
	if err != nil {
		run.Detail = err.Error()
		live.note("Failed: " + err.Error())
	} else {
		live.note("Done")
	}
	run.Log = live.tail()
	m.report(spec, before.Result, run, err)
	m.mu.Lock()
	if t := m.targets[id]; t != nil {
		t.live = nil
	}
	m.mu.Unlock()
	m.finish(id, func(s *State) {
		s.Runs = append([]Run{run}, s.Runs...)[:min(maxRuns, len(s.Runs)+1)]
		s.Checked = run.At
		if probeErr == nil {
			s.Result, s.Error = after, ""
		}
	})
	if err == nil && spec.Self && m.opt.Restart != nil {
		log.Info("Restarting into the updated monitor")
		m.opt.Restart()
	}
}

// ---------- alerts ----------

// change describes an update in a few words: "4.0.14 → 4.0.15", "12 packages" or "3 commits".
func change(s Spec, r Result) string {
	switch {
	case s.Kind == KindSystem:
		return plural(len(r.Pending), "package")
	case s.Kind == KindSnap:
		return plural(len(r.Pending), "snap")
	case s.Kind == KindGit:
		return fmt.Sprintf("%s → %s (%s)", r.Installed, r.Latest, plural(len(r.Pending), "commit"))
	case r.Installed != "" && r.Latest != "":
		return r.Installed + " → " + r.Latest
	}
	return r.Latest
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if n >= 20 && word == "commit" {
		return "20+ commits"
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (m *Manager) announce(news []Status) {
	if len(news) == 0 || m.opt.Alerts == nil || !m.opt.NotifyAvailable {
		return
	}
	title := fmt.Sprintf("%d updates available", len(news))
	if len(news) == 1 {
		title = news[0].Name + " update available"
	}
	var fields []discord.Field
	for _, st := range news[:min(len(news), 20)] {
		fields = append(fields, discord.Field{Name: st.Name, Value: change(st.Spec, st.Result), Inline: true})
	}
	m.opt.Alerts.SendEmbed(title, "Install from the dashboard's Updates page, or turn on auto-update for them.", discord.Info, fields, "Updates")
}

func (m *Manager) report(s Spec, offered Result, run Run, err error) {
	how := "From the dashboard"
	if run.Auto {
		how = "Automatically"
	}
	var nr *notRunningError
	switch {
	case err == nil:
		log.Info("Updated", "target", s.Name, "change", change(s, offered))
	case errors.As(err, &nr):
		log.Error("Service not running after update", "target", s.Name, "err", err)
	default:
		log.Warn("Update failed", "target", s.Name, "err", err)
	}
	if m.opt.Alerts == nil {
		return
	}
	fields := []discord.Field{{Name: "Change", Value: change(s, offered), Inline: true}, {Name: "Started", Value: how, Inline: true}}
	switch {
	case err == nil:
		m.opt.Alerts.SendEmbed(s.Name+" updated", cmp.Or(run.Detail, "Installed and running."), discord.OK, fields, "Updates")
	case errors.As(err, &nr):
		m.opt.Alerts.SendEmbed(s.Name+" is down after its update", discord.Truncate(err.Error(), 1000), discord.Critical, fields, "Updates")
	default:
		m.opt.Alerts.SendEmbed(s.Name+" update failed", discord.Truncate(err.Error(), 1000), discord.Warning, fields, "Updates")
	}
}
