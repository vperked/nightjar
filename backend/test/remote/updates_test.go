package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/collect"
	"nightjar/internal/discord"
	"nightjar/internal/remote"
	"nightjar/internal/updates"
)

// aptMachine is a pretend server with one Debian package at 1.0 and a newer one to install; with
// fail set, installing fails.
type aptMachine struct {
	mu                   sync.Mutex
	installed, candidate string
	fail                 bool
}

func (a *aptMachine) set(f func(*aptMachine)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

func (a *aptMachine) run(_ context.Context, c updates.Command) (updates.Output, error) {
	line := strings.Join(c.Argv, " ")
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case strings.HasPrefix(line, "apt-get update"):
		return updates.Output{}, nil
	case strings.HasPrefix(line, "dpkg-query"):
		return updates.Output{Stdout: a.installed}, nil
	case strings.HasPrefix(line, "apt-cache policy"):
		return updates.Output{Stdout: "  Candidate: " + a.candidate + "\n"}, nil
	case strings.HasPrefix(line, "dpkg --compare-versions"):
		if c.Argv[2] > c.Argv[4] {
			return updates.Output{}, nil
		}
		return updates.Output{Code: 1}, nil
	case strings.HasPrefix(line, "apt-get install"):
		if a.fail {
			return updates.Output{Code: 100, Stderr: "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, nil
		}
		a.installed = a.candidate
		return updates.Output{}, nil
	}
	return updates.Output{Code: 127, Stderr: "unexpected command: " + line}, nil
}

// updatesAgent is a real agent with updates, on a pretend machine.
func updatesAgent(t *testing.T) (*httptest.Server, *agent.Updates, *aptMachine) {
	t.Helper()
	machine := &aptMachine{installed: "1.0", candidate: "2.0"}
	store, err := updates.OpenFileStore(t.TempDir() + "/updates.json")
	if err != nil {
		t.Fatal(err)
	}
	plex := updates.Spec{ID: "plex", Name: "Plex", Kind: updates.KindApt, Package: "plexmediaserver"}
	u := agent.NewUpdates(store, updates.Options{Run: machine.run, Poll: time.Millisecond, GitHubAPI: "http://127.0.0.1:1"}, func() []updates.Spec { return []updates.Spec{plex} })
	t.Cleanup(func() { u.Manager.Wait(5 * time.Second) })
	s := agent.NewSampler("test")
	s.Sample(context.Background())
	s.UseUpdates(u)
	srv := httptest.NewServer(agent.Handler(s, token, nil, true))
	t.Cleanup(srv.Close)
	return srv, u, machine
}

func settled(t *testing.T, u *agent.Updates, cond func(updates.Status) bool) {
	t.Helper()
	for range 1000 {
		if st, _ := u.Manager.Get("plex"); st.Busy == "" && cond(st) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, _ := u.Manager.Get("plex")
	t.Fatalf("never settled: %+v", st)
}

// aboutAlerts records alerts with the subject they are about, which is what a server's mute holds.
type aboutAlerts struct {
	alerts
	mu       sync.Mutex
	subjects []string
}

type aboutSender struct {
	to      *aboutAlerts
	subject string
}

func (a *aboutAlerts) About(subject string) discord.Sender { return aboutSender{a, subject} }

func (s aboutSender) SendEmbed(title, desc string, sev discord.Severity, f []discord.Field, cat string) bool {
	s.to.mu.Lock()
	s.to.subjects = append(s.to.subjects, s.subject+" "+cat)
	s.to.mu.Unlock()
	return s.to.alerts.SendEmbed(title, desc, sev, f, cat)
}

func TestAnAgentsUpdatesAreManagedThroughItAndAnnouncedOnce(t *testing.T) {
	ctx := context.Background()
	srv, u, machine := updatesAgent(t)
	db := openDB(t)
	sent := &aboutAlerts{}
	m := remote.New(remote.Options{Store: db, Alerts: sent})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.HostUpdates(ctx, h.ID)
	if err != nil || len(v.Targets) != 1 || v.Targets[0].ID != "plex" || v.Window.Start != 240 || !v.Notify || v.TimeZone.Name == "" {
		t.Fatalf("updates: %+v %v", v, err)
	}
	if err := m.CheckHostUpdates(ctx, h.ID, ""); err != nil {
		t.Fatal(err)
	}
	settled(t, u, func(st updates.Status) bool { return st.Available })
	m.PollUpdates(ctx)
	if got := sent.after(1); !slices.Equal(got, []string{"info: box: Plex update available"}) {
		t.Fatalf("waiting update: %v", got)
	}
	if s := m.UpdatesSummary(); len(s) != 1 || s[0].ID != h.ID || s[0].Name != "box" || s[0].Available != 1 || s[0].Error != "" {
		t.Fatalf("summary: %+v", s)
	}
	// Once only, also after a restart.
	m.PollUpdates(ctx)
	m = remote.New(remote.Options{Store: db, Alerts: sent})
	m.PollUpdates(ctx)
	if got := sent.after(2); len(got) != 1 {
		t.Fatalf("announced again: %v", got)
	}

	// Its run is announced when it ends, once, named by server; a failed one too.
	if err := m.ApplyHostUpdate(ctx, h.ID, "plex"); err != nil {
		t.Fatal(err)
	}
	settled(t, u, func(st updates.Status) bool { return len(st.Runs) == 1 })
	m.PollUpdates(ctx)
	m.PollUpdates(ctx)
	if got := sent.after(3); len(got) != 2 || got[1] != "ok: box: Plex updated 1.0 → 2.0" {
		t.Fatalf("run: %v", got)
	}
	machine.set(func(a *aptMachine) { a.candidate, a.fail = "3.0", true })
	if err := m.ApplyHostUpdate(ctx, h.ID, "plex"); err != nil {
		t.Fatal(err)
	}
	settled(t, u, func(st updates.Status) bool { return len(st.Runs) == 2 })
	m.PollUpdates(ctx)
	if got := sent.after(3); len(got) != 3 || got[2] != "warning: box: Plex update failed" {
		t.Fatalf("failed run: %v", got)
	}
	sent.mu.Lock()
	for _, s := range sent.subjects {
		if s != "host:"+h.ID+" Updates" {
			t.Errorf("an alert not about the server, which its mute would miss: %q", s)
		}
	}
	sent.mu.Unlock()

	// The rest of the routes, through the agent.
	st, err := m.AddHostUpdate(ctx, h.ID, updates.Spec{ID: "tool", Name: "Tool", Kind: updates.KindApt, Package: "tool"})
	if err != nil || st.ID != "tool" {
		t.Fatalf("add: %+v %v", st, err)
	}
	var ae *remote.AgentError
	if _, err := m.AddHostUpdate(ctx, h.ID, updates.Spec{ID: "tool", Name: "Tool", Kind: updates.KindApt, Package: "tool"}); !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("added twice: %v", err)
	}
	if st, err := m.SaveHostUpdate(ctx, h.ID, "tool", updates.Spec{ID: "tool", Name: "Tool", Kind: updates.KindApt, Package: "tool", Paused: true}); err != nil || !st.Paused {
		t.Fatalf("save: %+v %v", st, err)
	}
	if err := m.DeleteHostUpdate(ctx, h.ID, "tool"); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckHostUpdates(ctx, h.ID, "tool"); !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Msg != updates.ErrNotFound.Error() {
		t.Fatalf("a deleted target: %v", err)
	}
	if added, err := m.DetectHostUpdates(ctx, h.ID); err != nil || len(added) != 0 {
		t.Fatalf("detect: %v %v", added, err)
	}
	off, interval := false, 3600.0
	v, err = m.SetHostUpdateSettings(ctx, h.ID, agent.UpdateSettingsChange{Notify: &off, Interval: &interval})
	if err != nil || v.Notify || v.Interval != 3600 || !v.Enabled {
		t.Fatalf("settings: %+v %v", v, err)
	}
	if _, err := m.HostUpdates(ctx, "nope"); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("no such server: %v", err)
	}
}

// Updates waiting are announced only when both this dashboard and the server say so; runs always are.
func TestWaitingUpdatesFollowBothNotifySettings(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name          string
		main, onAgent bool
	}{{"UPDATES_NOTIFY off", false, true}, {"the agent's notify off", true, false}} {
		srv, u, _ := updatesAgent(t)
		sent := &alerts{}
		m := remote.New(remote.Options{Store: openDB(t), Alerts: sent, UpdatesNotify: func() bool { return c.main }})
		h, err := m.Add(ctx, "box", srv.URL, token)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.SetHostUpdateSettings(ctx, h.ID, agent.UpdateSettingsChange{Notify: &c.onAgent}); err != nil {
			t.Fatal(err)
		}
		u.Manager.Check("plex")
		settled(t, u, func(st updates.Status) bool { return st.Available })
		m.PollUpdates(ctx)
		if got := sent.after(0); len(got) != 0 {
			t.Fatalf("%s: announced %v", c.name, got)
		}
		u.Manager.Apply("plex")
		settled(t, u, func(st updates.Status) bool { return len(st.Runs) == 1 })
		m.PollUpdates(ctx)
		if got := sent.after(1); !slices.Equal(got, []string{"ok: box: Plex updated 1.0 → 2.0"}) {
			t.Fatalf("%s: run: %v", c.name, got)
		}
	}
}

// An agent from before updates answers 404: the dashboard says to update it.
func TestAnAgentTooOldForUpdatesSaysToUpdateIt(t *testing.T) {
	ctx := context.Background()
	s := agent.NewSampler("test")
	s.Sample(ctx)
	readings := agent.Handler(s, token, nil, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agent.Path {
			readings.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	var ae *remote.AgentError
	if _, err := m.HostUpdates(ctx, h.ID); !errors.As(err, &ae) || ae.Status != http.StatusNotFound || ae.Msg != remote.UpdatesTooOld {
		t.Fatalf("an old agent: %v", err)
	}
	m.PollUpdates(ctx)
	if s := m.UpdatesSummary(); len(s) != 1 || s[0].Error != "update the agent" || s[0].Available != 0 {
		t.Fatalf("summary: %+v", s)
	}
}

// An agent busy updating its server turns its own update down; it is offered again in minutes.
func TestABusyAgentIsOfferedItsBuildAgainSoon(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == agent.UpdatePath {
			posts++
			http.Error(w, "busy updating Plex; try again when it is done", http.StatusConflict)
			return
		}
		json.NewEncoder(w).Encode(agent.Readings{Version: agent.Version, Agent: "test", Time: 1, Build: "old", CanUpdate: true,
			System: collect.System{Hostname: "nas", Architecture: "arm64"}, CPU: agent.CPU{Percent: 1, Count: 1}})
	}))
	defer srv.Close()
	var clock sync.Mutex
	now := time.Now()
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{},
		Now:        func() time.Time { clock.Lock(); defer clock.Unlock(); return now },
		AgentBuild: func(string) ([]byte, string, error) { return []byte("NEW"), "new", nil }})
	ctx := context.Background()
	h, err := m.Add(ctx, "nas", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	var st remote.State
	for range 200 {
		if st, _ = m.Get(h.ID); st.Update != nil && st.Update.State == "updating" && st.Update.Detail != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.Update == nil || !strings.Contains(st.Update.Detail, "Plex") {
		t.Fatalf("busy: %+v", st.Update)
	}
	clock.Lock()
	now = now.Add(5 * time.Minute)
	clock.Unlock()
	m.PollAll(ctx)
	for range 200 {
		mu.Lock()
		n := posts
		mu.Unlock()
		if n == 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("offered %d times", posts)
}

// The dashboard waits for an agent's detection as long as the agent lets it take: with less, a
// slow one reads as an agent that does not answer while it goes on.
func TestTheDashboardWaitsAsLongAsDetectionMayTake(t *testing.T) {
	if remote.DetectWait <= agent.DetectWait {
		t.Fatalf("the dashboard waits %s for a detection the agent allows %s", remote.DetectWait, agent.DetectWait)
	}
}
