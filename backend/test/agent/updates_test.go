package agent_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/updates"
)

// aptMachine is a pretend server with one Debian package at 1.0, and 2.0 to install. While hold is
// set, installing waits for it to close.
type aptMachine struct {
	mu                   sync.Mutex
	installed, candidate string
	hold                 chan struct{}
	ran                  []string
}

func newAptMachine() *aptMachine { return &aptMachine{installed: "1.0", candidate: "2.0"} }

func (a *aptMachine) run(ctx context.Context, c updates.Command) (updates.Output, error) {
	line := strings.Join(c.Argv, " ")
	a.mu.Lock()
	a.ran = append(a.ran, line)
	inst, cand, hold := a.installed, a.candidate, a.hold
	a.mu.Unlock()
	switch {
	case strings.HasPrefix(line, "apt-get update"):
		return updates.Output{}, nil
	case strings.HasPrefix(line, "dpkg-query"):
		return updates.Output{Stdout: inst}, nil
	case strings.HasPrefix(line, "apt-cache policy"):
		return updates.Output{Stdout: "  Installed: " + inst + "\n  Candidate: " + cand + "\n"}, nil
	case strings.HasPrefix(line, "dpkg --compare-versions"):
		if c.Argv[2] > c.Argv[4] {
			return updates.Output{}, nil
		}
		return updates.Output{Code: 1}, nil
	case strings.HasPrefix(line, "apt-get install"):
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return updates.Output{}, ctx.Err()
			}
		}
		a.mu.Lock()
		a.installed = a.candidate
		a.mu.Unlock()
		if c.Output != nil {
			c.Output.Write([]byte("Unpacking plexmediaserver (" + cand + ")\n"))
		}
		return updates.Output{}, nil
	}
	return updates.Output{Code: 127, Stderr: "unexpected command: " + line}, nil
}

func (a *aptMachine) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.ran)
}

var plex = updates.Spec{ID: "plex", Name: "Plex", Kind: updates.KindApt, Package: "plexmediaserver"}

// agentUpdates is an agent's update manager on a pretend machine, its targets in a file in dir.
func agentUpdates(t *testing.T, dir string, m *aptMachine) *agent.Updates {
	t.Helper()
	store, err := updates.OpenFileStore(filepath.Join(dir, "updates.json"))
	if err != nil {
		t.Fatal(err)
	}
	self := updates.Spec{ID: "nightjar", Name: "Nightjar", Kind: updates.KindGit, Dir: "/opt/nightjar", Self: true}
	u := agent.NewUpdates(store, updates.Options{Run: m.run, Poll: time.Millisecond, GitHubAPI: "http://127.0.0.1:1"}, func() []updates.Spec { return []updates.Spec{plex, self} })
	t.Cleanup(func() { u.Manager.Wait(5 * time.Second) })
	return u
}

func settleAgent(t *testing.T, u *agent.Updates, id string, cond func(updates.Status) bool) updates.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, ok := u.Manager.Get(id)
		if ok && st.Busy == "" && cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never settled: %+v", id, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type answer struct {
	code int
	body map[string]any
}

func callAgent(t *testing.T, h http.Handler, token, method, path, body string) answer {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	a := answer{code: rec.Code}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &a.body); err != nil {
			t.Fatalf("%s %s answered %d, not JSON: %q", method, path, rec.Code, rec.Body.String())
		}
	}
	return a
}

func TestTheDashboardManagesTheAgentsUpdates(t *testing.T) {
	dir := t.TempDir()
	machine := newAptMachine()
	u := agentUpdates(t, dir, machine)
	s := agent.NewSampler("test")
	s.UseUpdates(u)
	h := agent.Handler(s, tok, nil, true)

	if a := callAgent(t, h, "", http.MethodGet, agent.UpdatesPath, ""); a.code != http.StatusUnauthorized || a.body["error"] != "unauthorized" {
		t.Fatalf("without the token: %+v", a)
	}
	a := callAgent(t, h, tok, http.MethodGet, agent.UpdatesPath, "")
	targets, _ := a.body["targets"].([]any)
	window, _ := a.body["window"].(map[string]any)
	zone, _ := a.body["time_zone"].(map[string]any)
	if a.code != http.StatusOK || a.body["enabled"] != true || a.body["interval"] != 21600.0 || a.body["notify"] != true || a.body["commands_editable"] != true ||
		window["start"] != 240.0 || window["end"] != 360.0 || window["label"] != "04:00–06:00" || zone["name"] == "" || len(targets) != 1 {
		t.Fatalf("the defaults, and what was found without the agent itself: %+v", a)
	}
	if targets[0].(map[string]any)["id"] != "plex" {
		t.Fatalf("targets: %v", targets)
	}

	// Checking, updating and the rest, with the answers the dashboard's own routes give.
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath+"/check", "{}"); a.code != http.StatusAccepted || a.body["queued"] != true {
		t.Fatalf("check: %+v", a)
	}
	settleAgent(t, u, "plex", func(st updates.Status) bool { return st.Available })
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath+"/plex/update", "{}"); a.code != http.StatusAccepted {
		t.Fatalf("update: %+v", a)
	}
	st := settleAgent(t, u, "plex", func(st updates.Status) bool { return len(st.Runs) == 1 })
	if !st.Runs[0].OK || st.Runs[0].From != "1.0" || st.Runs[0].To != "2.0" || st.Available {
		t.Fatalf("updated: %+v", st)
	}
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath+"/nope/check", "{}"); a.code != http.StatusNotFound || a.body["error"] != updates.ErrNotFound.Error() {
		t.Fatalf("no such target: %+v", a)
	}
	// The agent is never a target of its own: the dashboard updates it.
	a = callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath, `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v","self":true}`)
	if a.code != http.StatusCreated || a.body["target"].(map[string]any)["self"] != nil {
		t.Fatalf("add: %+v", a)
	}
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath, `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v"}`); a.code != http.StatusConflict {
		t.Fatalf("added twice: %+v", a)
	}
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath, `{"name":"Bad","kind":"apt"}`); a.code != http.StatusBadRequest || a.body["error"] == nil {
		t.Fatalf("a bad target: %+v", a)
	}
	a = callAgent(t, h, tok, http.MethodPut, agent.UpdatesPath+"/tool", `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v","auto":true}`)
	if a.code != http.StatusOK || a.body["target"].(map[string]any)["auto"] != true {
		t.Fatalf("save: %+v", a)
	}
	if a := callAgent(t, h, tok, http.MethodDelete, agent.UpdatesPath+"/tool", ""); a.code != http.StatusNoContent {
		t.Fatalf("delete: %+v", a)
	}
	if a := callAgent(t, h, tok, http.MethodPost, agent.UpdatesPath+"/detect", "{}"); a.code != http.StatusOK || len(a.body["added"].([]any)) != 0 {
		t.Fatalf("detect finds nothing new: %+v", a)
	}

	// Settings: checked, kept, and answered with the whole view.
	for _, bad := range []string{`{"interval": 100}`, `{"interval": 3600.5}`, `{"window": {"start": 1440}}`, `{"window": {"end": -1}}`, `not json`} {
		if a := callAgent(t, h, tok, http.MethodPut, agent.UpdatesPath+"/settings", bad); a.code != http.StatusBadRequest || a.body["error"] == nil {
			t.Fatalf("settings %s: %+v", bad, a)
		}
	}
	a = callAgent(t, h, tok, http.MethodPut, agent.UpdatesPath+"/settings", `{"enabled": false, "interval": 3600, "window": {"start": 90, "end": 150}, "notify": false}`)
	window, _ = a.body["window"].(map[string]any)
	if a.code != http.StatusOK || a.body["enabled"] != false || a.body["interval"] != 3600.0 || a.body["notify"] != false || window["label"] != "01:30–02:30" || a.body["targets"] == nil {
		t.Fatalf("settings: %+v", a)
	}
	// Left out, a setting stays as it was.
	if a := callAgent(t, h, tok, http.MethodPut, agent.UpdatesPath+"/settings", `{"notify": true}`); a.body["interval"] != 3600.0 || a.body["notify"] != true {
		t.Fatalf("partial settings: %+v", a)
	}
	u.Manager.Wait(5 * time.Second)
	again := agentUpdates(t, dir, newAptMachine())
	if st := again.Settings(); st.Enabled || st.Interval != 3600 || st.Window.Start != 90 || st.Window.End != 150 || !st.Notify {
		t.Fatalf("settings after a restart: %+v", st)
	}
}

func TestTheAgentsUpdatesChangeOnlyOverHTTPS(t *testing.T) {
	s := agent.NewSampler("test")
	s.UseUpdates(agentUpdates(t, t.TempDir(), newAptMachine()))
	plain := agent.Handler(s, tok, nil, false)
	if a := callAgent(t, plain, tok, http.MethodGet, agent.UpdatesPath, ""); a.code != http.StatusOK {
		t.Fatalf("reading over plain HTTP: %+v", a)
	}
	for _, c := range [][2]string{{http.MethodPost, agent.UpdatesPath + "/check"}, {http.MethodPost, agent.UpdatesPath + "/plex/update"},
		{http.MethodPut, agent.UpdatesPath + "/settings"}, {http.MethodDelete, agent.UpdatesPath + "/plex"}, {http.MethodPost, agent.UpdatesPath + "/detect"}} {
		a := callAgent(t, plain, tok, c[0], c[1], "{}")
		if msg, _ := a.body["error"].(string); a.code != http.StatusForbidden || !strings.Contains(msg, "plain HTTP") {
			t.Fatalf("%s %s over plain HTTP: %+v", c[0], c[1], a)
		}
	}

	// An agent without an update manager says so, in JSON.
	bare := agent.NewSampler("test")
	if a := callAgent(t, agent.Handler(bare, tok, nil, true), tok, http.MethodGet, agent.UpdatesPath, ""); a.code != http.StatusNotFound || a.body["error"] != "this agent does not manage updates" {
		t.Fatalf("no manager: %+v", a)
	}
}

// The agent checks and installs on its own schedule, as the settings say.
func TestTheAgentTicksItsUpdatesAsTheSettingsSay(t *testing.T) {
	machine := newAptMachine()
	u := agentUpdates(t, t.TempDir(), machine)
	st := agent.DefaultUpdateSettings()
	st.Enabled = false
	if err := u.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	u.Tick(time.Now())
	time.Sleep(50 * time.Millisecond)
	if _, last := u.Manager.Activity(); last != 0 || machine.count() != 0 {
		t.Fatalf("turned off, it checks nothing: %v %d", last, machine.count())
	}
	st.Enabled = true
	u.SaveSettings(st)
	u.Tick(time.Now())
	deadline := time.Now().Add(5 * time.Second)
	for _, last := u.Manager.Activity(); last == 0; _, last = u.Manager.Activity() {
		if time.Now().After(deadline) {
			t.Fatal("a check round never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !settleAgent(t, u, "plex", func(updates.Status) bool { return true }).Available {
		t.Fatal("the check found the update")
	}
	// Auto-update in a window open now installs it.
	auto := plex
	auto.Auto = true
	if _, err := u.Manager.Save("plex", auto); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	minute := now.Hour()*60 + now.Minute()
	// Opened a minute ago, so the check just made counts as this window's.
	st.Window = agent.UpdateWindow{Start: (minute + 24*60 - 1) % (24 * 60), End: (minute + 2) % (24 * 60)}
	u.SaveSettings(st)
	u.Tick(now)
	if got := settleAgent(t, u, "plex", func(s updates.Status) bool { return len(s.Runs) == 1 }); !got.Runs[0].Auto || !got.Runs[0].OK {
		t.Fatalf("auto-update: %+v", got.Runs)
	}
}

// While it updates something on its server, the agent does not replace itself: the restart would
// cut the update off. The dashboard tries again later.
func TestTheAgentRefusesANewBuildWhileUpdating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightjar-agent")
	os.WriteFile(path, []byte("old build"), 0o755)
	restarted := make(chan struct{}, 1)
	self := &agent.Self{Path: path, Hash: hashOf([]byte("old build")), Restart: func() { restarted <- struct{}{} }}

	machine := newAptMachine()
	machine.hold = make(chan struct{})
	u := agentUpdates(t, t.TempDir(), machine)
	s := agent.NewSampler("test")
	s.UseUpdates(u)
	h := agent.Handler(s, tok, self, true)
	if err := u.Manager.Apply("plex"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for u.Busy() == "" {
		if time.Now().After(deadline) {
			t.Fatal("the update never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	build := newBuild(t)
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, agent.UpdatePath, strings.NewReader(string(build)))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(agent.BuildHeader, hashOf(build))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "busy updating Plex; try again when it is done") {
		t.Fatalf("while updating: %d %q", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "old build" {
		t.Fatal("the build was replaced while updating")
	}
	close(machine.hold)
	settleAgent(t, u, "plex", func(st updates.Status) bool { return len(st.Runs) == 1 })
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("after the update: %d %q", rec.Code, rec.Body.String())
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("never restarted on the new build")
	}
}
