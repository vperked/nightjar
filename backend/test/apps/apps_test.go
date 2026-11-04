package apps_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/apps"
)

// fakeGitHub answers "latest release" for every repo with assets for both architectures.
func fakeGitHub(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.3", "assets": []map[string]string{
			{"name": "x86_64-qbittorrent-nox", "browser_download_url": "https://example.com/qb"},
			{"name": "Jackett.Binaries.LinuxAMDx64.tar.gz", "browser_download_url": "https://example.com/jackett.tar.gz"},
			{"name": "thing_1.2.3_linux_x86_64.tar.gz", "browser_download_url": "https://example.com/x86_64.tar.gz"},
			{"name": "thing_1.2.3_linux_amd64.tar.gz", "browser_download_url": "https://example.com/amd64.tar.gz"},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type recorder struct {
	mu      sync.Mutex
	scripts map[string]string // the last script per app
	all     []string
	fail    bool
}

func (r *recorder) run(_ context.Context, name, script string, line func(string)) error {
	r.mu.Lock()
	r.scripts[name] = script
	r.all = append(r.all, script)
	r.mu.Unlock()
	line("::step:: Downloading")
	line("password=hunter2")
	if r.fail {
		return os.ErrPermission
	}
	return nil
}

func newManager(t *testing.T, units, active string, procs ...string) (*apps.Manager, *recorder, string, string) {
	t.Helper()
	manifests, locks := t.TempDir(), t.TempDir()
	rec := &recorder{scripts: map[string]string{}}
	m := apps.New(apps.Options{
		Run: rec.run, ManifestDir: manifests, LockDir: locks, Arch: "amd64", GitHubAPI: fakeGitHub(t).URL,
		Processes: func() []string { return procs },
		Systemctl: func(args ...string) string {
			if args[0] == "list-units" {
				return active
			}
			return units
		},
	})
	return m, rec, manifests, locks
}

func status(m *apps.Manager, id string) apps.Status {
	for _, s := range m.Statuses() {
		if s.ID == id {
			return s
		}
	}
	return apps.Status{}
}

func TestStatusKnowsWhoInstalledWhat(t *testing.T) {
	m, _, manifests, locks := newManager(t,
		"sonarr.service enabled enabled\nqbittorrent@.service indirect enabled\n",
		"qbittorrent@perked.service loaded active running\nntfy.service loaded active running\n")
	os.WriteFile(filepath.Join(locks, ".prowlarr.lock"), nil, 0o644)
	os.WriteFile(filepath.Join(manifests, "ntfy.json"), []byte(`{"version":"2.36.0","port":2586}`), 0o644)

	if s := status(m, "sonarr"); !s.Installed || s.By != "system" || s.Running {
		t.Fatalf("sonarr has a unit file but is not running: %+v", s)
	}
	if s := status(m, "qbittorrent"); !s.Installed || !s.Running || s.Unit != "qbittorrent@perked.service" {
		t.Fatalf("qBittorrent runs as a template instance: %+v", s)
	}
	if s := status(m, "prowlarr"); !s.Installed || s.By != "swizzin" {
		t.Fatalf("prowlarr has a swizzin lock: %+v", s)
	}
	if s := status(m, "ntfy"); !s.Installed || s.By != "store" || !s.Running || s.Version != "2.36.0" || s.Port != 2586 {
		t.Fatalf("ntfy came from the store: %+v", s)
	}
	if s := status(m, "lidarr"); s.Installed || !s.Available {
		t.Fatalf("lidarr is not installed and can be: %+v", s)
	}
}

func TestInstallBuildsAServiceAndLogsTheJob(t *testing.T) {
	m, rec, _, _ := newManager(t, "", "")
	var added int
	job, err := m.Install("autobrr", 17474, "perked", func(a apps.App, port int) { added = port })
	if err != nil || job.Action != "install" {
		t.Fatalf("install: %v %+v", err, job)
	}
	m.Wait(5 * time.Second)
	got := m.Job(0)
	if !got.OK || got.Step != "Downloading" || added != 17474 {
		t.Fatalf("job: %+v (check port %d)", got, added)
	}
	if strings.Contains(strings.Join(got.Lines, "\n"), "hunter2") {
		t.Fatal("secrets in the install log are masked")
	}
	script := rec.scripts["autobrr"]
	for _, want := range []string{
		"https://example.com/x86_64.tar.gz", `step "Downloading autobrr 1.2.3"`,
		"User=perked", "ExecStart=/opt/autobrr/autobrr --config=/var/lib/autobrr", "port = 17474",
		"systemctl enable --now autobrr.service", "/autobrr.json",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script should contain %q", want)
		}
	}
	if j := m.Job(len(got.Lines)); len(j.Lines) != 0 {
		t.Fatalf("after skips lines already seen: %v", j.Lines)
	}
}

func TestInstallRefusals(t *testing.T) {
	m, rec, _, _ := newManager(t, "sonarr.service enabled enabled\n", "")
	if _, err := m.Install("sonarr", 0, "perked", nil); err != apps.ErrInstalled {
		t.Fatalf("another installer's sonarr: %v", err)
	}
	if _, err := m.Install("nope", 0, "perked", nil); err != apps.ErrUnknown {
		t.Fatalf("unknown app: %v", err)
	}
	if _, err := m.Install("lidarr", 0, "root; rm -rf /", nil); err == nil {
		t.Fatal("a bad user name must never reach the script")
	}
	ln, _ := net.Listen("tcp", ":0")
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port
	if _, err := m.Install("lidarr", busy, "perked", nil); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a port in use: %v", err)
	}
	if _, err := m.Remove("lidarr", false); err != apps.ErrNotOurs {
		t.Fatalf("removing what the store did not install: %v", err)
	}

	// One job at a time.
	slow := make(chan struct{})
	m2 := apps.New(apps.Options{Arch: "amd64", GitHubAPI: fakeGitHub(t).URL, ManifestDir: t.TempDir(), LockDir: t.TempDir(),
		Systemctl: func(...string) string { return "" }, Processes: func() []string { return nil },
		Run: func(context.Context, string, string, func(string)) error { <-slow; return nil }})
	if _, err := m2.Install("jackett", 0, "perked", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Install("ntfy", 0, "perked", nil); err != apps.ErrBusy {
		t.Fatalf("a second install while one runs: %v", err)
	}
	close(slow)
	m2.Wait(5 * time.Second)
	_ = rec
}

func TestFailedInstallSaysSo(t *testing.T) {
	m, rec, _, _ := newManager(t, "", "")
	rec.fail = true
	m.Install("ntfy", 0, "perked", nil)
	m.Wait(5 * time.Second)
	if j := m.Job(0); j.OK || j.Error == "" || j.Finished == 0 {
		t.Fatalf("job: %+v", j)
	}
}

// Every recipe's install and remove scripts must at least parse as bash.
func TestEveryScriptIsValidBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	m, rec, manifests, _ := newManager(t, "", "")
	port := 20000
	for _, a := range apps.Catalog {
		port++
		if _, err := m.Install(a.ID, port, "perked", nil); err != nil {
			t.Fatalf("%s: %v", a.ID, err)
		}
		m.Wait(5 * time.Second)
		if j := m.Job(0); !j.OK {
			t.Fatalf("%s: %+v", a.ID, j)
		}
		os.WriteFile(filepath.Join(manifests, a.ID+".json"), []byte(`{"port":`+strconv.Itoa(port)+`}`), 0o644)
		if _, err := m.Remove(a.ID, true); err != nil {
			t.Fatalf("%s remove: %v", a.ID, err)
		}
		m.Wait(5 * time.Second)
	}
	if len(rec.all) != 2*len(apps.Catalog) {
		t.Fatalf("expected an install and a remove script per app, got %d", len(rec.all))
	}
	for _, script := range rec.all {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%v\n%s\n%s", err, out, script)
		}
	}
}

func TestReinstallKeepsItsPortAndJobLinesAreNeverNull(t *testing.T) {
	m, rec, manifests, _ := newManager(t, "", "")
	os.WriteFile(filepath.Join(manifests, "autobrr.json"), []byte(`{"version":"1.0.0","port":17475}`), 0o644)
	var added int
	if _, err := m.Install("autobrr", 7474, "perked", func(_ apps.App, port int) { added = port }); err != nil {
		t.Fatal(err)
	}
	m.Wait(5 * time.Second)
	if added != 17475 || !strings.Contains(rec.scripts["autobrr"], "port = 17475") {
		t.Fatalf("a reinstall keeps the port its config was written with, got %d", added)
	}
	// A client that asks past the end, e.g. after a restart, gets an empty list, not null.
	raw, _ := json.Marshal(m.Job(1 << 20))
	if !strings.Contains(string(raw), `"lines":[]`) {
		t.Fatalf("lines past the end: %s", raw)
	}
}

func TestUnsafeDownloadAddressesNeverReachTheScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.3", "assets": []map[string]string{
			{"name": "ntfy_1.2.3_linux_amd64.tar.gz", "browser_download_url": "https://example.com/$(touch /tmp/pwned).tar.gz"},
		}})
	}))
	defer srv.Close()
	ran := false
	m := apps.New(apps.Options{Arch: "amd64", GitHubAPI: srv.URL, ManifestDir: t.TempDir(), LockDir: t.TempDir(),
		Systemctl: func(...string) string { return "" }, Processes: func() []string { return nil },
		Run: func(context.Context, string, string, func(string)) error { ran = true; return nil }})
	if _, err := m.Install("ntfy", 0, "perked", nil); err != nil {
		t.Fatal(err)
	}
	m.Wait(5 * time.Second)
	if j := m.Job(0); j.OK || ran || !strings.Contains(j.Error, "unexpected download address") {
		t.Fatalf("job: %+v (script ran: %v)", j, ran)
	}
}

func TestARunningProgramCountsAsInstalled(t *testing.T) {
	// Unpackerr in Docker, and qBittorrent started by hand: no unit or lock the store knows.
	m, _, _, _ := newManager(t, "", "", "dockerd", "unpackerr", "qbittorrent-nox")
	for _, id := range []string{"unpackerr", "qbittorrent"} {
		if s := status(m, id); !s.Installed || !s.Running || s.By != "system" {
			t.Fatalf("%s is running: %+v", id, s)
		}
	}
	if _, err := m.Install("unpackerr", 0, "perked", nil); err != apps.ErrInstalled {
		t.Fatalf("installing over a running copy: %v", err)
	}
	if s := status(m, "lidarr"); s.Installed {
		t.Fatalf("lidarr is not running: %+v", s)
	}
}
