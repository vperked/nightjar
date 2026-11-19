package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/remote"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
)

// pretendApt answers like a server with one Debian package at 1.0 and 2.0 to install.
type pretendApt struct {
	mu        sync.Mutex
	installed string
}

func (p *pretendApt) run(_ context.Context, c updates.Command) (updates.Output, error) {
	line := strings.Join(c.Argv, " ")
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasPrefix(line, "apt-get update"):
		return updates.Output{}, nil
	case strings.HasPrefix(line, "dpkg-query"):
		return updates.Output{Stdout: p.installed}, nil
	case strings.HasPrefix(line, "apt-cache policy"):
		return updates.Output{Stdout: "  Candidate: 2.0\n"}, nil
	case strings.HasPrefix(line, "dpkg --compare-versions"):
		if c.Argv[2] > c.Argv[4] {
			return updates.Output{}, nil
		}
		return updates.Output{Code: 1}, nil
	case strings.HasPrefix(line, "apt-get install"):
		p.installed = "2.0"
		return updates.Output{}, nil
	}
	return updates.Output{Code: 127, Stderr: "unexpected command: " + line}, nil
}

// hostUpdatesServer is the API with one server, whose agent keeps a pretend machine updated.
func hostUpdatesServer(t *testing.T, token string) (http.Handler, string, *agent.Updates) {
	t.Helper()
	db := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	t.Cleanup(db.Close)
	const agentToken = "0123456789abcdef0123456789abcdef"
	store, err := updates.OpenFileStore(filepath.Join(t.TempDir(), "updates.json"))
	if err != nil {
		t.Fatal(err)
	}
	machine := &pretendApt{installed: "1.0"}
	u := agent.NewUpdates(store, updates.Options{Run: machine.run, Poll: time.Millisecond, GitHubAPI: "http://127.0.0.1:1"}, func() []updates.Spec {
		return []updates.Spec{{ID: "plex", Name: "Plex", Kind: updates.KindApt, Package: "plexmediaserver"}}
	})
	t.Cleanup(func() { u.Manager.Wait(5 * time.Second) })
	s0 := agent.NewSampler("test")
	s0.Sample(context.Background())
	s0.UseUpdates(u)
	srv := httptest.NewServer(agent.Handler(s0, agentToken, nil, true))
	t.Cleanup(srv.Close)
	rm := remote.New(remote.Options{Store: db})
	host, err := rm.Add(context.Background(), "box", srv.URL, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Remote: rm}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "8080", AuthToken: token, Store: db, FrontendDir: t.TempDir()}, ctx)
	return s.Handler(), host.ID, u
}

func waitForPlex(t *testing.T, u *agent.Updates, cond func(updates.Status) bool) {
	t.Helper()
	for range 1000 {
		if st, _ := u.Manager.Get("plex"); st.Busy == "" && cond(st) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("plex never settled")
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestServersUpdatesAreManagedFromTheDashboard(t *testing.T) {
	h, id, u := hostUpdatesServer(t, "tok")
	base := "/api/hosts/" + id + "/updates"

	code, body := call(t, h, http.MethodGet, "/api/hosts/updates", "")
	hosts, _ := body["hosts"].([]any)
	if code != http.StatusOK || len(hosts) != 1 {
		t.Fatalf("summary: %d %v", code, body)
	}
	if got := keys(hosts[0].(map[string]any)); !slices.Equal(got, []string{"applying", "available", "checked", "error", "id", "name"}) {
		t.Fatalf("summary fields: %v", got)
	}

	// The same answer as this machine's own, plus notify and the server's clock.
	code, body = call(t, h, http.MethodGet, base, "")
	if code != http.StatusOK {
		t.Fatalf("updates: %d %v", code, body)
	}
	if got := keys(body); !slices.Equal(got, []string{"checking", "commands_editable", "enabled", "interval", "kinds", "last_check", "notify", "targets", "time_zone", "window"}) {
		t.Fatalf("fields: %v", got)
	}
	window := body["window"].(map[string]any)
	if body["commands_editable"] != true || window["start"] != 240.0 || window["label"] != "04:00–06:00" || len(body["targets"].([]any)) != 1 {
		t.Fatalf("updates: %v", body)
	}

	if code, body := call(t, h, http.MethodPost, base+"/check", "{}"); code != http.StatusAccepted || body["queued"] != true {
		t.Fatalf("check: %d %v", code, body)
	}
	waitForPlex(t, u, func(st updates.Status) bool { return st.Available })
	if code, body := call(t, h, http.MethodPost, base+"/plex/check", "{}"); code != http.StatusAccepted || body["queued"] != true {
		t.Fatalf("check one: %d %v", code, body)
	}
	waitForPlex(t, u, func(st updates.Status) bool { return st.Available })
	if code, body := call(t, h, http.MethodPost, base+"/plex/update", "{}"); code != http.StatusAccepted || body["queued"] != true {
		t.Fatalf("update: %d %v", code, body)
	}
	waitForPlex(t, u, func(st updates.Status) bool { return len(st.Runs) == 1 })
	_, body = call(t, h, http.MethodGet, base, "")
	plex := body["targets"].([]any)[0].(map[string]any)
	if runs := plex["runs"].([]any); len(runs) != 1 || runs[0].(map[string]any)["to"] != "2.0" {
		t.Fatalf("after the update: %v", plex)
	}

	code, body = call(t, h, http.MethodPost, base, `{"name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v"}`)
	if code != http.StatusCreated || body["target"].(map[string]any)["id"] != "tool" {
		t.Fatalf("add: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPost, base, `{"name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v"}`); code != http.StatusConflict || body["error"] != updates.ErrExists.Error() {
		t.Fatalf("added twice: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPost, base, `{"name":"Tool","kind":"release","repo":"not a repo"}`); code != http.StatusBadRequest || body["error"] == nil {
		t.Fatalf("a bad target: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPut, base+"/tool", `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v","auto":true}`); code != http.StatusOK || body["target"].(map[string]any)["auto"] != true {
		t.Fatalf("save: %d %v", code, body)
	}
	if code, _ := call(t, h, http.MethodDelete, base+"/tool", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, body := call(t, h, http.MethodPost, base+"/tool/update", "{}"); code != http.StatusNotFound || body["error"] != updates.ErrNotFound.Error() {
		t.Fatalf("a deleted target: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPost, base+"/detect", "{}"); code != http.StatusOK || body["added"] == nil {
		t.Fatalf("detect: %d %v", code, body)
	}

	code, body = call(t, h, http.MethodPut, base+"/settings", `{"enabled": true, "interval": 7200, "window": {"start": 90, "end": 150}, "notify": false}`)
	if code != http.StatusOK || body["interval"] != 7200.0 || body["notify"] != false || body["window"].(map[string]any)["label"] != "01:30–02:30" || body["commands_editable"] != true || body["targets"] == nil {
		t.Fatalf("settings: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPut, base+"/settings", `{"window": {"start": 2000}}`); code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "minutes after midnight") {
		t.Fatalf("bad settings: %d %v", code, body)
	}
	if code, _ := call(t, h, http.MethodGet, "/api/hosts/nope/updates", ""); code != http.StatusNotFound {
		t.Fatalf("no such server: %d", code)
	}
	_, body = call(t, h, http.MethodGet, "/api/hosts/updates", "")
	if s := body["hosts"].([]any)[0].(map[string]any); s["id"] != id || s["name"] != "box" || s["error"] != "" || s["checked"].(float64) == 0 {
		t.Fatalf("summary after a poll: %v", s)
	}
}

// Without sign-in, a server's update commands are as locked as this machine's.
func TestServersUpdateCommandsNeedSignIn(t *testing.T) {
	h, id, _ := hostUpdatesServer(t, "")
	base := "/api/hosts/" + id + "/updates"
	if _, body := call(t, h, http.MethodGet, base, ""); body["commands_editable"] != false {
		t.Fatalf("commands_editable: %v", body)
	}
	code, body := call(t, h, http.MethodPost, base, `{"name":"Evil","kind":"release","repo":"a/b","version_command":"curl x | sh"}`)
	if code != http.StatusForbidden || !strings.Contains(body["error"].(string), "WEB_AUTH_TOKEN") {
		t.Fatalf("a command without sign-in: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPut, base+"/plex", `{"id":"plex","name":"Plex","kind":"apt","package":"plexmediaserver","auto":true}`); code != http.StatusOK {
		t.Fatalf("auto-update without changing commands: %d %v", code, body)
	}
	if code, _ := call(t, h, http.MethodPut, base+"/plex", `{"id":"plex","name":"Plex","kind":"apt","package":"plexmediaserver","config_file":"/etc/shadow"}`); code != http.StatusForbidden {
		t.Fatalf("changing a path without sign-in: %d", code)
	}
}
