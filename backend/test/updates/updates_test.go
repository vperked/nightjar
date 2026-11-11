package updates_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
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
		if c.Output != nil {
			// What the update prints reaches the dashboard as it goes, a progress bar as its last state.
			c.Output.Write([]byte("Downloading app " + h.next + "\nprogress 10%\rprogress 100%\nInstalled\n"))
		}
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
	// The run keeps what it did: each command, what it printed, and the steps between.
	logText := strings.Join(run.Log, "\n")
	for _, want := range []string{"Updating App: 1.2.0 → v1.3.0", "$ sh -c install-app", "Downloading app v1.3.0", "progress 100%", "Installed", "Checking it is running", "Done"} {
		if !strings.Contains(logText, want) {
			t.Errorf("the run's log misses %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "progress 10%") {
		t.Errorf("a progress bar keeps only its last state:\n%s", logText)
	}
	if st.Live != nil {
		t.Error("an update that is over is not live")
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
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["name"] == "ApplicationUpdate" {
				version = "4.0.15.2941"
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "status": "queued"})
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

// A major version is installed when you ask for it, as the app's own Install button does; an
// automatic update leaves it to you; an app that will not update says so in a minute, not fifteen.
func TestArrMajorUpdates(t *testing.T) {
	var mu sync.Mutex
	version, latest := "2.3.7.5365", "3.0.0.5737"
	var asked []map[string]any
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/system/status":
			json.NewEncoder(w).Encode(map[string]string{"version": version})
		case "GET /api/v1/update":
			json.NewEncoder(w).Encode([]map[string]any{{"version": latest, "installed": version == latest, "installable": true}, {"version": version, "installed": true}})
		case "POST /api/v1/command":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			asked = append(asked, body)
			if body["installMajorUpdate"] == true && !refuse {
				version = latest
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 9})
		case "GET /api/v1/command/9":
			// Prowlarr finishes the command, having logged that it will not install a major update.
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "status": "completed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	config := filepath.Join(t.TempDir(), "config.xml")
	os.WriteFile(config, []byte("<Config><Port>"+port+"</Port><ApiKey>k3y</ApiKey></Config>"), 0o600)
	host := &fakeHost{active: true}
	m := newManager(t, openStore(t), host.run, "", nil)
	m.Save("", updates.Spec{ID: "prowlarr", Name: "Prowlarr", Kind: updates.KindArr, ConfigFile: config, Unit: "prowlarr"})
	settle(t, m, "prowlarr", func(s updates.Status) bool { return s.Checked > 0 && s.Available })

	// An app that finishes its command without updating is reported soon, with what to read.
	mu.Lock()
	refuse = true
	mu.Unlock()
	m.Apply("prowlarr")
	st := settle(t, m, "prowlarr", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if r := st.Runs[0]; r.OK || !strings.Contains(r.Detail, "without updating") {
		t.Fatalf("a refusal: %+v", r)
	}

	// Pressed by you: it asks for the major update in so many words, and it goes in.
	mu.Lock()
	refuse = false
	mu.Unlock()
	m.Apply("prowlarr")
	st = settle(t, m, "prowlarr", func(s updates.Status) bool { return len(s.Runs) == 2 })
	if r := st.Runs[0]; !r.OK || r.To != latest {
		t.Fatalf("a major update: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[1]["installMajorUpdate"] != true || asked[1]["name"] != "ApplicationUpdate" {
		t.Fatalf("asked: %v", asked)
	}
	if !strings.Contains(strings.Join(st.Runs[0].Log, "\n"), "(a major update)") {
		t.Errorf("the log says it is a major update: %v", st.Runs[0].Log)
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

// A target saved before Nightjar knew how to update it gets the way once it does; one set up by
// hand keeps its own command.
func TestSavedTargetsGetTheUpdateCommandsNightjarLearns(t *testing.T) {
	store := openStore(t)
	host := &fakeHost{active: true}
	old := []updates.Spec{
		{ID: "qbittorrent", Name: "qBittorrent", Kind: updates.KindRelease, Repo: "qbittorrent/qBittorrent", VersionCommand: "dpkg-query -W qbittorrent-nox", Unit: "qbittorrent@perked"},
		{ID: "seerr", Name: "Seerr", Kind: updates.KindRelease, Repo: "seerr-team/seerr", VersionCommand: "cat v", UpdateCommand: "my-own-update", Unit: "seerr"},
	}
	first := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return old }, false)
	_ = first
	store.SetMeta("updates.recipes", "1") // as saved by the version before

	learned := []updates.Spec{
		{ID: "qbittorrent", Name: "qBittorrent", Kind: updates.KindRelease, Repo: "qbittorrent/qBittorrent", VersionCommand: "/usr/bin/qbittorrent-nox --version", UpdateCommand: "swap-in-static-build", Unit: "qbittorrent@perked", Restart: true},
		{ID: "seerr", Name: "Seerr", Kind: updates.KindRelease, Repo: "seerr-team/seerr", VersionCommand: "cat v", UpdateCommand: "build-it", Restart: true},
	}
	m := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return learned }, false)
	qb, _ := m.Get("qbittorrent")
	if qb.UpdateCommand != "swap-in-static-build" || qb.VersionCommand != "/usr/bin/qbittorrent-nox --version" || !qb.Restart || !qb.CanUpdate {
		t.Fatalf("qBittorrent learned nothing: %+v", qb.Spec)
	}
	if seerr, _ := m.Get("seerr"); seerr.UpdateCommand != "my-own-update" || seerr.Restart {
		t.Fatalf("a command set up by hand was replaced: %+v", seerr.Spec)
	}
	// And it is kept: a restart sees it without detecting again.
	again := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return nil }, false)
	if qb, _ := again.Get("qbittorrent"); qb.UpdateCommand != "swap-in-static-build" {
		t.Fatalf("not kept: %+v", qb.Spec)
	}
}

// A command an older Nightjar wrote from its recipe gets the recipe's fixes; anything else in it
// changed by hand keeps it as it is.
func TestCommandsFromAnOlderRecipeAreUpgraded(t *testing.T) {
	store := openStore(t)
	host := &fakeHost{active: true}
	oldCmd := "set -e\ncd /opt/seerr\ngit fetch --tags --quiet origin\npnpm build"
	newCmd := "set -e\ncd /opt/seerr\ngit fetch --force origin \"+refs/tags/$TAG:refs/tags/$TAG\"\npnpm build"
	saved := []updates.Spec{
		{ID: "seerr", Name: "Seerr", Kind: updates.KindRelease, Repo: "seerr-team/seerr", VersionCommand: "cat v", UpdateCommand: oldCmd},
		{ID: "other", Name: "Other", Kind: updates.KindRelease, Repo: "o/o", VersionCommand: "cat v", UpdateCommand: oldCmd + "\nmy-own-step"},
	}
	updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return saved }, false)
	store.SetMeta("updates.recipes", "2")
	recipe := []updates.Spec{
		{ID: "seerr", Name: "Seerr", Kind: updates.KindRelease, Repo: "seerr-team/seerr", VersionCommand: "cat v", UpdateCommand: newCmd, Restart: true},
		{ID: "other", Name: "Other", Kind: updates.KindRelease, Repo: "o/o", VersionCommand: "cat v", UpdateCommand: newCmd},
	}
	m := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return recipe }, false)
	if s, _ := m.Get("seerr"); s.UpdateCommand != newCmd || !s.Restart {
		t.Fatalf("not upgraded: %q", s.UpdateCommand)
	}
	if o, _ := m.Get("other"); o.UpdateCommand != oldCmd+"\nmy-own-step" {
		t.Fatalf("a changed command was replaced: %q", o.UpdateCommand)
	}
}

// ---------- the worker ----------

// gated holds a command containing hold until it is let go, counting the commands that ran.
type gated struct {
	*fakeHost
	hold    string
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	counts  map[string]int
}

func newGated(h *fakeHost) *gated {
	return &gated{fakeHost: h, started: make(chan struct{}, 16), release: make(chan struct{}), counts: map[string]int{}}
}

func (g *gated) run(ctx context.Context, c updates.Command) (updates.Output, error) {
	line := strings.Join(c.Argv, " ")
	g.mu.Lock()
	g.counts[line]++
	hold := g.hold != "" && strings.Contains(line, g.hold)
	g.mu.Unlock()
	if hold {
		g.started <- struct{}{}
		<-g.release
	}
	return g.fakeHost.run(ctx, c)
}

func (g *gated) holdOn(s string) {
	g.mu.Lock()
	g.hold = s
	g.mu.Unlock()
}

func (g *gated) count(s string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for line, c := range g.counts {
		if strings.Contains(line, s) {
			n += c
		}
	}
	return n
}

// A round that takes longer than a monitoring cycle is not queued again behind itself.
func TestALongCheckRoundIsNotQueuedTwice(t *testing.T) {
	g := newGated(&fakeHost{version: "1.0.0", active: true})
	m := newManager(t, openStore(t), g.run, github(t, "1.0.0").URL, nil)
	m.Save("", app)
	settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	before := g.count("app --version")

	g.holdOn("app --version")
	m.CheckAll()
	<-g.started
	for range 5 {
		m.Tick(time.Now(), time.Nanosecond, updates.Window{})
	}
	g.holdOn("")
	close(g.release)
	deadline := time.Now().Add(10 * time.Second)
	for checking, _ := m.Activity(); checking; checking, _ = m.Activity() {
		if time.Now().After(deadline) {
			t.Fatal("the round never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := g.count("app --version") - before; n != 1 {
		t.Fatalf("one round asked for the version %d times", n)
	}
}

// Stopping waits for an update under way to finish, however long the caller would wait, since
// cutting apt or dpkg off halfway breaks the machine's packages; nothing starts after.
func TestStoppingWaitsForAnUpdateUnderWay(t *testing.T) {
	g := newGated(&fakeHost{version: "1.0.0", next: "1.1.0", active: true})
	m := newManager(t, openStore(t), g.run, github(t, "1.1.0").URL, nil)
	m.Save("", app)
	settle(t, m, "app", func(s updates.Status) bool { return s.Available })

	g.holdOn("install-app")
	m.Apply("app")
	<-g.started
	stopped := make(chan struct{})
	go func() { m.Wait(10 * time.Millisecond); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stopped in the middle of an update")
	case <-time.After(200 * time.Millisecond):
	}
	close(g.release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting after the update finished")
	}
	before := g.count("app --version")
	m.CheckAll()
	m.Check("app")
	time.Sleep(100 * time.Millisecond)
	if g.count("app --version") != before {
		t.Fatal("a job started after stopping")
	}
}

// A command that runs out of time stops with everything it started: sh, runuser and sudo run the
// real work as a child, which holds the output open.
func TestExecTimeoutStopsWhatTheCommandStarted(t *testing.T) {
	start := time.Now()
	_, err := updates.Exec(context.Background(), updates.Command{Argv: []string{"sh", "-c", "sleep 30; true"}, Timeout: 200 * time.Millisecond})
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	// One that finishes but leaves something running in the background has worked.
	out, err := updates.Exec(context.Background(), updates.Command{Argv: []string{"sh", "-c", "sleep 8 & echo done"}, Timeout: time.Minute})
	if err != nil || out.Code != 0 || !strings.Contains(out.Stdout, "done") {
		t.Fatalf("%+v %v", out, err)
	}
}

// A checkout that belongs to a uid with no account is not run as root.
func TestGitCheckoutOfAnUnknownUidIsRefused(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give the checkout away")
	}
	uid := 0
	for id := 60999; id > 50000; id-- {
		if _, err := user.LookupId(strconv.Itoa(id)); err != nil {
			uid = id
			break
		}
	}
	if uid == 0 {
		t.Skip("no free uid")
	}
	checkout := t.TempDir()
	if err := os.Chown(checkout, uid, uid); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{active: true}
	m := newManager(t, openStore(t), host.run, "", nil)
	m.Save("", updates.Spec{ID: "tool", Name: "Tool", Kind: updates.KindGit, Dir: checkout})
	st := settle(t, m, "tool", func(s updates.Status) bool { return s.Checked > 0 })
	if !strings.Contains(st.Error, "no account") {
		t.Fatalf("check: %+v", st.State)
	}
	if host.ranLine("git") != nil {
		t.Fatal("git ran as the monitor")
	}
}

// An app bound to one address is asked there, not on loopback.
func TestArrBoundToOneAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("no 127.0.0.2:", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/system/status":
			json.NewEncoder(w).Encode(map[string]string{"version": "4.0.15.2941"})
		case "/api/v3/update":
			json.NewEncoder(w).Encode([]map[string]any{{"version": "4.0.15.2941", "installed": true}})
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	config := filepath.Join(t.TempDir(), "config.xml")
	os.WriteFile(config, []byte("<Config><Port>"+port+"</Port><BindAddress>127.0.0.2</BindAddress><ApiKey>k3y</ApiKey></Config>"), 0o600)
	m := newManager(t, openStore(t), (&fakeHost{active: true}).run, "", nil)
	m.Save("", updates.Spec{ID: "radarr", Name: "Radarr", Kind: updates.KindArr, ConfigFile: config})
	st := settle(t, m, "radarr", func(s updates.Status) bool { return s.Checked > 0 })
	if st.Error != "" || st.Installed != "4.0.15.2941" {
		t.Fatalf("check: %+v", st.State)
	}
}

// assetGitHub serves acme/app's latest release, v1.3.0, and acme/builds' latest, with one file
// whose digest is digest ("" for none).
func assetGitHub(t *testing.T, digest string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/releases/latest":
			json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.3.0"})
		case "/repos/acme/builds/releases/latest":
			asset := map[string]string{"name": "x86_64-app", "browser_download_url": "https://github.com/acme/builds/releases/download/b1/x86_64-app"}
			if digest != "" {
				asset["digest"] = digest
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "b1", "assets": []map[string]string{asset, {"name": "other", "digest": "sha256:" + strings.Repeat("f", 64)}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A release update that installs a download gets its address and the SHA-256 GitHub lists for it,
// and does not run at all when GitHub lists none.
func TestReleaseDownloadsAreChecked(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	spec := app
	spec.Asset, spec.AssetRepo = "x86_64-app", "acme/builds"

	host := &fakeHost{version: "v1.2.0", next: "v1.3.0", active: true}
	m := newManager(t, openStore(t), host.run, assetGitHub(t, "sha256:"+sum).URL, nil)
	if _, err := m.Save("", spec); err != nil {
		t.Fatal(err)
	}
	settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	m.Apply("app")
	st := settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	c := host.ranLine("install-app")
	if !st.Runs[0].OK || c == nil || !slices.Contains(c.Env, "ASSET_SHA256="+sum) || !slices.Contains(c.Env, "ASSET_URL=https://github.com/acme/builds/releases/download/b1/x86_64-app") {
		t.Fatalf("run %+v, command %+v", st.Runs, c)
	}

	host = &fakeHost{version: "v1.2.0", next: "v1.3.0", active: true}
	m = newManager(t, openStore(t), host.run, assetGitHub(t, "").URL, nil)
	m.Save("", spec)
	settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	m.Apply("app")
	st = settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	if st.Runs[0].OK || !strings.Contains(st.Runs[0].Detail, "SHA-256") || host.ranLine("install-app") != nil {
		t.Fatalf("an unchecked download was installed: %+v", st.Runs[0])
	}
}

// A dashboard save, which does not send the download a recipe checks, keeps it.
func TestSavingFromTheDashboardKeepsTheCheckedDownload(t *testing.T) {
	spec := app
	spec.Asset, spec.AssetRepo = "x86_64-app", "acme/builds"
	m := newManager(t, openStore(t), (&fakeHost{active: true}).run, "", nil)
	if _, err := m.Save("", spec); err != nil {
		t.Fatal(err)
	}
	edited := app
	edited.Auto = true
	if st, err := m.Save("app", edited); err != nil || st.Asset != "x86_64-app" || st.AssetRepo != "acme/builds" {
		t.Fatalf("saved %+v, %v", st.Spec, err)
	}
}

// A release update that works in a folder runs as the folder's owner, not as root.
func TestReleaseUpdateInAFolderRunsAsItsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give the folder away")
	}
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody account")
	}
	uid, _ := strconv.Atoi(nobody.Uid)
	dir := t.TempDir()
	os.Chown(dir, uid, -1)
	spec := app
	spec.Dir = dir
	host := &fakeHost{version: "v1.2.0", next: "v1.3.0", active: true}
	m := newManager(t, openStore(t), host.run, github(t, "v1.3.0").URL, nil)
	m.Save("", spec)
	settle(t, m, "app", func(s updates.Status) bool { return s.Checked > 0 })
	m.Apply("app")
	settle(t, m, "app", func(s updates.Status) bool { return len(s.Runs) == 1 })
	c := host.ranLine("install-app")
	if c == nil || c.User != "nobody" || c.Root || c.Dir != dir {
		t.Fatalf("ran %+v", c)
	}
}

// The qBittorrent recipe as an older Nightjar saved it, which ran an unchecked download as root,
// is replaced with the one that checks it, along with the download it checks.
func TestTheUncheckedQBittorrentRecipeIsReplaced(t *testing.T) {
	const v1 = `set -e
case "$(uname -m)" in x86_64) arch=x86_64 ;; aarch64) arch=aarch64 ;; *) echo "no static qBittorrent build for $(uname -m)"; exit 1 ;; esac
tmp=$(mktemp /usr/bin/.qbittorrent-nox.XXXXXX); trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp" "https://github.com/userdocs/qbittorrent-nox-static/releases/latest/download/${arch}-qbittorrent-nox"
chmod 755 "$tmp"
"$tmp" --version
mv -f "$tmp" /usr/bin/qbittorrent-nox
trap - EXIT`
	store := openStore(t)
	host := &fakeHost{active: true}
	saved := updates.Spec{ID: "qbittorrent", Name: "qBittorrent", Kind: updates.KindRelease, Repo: "qbittorrent/qBittorrent", TagPrefix: "release-", VersionCommand: "/usr/bin/qbittorrent-nox --version", UpdateCommand: v1, Unit: "qbittorrent@perked", Restart: true}
	updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return []updates.Spec{saved} }, false)
	store.SetMeta("updates.recipes", "3")
	recipe := updates.QBittorrentStatic("qbittorrent@perked", "amd64")
	if strings.Contains(recipe.UpdateCommand, "\n\"$tmp\" --version") || !strings.Contains(recipe.UpdateCommand, "sha256sum -c") || !strings.Contains(recipe.UpdateCommand, "setpriv") {
		t.Fatalf("recipe: %s", recipe.UpdateCommand)
	}
	m := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return []updates.Spec{recipe} }, false)
	qb, _ := m.Get("qbittorrent")
	if qb.UpdateCommand != recipe.UpdateCommand || qb.Asset != "x86_64-qbittorrent-nox" || qb.AssetRepo != "userdocs/qbittorrent-nox-static" {
		t.Fatalf("not replaced: %+v", qb.Spec)
	}
}

// A target saved from today's recipe gets what now goes with it, such as the folder its build runs
// in as that folder's owner, without its command changing.
func TestSavedRecipesLearnWhoRunsThem(t *testing.T) {
	store := openStore(t)
	host := &fakeHost{active: true}
	saved := updates.Spec{ID: "seerr", Name: "Seerr", Kind: updates.KindRelease, Repo: "seerr-team/seerr", VersionCommand: "cat v", UpdateCommand: "build-it", Restart: true}
	updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return []updates.Spec{saved} }, false)
	store.SetMeta("updates.recipes", "3")
	recipe := saved
	recipe.Dir = "/opt/seerr"
	m := updates.New(updates.Options{Store: store, Run: host.run}, func() []updates.Spec { return []updates.Spec{recipe} }, false)
	if s, _ := m.Get("seerr"); s.Dir != "/opt/seerr" || s.UpdateCommand != "build-it" {
		t.Fatalf("seerr: %+v", s.Spec)
	}
}
