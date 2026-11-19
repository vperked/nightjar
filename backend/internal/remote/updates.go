package remote

// Each agent keeps its own server's software up to date (see agent.Updates). The dashboard manages
// that through the agent, and polls it every few minutes (more often while something is under way,
// and right after a change made here) to show how many updates wait on each server and to announce,
// through its own alerts, the updates that wait and the updates that ran.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/discord"
	"nightjar/internal/updates"
)

// UpdateMark is what was announced about one of a server's update targets, kept with the server so a
// restart does not announce it again.
type UpdateMark struct {
	// Run is when the newest run announced finished.
	Run float64 `json:"run,omitempty"`
	// Version is the update announced as waiting (the agent's "notified").
	Version string `json:"version,omitempty"`
}

// HostUpdatesSummary is one server's updates as the dashboard last polled them.
type HostUpdatesSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available int    `json:"available"` // targets with an update waiting, paused ones aside
	Applying  string `json:"applying"`  // the target updating now, "" when none
	Checked   int64  `json:"checked"`   // when the agent last checked them all, 0 before
	Error     string `json:"error"`     // why the last poll failed
}

// How often each agent's updates are polled: every few minutes, and while one is under way often
// enough to announce its end soon after.
const (
	updatesEvery = 5 * time.Minute
	updatesBusy  = 30 * time.Second
	updatesWait  = 30 * time.Second
)

// DetectWait is how long the dashboard waits for an agent to look for what to keep updated: as long
// as the agent lets detection take, and a little more for the answer to come back. With less, a
// slow detection read as an agent that does not answer while it went on.
const DetectWait = agent.DetectWait + 15*time.Second

// errNoAgentUpdates is a server without an agent, asked about its updates; it is ErrNoAgent.
var errNoAgentUpdates = noAgentError("this server has no agent; install it to keep its software updated from here")

// UpdatesTooOld is what an agent from before it kept its server updated is told to do.
const UpdatesTooOld = "this server's agent is too old for updates; it updates itself shortly, or reinstall it"

// updatesTooOldShort is the same for the list of servers.
const updatesTooOldShort = "update the agent"

// HostUpdates asks a server's agent for its updates. What it answers also refreshes the list and
// what is announced.
func (m *Monitor) HostUpdates(ctx context.Context, id string) (agent.UpdatesView, error) {
	var v agent.UpdatesView
	_, err := m.updatesCall(ctx, id, http.MethodGet, agent.UpdatesPath, nil, &v)
	m.noteUpdates(id, &v, err)
	return v, err
}

// AddHostUpdate adds a target on a server.
func (m *Monitor) AddHostUpdate(ctx context.Context, id string, spec updates.Spec) (updates.Status, error) {
	var body struct {
		Target updates.Status `json:"target"`
	}
	_, err := m.updatesChange(ctx, id, http.MethodPost, agent.UpdatesPath, spec, &body)
	return body.Target, err
}

// SaveHostUpdate replaces a target on a server.
func (m *Monitor) SaveHostUpdate(ctx context.Context, id, target string, spec updates.Spec) (updates.Status, error) {
	var body struct {
		Target updates.Status `json:"target"`
	}
	_, err := m.updatesChange(ctx, id, http.MethodPut, agent.UpdatesPath+"/"+url.PathEscape(target), spec, &body)
	return body.Target, err
}

// DeleteHostUpdate removes a target from a server.
func (m *Monitor) DeleteHostUpdate(ctx context.Context, id, target string) error {
	_, err := m.updatesChange(ctx, id, http.MethodDelete, agent.UpdatesPath+"/"+url.PathEscape(target), nil, nil)
	return err
}

// CheckHostUpdates has a server check one target, or all of them for target "".
func (m *Monitor) CheckHostUpdates(ctx context.Context, id, target string) error {
	path := agent.UpdatesPath + "/check"
	if target != "" {
		path = agent.UpdatesPath + "/" + url.PathEscape(target) + "/check"
	}
	_, err := m.updatesChange(ctx, id, http.MethodPost, path, struct{}{}, nil)
	return err
}

// ApplyHostUpdate has a server install a target's update.
func (m *Monitor) ApplyHostUpdate(ctx context.Context, id, target string) error {
	_, err := m.updatesChange(ctx, id, http.MethodPost, agent.UpdatesPath+"/"+url.PathEscape(target)+"/update", struct{}{}, nil)
	return err
}

// DetectHostUpdates has a server look again for what to keep updated, and returns what it added.
func (m *Monitor) DetectHostUpdates(ctx context.Context, id string) ([]updates.Status, error) {
	var body struct {
		Added []updates.Status `json:"added"`
	}
	_, err := m.updatesChangeWithin(ctx, id, http.MethodPost, agent.UpdatesPath+"/detect", struct{}{}, &body, DetectWait)
	if body.Added == nil {
		body.Added = []updates.Status{}
	}
	return body.Added, err
}

// SetHostUpdateSettings changes a server's update schedule and returns its updates.
func (m *Monitor) SetHostUpdateSettings(ctx context.Context, id string, c agent.UpdateSettingsChange) (agent.UpdatesView, error) {
	var v agent.UpdatesView
	_, err := m.updatesCall(ctx, id, http.MethodPut, agent.UpdatesPath+"/settings", c, &v)
	if err == nil {
		m.noteUpdates(id, &v, nil)
	}
	return v, err
}

// updatesChange makes a change and polls the server's updates again, in the background, so the
// list and the alerts follow it.
func (m *Monitor) updatesChange(ctx context.Context, id, method, path string, body, out any) (int, error) {
	return m.updatesChangeWithin(ctx, id, method, path, body, out, updatesWait)
}

// updatesChangeWithin is updatesChange with a wait of its own for the agent's answer.
func (m *Monitor) updatesChangeWithin(ctx context.Context, id, method, path string, body, out any, wait time.Duration) (int, error) {
	status, err := m.updatesCallWithin(ctx, id, method, path, body, out, wait)
	if err == nil {
		m.mu.Lock()
		e := m.find(id)
		m.mu.Unlock()
		if e != nil {
			go m.pollUpdates(context.Background(), e)
		}
	}
	return status, err
}

// updatesCall asks an agent about its updates and reads its answer into out (when there is one). An
// agent's refusal comes back as an AgentError with its status and words.
func (m *Monitor) updatesCall(ctx context.Context, id, method, path string, body, out any) (int, error) {
	return m.updatesCallWithin(ctx, id, method, path, body, out, updatesWait)
}

func (m *Monitor) updatesCallWithin(ctx context.Context, id, method, path string, body, out any, wait time.Duration) (int, error) {
	status, raw, name, err := m.agentDo(ctx, id, method, path, body, errNoAgentUpdates, wait)
	if err != nil {
		return 0, err
	}
	if status < 200 || status > 299 {
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Error string `json:"error"`
		}
		switch {
		case json.Unmarshal(raw, &e) == nil && e.Error != "":
			msg = e.Error
		case tooOldAgent(status, msg):
			msg, status = UpdatesTooOld, http.StatusNotFound
		}
		return status, &AgentError{Status: status, Msg: msg}
	}
	if out != nil && status != http.StatusNoContent {
		if err := json.Unmarshal(raw, out); err != nil {
			return status, fmt.Errorf("%s did not answer like an agent: %v", name, err)
		}
	}
	return status, nil
}

// ---------- polling ----------

// UpdatesSummary lists each agent server's updates as last polled.
func (m *Monitor) UpdatesSummary() []HostUpdatesSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []HostUpdatesSummary{}
	for _, e := range m.hosts {
		if e.host.isSSHOnly() {
			continue
		}
		h := HostUpdatesSummary{ID: e.host.ID, Name: e.host.Name, Error: e.updatesErr}
		if v := e.updates; v != nil {
			h.Checked = int64(v.LastCheck)
			for _, t := range v.Targets {
				if t.Available && !t.Paused {
					h.Available++
				}
				if t.Busy == "updating" {
					h.Applying = t.Name
				}
			}
		}
		out = append(out, h)
	}
	return out
}

// updatesLoop polls the agents' updates until ctx ends: each every few minutes, one with something
// under way more often.
func (m *Monitor) updatesLoop(ctx context.Context) {
	t := time.NewTicker(updatesBusy)
	defer t.Stop()
	for {
		m.pollUpdatesDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PollUpdates polls every agent's updates now and waits for them.
func (m *Monitor) PollUpdates(ctx context.Context) {
	m.pollUpdatesWhere(ctx, func(*entry, time.Time) bool { return true })
}

func (m *Monitor) pollUpdatesDue(ctx context.Context) {
	m.pollUpdatesWhere(ctx, func(e *entry, now time.Time) bool {
		since := now.Sub(e.updatesAt)
		return since >= updatesEvery || (since >= updatesBusy && e.updates != nil && underWay(e.updates))
	})
}

func underWay(v *agent.UpdatesView) bool {
	return v.Checking || slices.ContainsFunc(v.Targets, func(t updates.Status) bool { return t.Busy != "" })
}

func (m *Monitor) pollUpdatesWhere(ctx context.Context, due func(*entry, time.Time) bool) {
	now := m.opt.Now()
	m.mu.Lock()
	var hosts []*entry
	for _, e := range m.hosts {
		if !e.host.isSSHOnly() && !e.updatesPolling && due(e, now) {
			hosts = append(hosts, e)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range hosts {
		wg.Go(func() { m.pollUpdates(ctx, e) })
	}
	wg.Wait()
}

// pollUpdates asks one agent for its updates, unless it is being asked already.
func (m *Monitor) pollUpdates(ctx context.Context, e *entry) {
	m.mu.Lock()
	if e.updatesPolling || !slices.Contains(m.hosts, e) {
		m.mu.Unlock()
		return
	}
	e.updatesPolling, e.updatesAt = true, m.opt.Now()
	id := e.host.ID
	m.mu.Unlock()
	var v agent.UpdatesView
	_, err := m.updatesCall(ctx, id, http.MethodGet, agent.UpdatesPath, nil, &v)
	m.mu.Lock()
	e.updatesPolling = false
	m.mu.Unlock()
	if ctx.Err() != nil {
		return // shutting down is not the agent's fault
	}
	m.noteUpdates(id, &v, err)
}

// noteUpdates takes in what an agent said about its updates (or why it could not be asked).
func (m *Monitor) noteUpdates(id string, v *agent.UpdatesView, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil {
		return
	}
	e.updatesAt = m.opt.Now()
	var ae *AgentError
	switch {
	case errors.As(err, &ae) && ae.Msg == UpdatesTooOld:
		e.updatesErr = updatesTooOldShort
	case err != nil:
		e.updatesErr = err.Error()
	default:
		e.updatesErr = ""
		e.updates = v
		m.announceUpdatesLocked(e, v)
	}
}

// announceUpdatesLocked announces what is new in a poll: updates that wait for the dashboard (when
// both this dashboard and the server announce them, as UPDATES_NOTIFY and the agent's notify say)
// and runs that finished, each once. The first poll of a server takes note of the runs it already
// had without announcing them.
func (m *Monitor) announceUpdatesLocked(e *entry, v *agent.UpdatesView) {
	first := e.host.Updates == nil
	marks := map[string]UpdateMark{}
	var waiting []updates.Status
	type finished struct {
		target updates.Status
		run    updates.Run
	}
	var runs []finished
	for _, t := range v.Targets {
		before := e.host.Updates[t.ID]
		mark := before
		for _, r := range slices.Backward(t.Runs) { // oldest first
			if r.At > before.Run {
				if !first {
					runs = append(runs, finished{t, r})
				}
				mark.Run = max(mark.Run, r.At)
			}
		}
		if t.Available && t.Notified != "" && t.Notified != before.Version {
			mark.Version = t.Notified
			// Ones that install themselves are announced when they have.
			if !t.Auto && !t.Paused {
				waiting = append(waiting, t)
			}
		}
		marks[t.ID] = mark
	}
	if first || !maps.Equal(marks, e.host.Updates) {
		e.host.Updates = marks
		if err := m.saveLocked(); err != nil {
			log.Warn("Could not keep what was announced about a server's updates", "server", e.host.Name, "err", err)
		}
	}
	notify := v.Notify && (m.opt.UpdatesNotify == nil || m.opt.UpdatesNotify())
	if len(waiting) > 0 && notify {
		title := fmt.Sprintf("%s: %d updates available", e.host.Name, len(waiting))
		if len(waiting) == 1 {
			title = e.host.Name + ": " + waiting[0].Name + " update available"
		}
		var fields []discord.Field
		for _, st := range waiting[:min(len(waiting), 20)] {
			fields = append(fields, discord.Field{Name: st.Name, Value: cmp.Or(updates.Change(st.Spec, st.Result), "update waiting"), Inline: true})
		}
		log.Info(title)
		m.sendFields(e, title, "Install from the dashboard's Updates page (pick "+e.host.Name+"), or turn on auto-update for them.", discord.Info, "Updates", fields)
	}
	for _, f := range runs {
		m.announceRunLocked(e, f.target, f.run)
	}
}

// announceRunLocked announces an update that ran on a server, as the dashboard announces its own.
func (m *Monitor) announceRunLocked(e *entry, t updates.Status, r updates.Run) {
	how := "From the dashboard"
	if r.Auto {
		how = "Automatically"
	}
	fields := []discord.Field{{Name: "Started", Value: how, Inline: true}}
	change := ""
	if r.From != "" && r.To != "" && r.From != r.To {
		change = r.From + " → " + r.To
		fields = append(fields, discord.Field{Name: "Change", Value: change, Inline: true})
	}
	name := e.host.Name + ": " + t.Name
	switch {
	case r.OK:
		title := strings.TrimSpace(name + " updated " + change)
		log.Info(title)
		m.sendFields(e, title, cmp.Or(r.Detail, "Installed and running."), discord.OK, "Updates", fields)
	case strings.Contains(r.Detail, "is not running after the update"):
		log.Error(name + " is down after its update")
		m.sendFields(e, name+" is down after its update", discord.Truncate(r.Detail, 1000), discord.Critical, "Updates", fields)
	default:
		log.Warn(name + " update failed")
		m.sendFields(e, name+" update failed", discord.Truncate(cmp.Or(r.Detail, "It did not say why; its log on the Updates page may."), 1000), discord.Warning, "Updates", fields)
	}
}
