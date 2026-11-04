package updates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
)

func TestVersions(t *testing.T) {
	for in, want := range map[string]string{
		"AdGuard Home, version v0.107.79":  "0.107.79",
		"Version: 1.71.0\nCommit: afcdb18": "1.71.0",
		`  "version": "3.2.0",`:            "3.2.0",
		"release-5.1.4":                    "5.1.4",
		"no version here":                  "",
	} {
		if got := updates.ExtractVersion(in); got != want {
			t.Errorf("ExtractVersion(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"v1.10.0", "1.9.9", 1}, {"4.0.15.2941", "4.0.15", 1}, {"2.4", "2.4.0", 0}, {"0.107.79", "v0.107.80", -1}} {
		if got := updates.Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestWindow(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 3, 10, h, 30, 0, 0, time.Local) }
	night := updates.Window{Start: 4, End: 6}
	if !night.Contains(at(4)) || !night.Contains(at(5)) || night.Contains(at(6)) || night.Contains(at(3)) {
		t.Fatal("4–6 should contain 04:30 and 05:30 only")
	}
	over := updates.Window{Start: 23, End: 2}
	if !over.Contains(at(23)) || !over.Contains(at(1)) || over.Contains(at(2)) || over.Contains(at(12)) {
		t.Fatal("23–2 runs over midnight")
	}
	if got := over.Opened(at(1)); got.Day() != 9 || got.Hour() != 23 {
		t.Fatalf("a window open at 01:30 opened the evening before, got %v", got)
	}
	if !(updates.Window{Start: 3, End: 3}).Contains(at(15)) {
		t.Fatal("equal start and end means any time")
	}
}

// fakeHost answers commands like a machine with one program at `version` whose update command
// installs `next`, and records what ran.
type fakeHost struct {
	mu      sync.Mutex
	version string
	next    string
	active  bool
	ran     []updates.Command
}

func (h *fakeHost) run(_ context.Context, c updates.Command) (updates.Output, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ran = append(h.ran, c)
	line := strings.Join(c.Argv, " ")
	switch {
	case strings.HasSuffix(line, "app --version"):
		return updates.Output{Stdout: "app " + h.version}, nil
	case strings.HasSuffix(line, "install-app"):
		h.version = h.next
		return updates.Output{}, nil
	case strings.HasPrefix(line, "systemctl is-active"):
		if h.active {
			return updates.Output{Stdout: "active\n"}, nil
		}
		return updates.Output{Stdout: "failed\n", Code: 3}, nil
	}
	return updates.Output{Code: 127, Stderr: "unexpected command: " + line}, nil
}

func (h *fakeHost) ranLine(want string) *updates.Command {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.ran {
		if strings.Contains(strings.Join(h.ran[i].Argv, " "), want) {
			return &h.ran[i]
		}
	}
	return nil
}

// github serves /repos/acme/app/releases/latest with tag.
func github(t *testing.T, tag string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/app/releases/latest" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type alert struct {
	Title string
	Sev   discord.Severity
}

type recorder struct {
	mu     sync.Mutex
	alerts []alert
}

func (r *recorder) SendEmbed(title, _ string, sev discord.Severity, _ []discord.Field, _ string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, alert{title, sev})
	return true
}

func (r *recorder) all() []alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]alert(nil), r.alerts...)
}

func newManager(t *testing.T, store *storage.Store, run updates.Runner, gh string, alerts discord.Sender) *updates.Manager {
	t.Helper()
	return updates.New(updates.Options{Store: store, Run: run, GitHubAPI: gh, Alerts: alerts, NotifyAvailable: true, Poll: time.Millisecond, Timeout: time.Minute}, nil, false)
}

func openStore(t *testing.T) *storage.Store {
	s := storage.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	t.Cleanup(s.Close)
	return s
}

var app = updates.Spec{ID: "app", Name: "App", Kind: updates.KindRelease, Repo: "acme/app", VersionCommand: "/opt/app --version", UpdateCommand: "install-app", Unit: "app"}

// settle waits until the target is idle and cond holds.
func settle(t *testing.T, m *updates.Manager, id string, cond func(updates.Status) bool) updates.Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, ok := m.Get(id)
		if ok && st.Busy == "" && cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never settled: %+v", id, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReleaseCheckAndManualUpdate(t *testing.T) {
	host := &fakeHost{version: "v1.2.0", next: "v1.3.0", active: true}
	rec := &recorder{}
	m := newManager(t, openStore(t), host.run, github(t, "v1.3.0").URL, rec)
	if _, err := m.Save("", app); err != nil {
		t.Fatal(err)
	}
	st := settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	if !st.Available || st.Installed != "1.2.0" || st.Latest != "v1.3.0" || st.Error != "" {
		t.Fatalf("check: %+v", st.State)
	}

	if err := m.Apply("app"); err != nil {
		t.Fatal(err)
	}
	st = settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	run := st.Runs[0]
	if !run.OK || run.Auto || run.From != "1.2.0" || run.To != "1.3.0" || st.Available {
		t.Fatalf("update: %+v / %+v", run, st.State)
	}
	if c := host.ranLine("install-app"); c == nil || !strings.Contains(strings.Join(c.Env, " "), "VERSION=1.3.0") {
		t.Fatalf("the update command sees the new version: %+v", c)
	}
	if host.ranLine("systemctl restart") != nil {
		t.Fatal("the unit is only restarted when Restart is set")
	}
	if got := rec.all(); len(got) != 2 || got[0].Title != "App update available" || got[1].Title != "App updated" || got[1].Sev != discord.OK {
		t.Fatalf("alerts: %+v", got)
	}
}

func TestAutoUpdateRunsOnceInTheWindow(t *testing.T) {
	host := &fakeHost{version: "1.0.0", next: "1.0.0", active: true} // the update "fails" to change the version
	rec := &recorder{}
	m := newManager(t, openStore(t), host.run, github(t, "v1.1.0").URL, rec)
	spec := app
	spec.Auto = true
	m.Save("", spec)
	settle(t, m, "app", func(s updates.Status) bool { return s.Available })

	closed := updates.Window{Start: 4, End: 6}
	noon := time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)
	m.Tick(noon, time.Hour, closed)
	time.Sleep(50 * time.Millisecond)
	if st, _ := m.Get("app"); len(st.Runs) != 0 {
		t.Fatal("nothing installs outside the window")
	}

	always := updates.Window{}
	m.Tick(time.Now(), time.Hour, always)
	st := settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if !st.Runs[0].Auto {
		t.Fatalf("run: %+v", st.Runs[0])
	}
	// The same version on offer again is not retried every cycle.
	for range 3 {
		m.Tick(time.Now(), time.Hour, always)
	}
	time.Sleep(50 * time.Millisecond)
	if st, _ := m.Get("app"); len(st.Runs) != 1 {
		t.Fatalf("auto-update retried the same version: %d runs", len(st.Runs))
	}
	for _, a := range rec.all() {
		if strings.Contains(a.Title, "available") {
			t.Fatal("automatic targets are not announced as waiting")
		}
	}
}

func TestServiceDownAfterUpdateIsCritical(t *testing.T) {
	host := &fakeHost{version: "1.0.0", next: "1.1.0", active: false}
	rec := &recorder{}
	m := newManager(t, openStore(t), host.run, github(t, "1.1.0").URL, rec)
	m.Save("", app)
	settle(t, m, "app", func(s updates.Status) bool { return s.Available })
	m.Apply("app")
	st := settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if st.Runs[0].OK || !strings.Contains(st.Runs[0].Detail, "not running") {
		t.Fatalf("run: %+v", st.Runs[0])
	}
	got := rec.all()
	if last := got[len(got)-1]; last.Sev != discord.Critical || last.Title != "App is down after its update" {
		t.Fatalf("alerts: %+v", got)
	}
}

func TestAvailableUpdatesAreAnnouncedOnce(t *testing.T) {
	host := &fakeHost{version: "1.0.0", active: true}
	rec := &recorder{}
	m := newManager(t, openStore(t), host.run, github(t, "1.1.0").URL, rec)
	m.Save("", app)
	settle(t, m, "app", func(s updates.Status) bool { return s.Available })
	for range 2 {
		m.CheckAll()
		time.Sleep(20 * time.Millisecond)
		settle(t, m, "app", func(updates.Status) bool { checking, _ := m.Activity(); return !checking })
	}
	n := 0
	for _, a := range rec.all() {
		if a.Title == "App update available" && a.Sev == discord.Info {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("announced %d times: %+v", n, rec.all())
	}
}

func TestStateSurvivesRestartAndSecretsStayHidden(t *testing.T) {
	store := openStore(t)
	host := &fakeHost{version: "1.0.0", active: true}
	gh := github(t, "1.0.0").URL
	detected := 0
	detect := func() []updates.Spec { detected++; return []updates.Spec{app} }
	m := updates.New(updates.Options{Store: store, Run: host.run, GitHubAPI: gh}, detect, false)
	m.CheckAll()
	settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })

	arr := updates.Spec{ID: "sonarr", Name: "Sonarr", Kind: updates.KindArr, URL: "http://127.0.0.1:1", APIKey: "secret-key", Paused: true}
	if _, err := m.Save("", arr); err != nil {
		t.Fatal(err)
	}
	arr.APIKey = "" // an empty key keeps the stored one
	arr.Name = "Sonarr 4"
	st, err := m.Save("sonarr", arr)
	if err != nil || st.APIKey != "" || !st.APIKeySet {
		t.Fatalf("save: %v %+v", err, st)
	}

	again := updates.New(updates.Options{Store: store, Run: host.run, GitHubAPI: gh}, detect, false)
	if detected != 1 {
		t.Fatalf("detection runs once per database, ran %d times", detected)
	}
	list := again.Statuses()
	if len(list) != 2 || list[0].Installed != "1.0.0" || list[0].Checked == 0 || list[1].Name != "Sonarr 4" || !list[1].APIKeySet {
		t.Fatalf("after restart: %+v", list)
	}
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), "secret-key") {
		t.Fatal("API keys never leave the server")
	}
}

func TestSpecValidation(t *testing.T) {
	for _, bad := range []string{
		`{"name":"x","kind":"nope"}`,
		`{"name":"x","kind":"apt","package":"-oAPT::Update::Pre-Invoke::=sh"}`,
		`{"name":"x","kind":"release","repo":"not a repo","version_command":"x"}`,
		`{"name":"x","kind":"git"}`,
		`{"name":"x","kind":"system","unit":"--now"}`,
		`{"name":"","kind":"system"}`,
	} {
		if _, err := updates.ParseSpec([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	s, err := updates.ParseSpec([]byte(`{"name":"Plex Media Server","kind":"apt","package":"plexmediaserver"}`))
	if err != nil || s.ID != "plex-media-server" {
		t.Fatalf("%v %+v", err, s)
	}
}

// ---------- Sonarr-style apps ----------

func TestArrUpdatesThroughItsAPI(t *testing.T) {
	var mu sync.Mutex
	version := "4.0.14.2939"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k3y" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /sonarr/api/v3/system/status":
			json.NewEncoder(w).Encode(map[string]string{"version": version})
		case "GET /sonarr/api/v3/update":
			json.NewEncoder(w).Encode([]map[string]any{
				{"version": "4.0.15.2941", "installed": version == "4.0.15.2941", "installable": true},
				{"version": "4.0.14.2939", "installed": version == "4.0.14.2939"},
			})
		case "POST /sonarr/api/v3/command":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] == "ApplicationUpdate" {
				version = "4.0.15.2941"
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	config := filepath.Join(t.TempDir(), "config.xml")
	os.WriteFile(config, []byte("<Config><Port>"+port+"</Port><UrlBase>/sonarr</UrlBase><ApiKey>k3y</ApiKey></Config>"), 0o600)

	host := &fakeHost{active: true}
	m := newManager(t, openStore(t), host.run, "", nil)
	m.Save("", updates.Spec{ID: "sonarr", Name: "Sonarr", Kind: updates.KindArr, ConfigFile: config, Unit: "sonarr"})
	st := settle(t, m, "sonarr", func(s updates.Status) bool { return s.Checked > 0 })
	if !st.Available || st.Installed != "4.0.14.2939" || st.Latest != "4.0.15.2941" {
		t.Fatalf("check: %+v", st.State)
	}
	m.Apply("sonarr")
	st = settle(t, m, "sonarr", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if r := st.Runs[0]; !r.OK || r.To != "4.0.15.2941" || st.Available {
		t.Fatalf("update: %+v", st)
	}
}

// ---------- git checkouts, with real git ----------

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitCheckoutPullsAndBuilds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	upstream, checkout := filepath.Join(root, "upstream"), filepath.Join(root, "checkout")
	os.Mkdir(upstream, 0o755)
	git(t, upstream, "init", "-q")
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "first")
	git(t, root, "clone", "-q", upstream, checkout)
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "fix the thing")

	m := newManager(t, openStore(t), updates.Exec, "", nil)
	m.Save("", updates.Spec{ID: "tool", Name: "Tool", Kind: updates.KindGit, Dir: checkout, UpdateCommand: "touch built"})
	st := settle(t, m, "tool", func(s updates.Status) bool { return s.Checked > 0 })
	if !st.Available || len(st.Pending) != 1 || !strings.HasSuffix(st.Pending[0], "fix the thing") || st.Error != "" {
		t.Fatalf("check: %+v", st.State)
	}
	m.Apply("tool")
	st = settle(t, m, "tool", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if !st.Runs[0].OK || st.Available {
		t.Fatalf("update: %+v", st)
	}
	if git(t, checkout, "rev-parse", "HEAD") != git(t, upstream, "rev-parse", "HEAD") {
		t.Fatal("the checkout was not pulled")
	}
	if _, err := os.Stat(filepath.Join(checkout, "built")); err != nil {
		t.Fatal("the build command did not run in the checkout")
	}
}

func TestSelfUpdateRestartsTheMonitor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	upstream, checkout := filepath.Join(root, "upstream"), filepath.Join(root, "checkout")
	os.Mkdir(upstream, 0o755)
	git(t, upstream, "init", "-q")
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "v1")
	git(t, root, "clone", "-q", upstream, checkout)
	git(t, upstream, "commit", "-q", "--allow-empty", "-m", "v2")

	restarted := make(chan struct{}, 1)
	m := updates.New(updates.Options{Store: openStore(t), Restart: func() { restarted <- struct{}{} }, Poll: time.Millisecond}, nil, false)
	m.Save("", updates.Spec{ID: "nightjar", Name: "Nightjar", Kind: updates.KindGit, Dir: checkout, Self: true, Auto: true})
	settle(t, m, "nightjar", func(s updates.Status) bool { return s.Available })
	// The first tick finds no check round yet and starts one; a later one installs.
	deadline := time.After(10 * time.Second)
	for {
		m.Tick(time.Now(), time.Hour, updates.Window{})
		select {
		case <-restarted:
			if st, _ := m.Get("nightjar"); git(t, checkout, "rev-parse", "HEAD") != git(t, upstream, "rev-parse", "HEAD") {
				t.Fatalf("restarted without pulling: %+v", st)
			}
			return
		case <-deadline:
			st, _ := m.Get("nightjar")
			t.Fatalf("no restart after the self-update: %+v", st)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func ExampleSlug() {
	fmt.Println(updates.Slug("Plex Media Server"), updates.Slug("AdGuard Home!"))
	// Output: plex-media-server adguard-home
}

func TestTheMonitorsOwnCheckoutIsKeptOnce(t *testing.T) {
	m := newManager(t, openStore(t), nil, "", nil)
	checkout := t.TempDir()
	// Kept under the monitor's old name, then detected again under its new one.
	if _, err := m.Save("", updates.Spec{ID: "servermonitor", Name: "Server Monitor", Kind: updates.KindGit, Dir: checkout, Self: true}); err != nil {
		t.Fatal(err)
	}
	if added := m.AddDetected([]updates.Spec{{ID: "nightjar", Name: "Nightjar", Kind: updates.KindGit, Dir: checkout, Self: true}}); len(added) != 0 {
		t.Fatalf("a second entry for the same checkout: %+v", added)
	}
}
