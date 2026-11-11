package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/services"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
)

const hbToken = "hb-token-hb-token-hb"

// secretsServer is an API with a heartbeat and an address with a password in it, and a session for
// the admin and for a viewer.
func secretsServer(t *testing.T) (s *api.Server, ctx *monitor.Context, db *storage.Store, admin, viewer *http.Cookie) {
	t.Helper()
	db = storage.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	if err := acc.AddViewer("sam", "sam's long password"); err != nil {
		t.Fatal(err)
	}
	adminToken, _ := acc.NewSession("test")
	viewerToken, _ := acc.NewSessionFor(accounts.Who{Username: "sam", Role: accounts.RoleViewer}, "test")
	ctx = &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{},
		Services: services.NewMonitor(nil, db)}
	s = api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir(), Version: "test"}, ctx)
	for _, spec := range []string{
		`{"name":"backup","type":"heartbeat","target":"` + hbToken + `","schedule":"@daily"}`,
		`{"name":"api","type":"http","target":"https://jay:hunter2@example.com/health"}`,
	} {
		sp, err := services.ParseSpec([]byte(spec))
		if err != nil {
			t.Fatal(err)
		}
		if err := ctx.Services.Save("", sp); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx, db, &http.Cookie{Name: "nightjar_session", Value: adminToken}, &http.Cookie{Name: "nightjar_session", Value: viewerToken}
}

// A viewer never gets a heartbeat's token (with it they could hide a failed job, or raise false
// alerts) or a password in a check's address; the admin's list of checks keeps both.
func TestViewerGetsNoCheckSecrets(t *testing.T) {
	s, _, _, admin, viewer := secretsServer(t)
	get := func(path string, cookie *http.Cookie) string {
		rec := serve(s, "GET", path, "", "127.0.0.1:5000", nil, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	for _, path := range []string{"/api/checks", "/api/services"} {
		if out := get(path, viewer); strings.Contains(out, hbToken) || strings.Contains(out, "hunter2") {
			t.Fatalf("a viewer's %s has a secret: %s", path, out)
		}
	}
	if out := get("/api/checks", admin); !strings.Contains(out, hbToken) || !strings.Contains(out, "jay:hunter2@") {
		t.Fatalf("the admin's checks lost their targets: %s", out)
	}
	// The list is the same for both: the admin edits a check from its own full copy.
	if out := get("/api/services", admin); strings.Contains(out, hbToken) || !strings.Contains(out, "https://example.com/health") {
		t.Fatalf("services: %s", out)
	}
}

// A backup without secrets leaves out heartbeat tokens, passwords in addresses and the ntfy topic;
// restoring it keeps the ones here.
func TestBackupWithoutSecretsLeavesOutTargetsAndTopic(t *testing.T) {
	s, ctx, db, admin, _ := secretsServer(t)
	db.SaveSettings(map[string]string{"NTFY_URL": "https://ntfy.sh/private-topic", "DASHBOARD_URL": "https://monitor.example.com"})
	rec := serve(s, "GET", "/api/backup", "", "127.0.0.1:5000", nil, admin)
	out := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(out, hbToken) || strings.Contains(out, "hunter2") || strings.Contains(out, "private-topic") {
		t.Fatalf("a backup without secrets has one: %d %s", rec.Code, out)
	}
	if !strings.Contains(out, "monitor.example.com") {
		t.Fatalf("a plain setting is missing: %s", out)
	}
	if with := serve(s, "GET", "/api/backup?secrets=1", "", "127.0.0.1:5000", nil, admin).Body.String(); !strings.Contains(with, hbToken) ||
		!strings.Contains(with, "hunter2") || !strings.Contains(with, "private-topic") {
		t.Fatalf("a backup with secrets lacks one: %s", with)
	}

	if rec := serve(s, "POST", "/api/backup/restore", out, "127.0.0.1:5000", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if hb, _ := ctx.Services.SpecOf("backup"); hb.Target != hbToken {
		t.Fatalf("the heartbeat got a new token: %q", hb.Target)
	}
	if web, _ := ctx.Services.SpecOf("api"); web.Target != "https://jay:hunter2@example.com/health" {
		t.Fatalf("the address lost its password: %q", web.Target)
	}
	if got := db.Settings()["NTFY_URL"]; got != "https://ntfy.sh/private-topic" {
		t.Fatalf("the ntfy topic was lost: %q", got)
	}
}

// A part of a backup Nightjar could not load is refused before anything is changed.
func TestRestoreChecksEveryPartFirst(t *testing.T) {
	s, ctx, db, admin, _ := secretsServer(t)
	for _, part := range []string{
		`"remote.hosts":{"id":"nas"}`,
		`"maintenance.windows":"soon"`,
		`"incidents.feedback":{"x":{}}`,
		`"dashboard.appearance":{"style":"neon"}`,
		`"statuspage.design":{"groups":"all"}`,
		`"notify.prefs":null`,
	} {
		body := `{"nightjar":"test","secrets":true,"checks":[{"name":"new","type":"tcp","target":"127.0.0.1:1"}],` +
			`"settings":{"DASHBOARD_URL":"https://other.example.com"},"meta":{"plugins.state":{},` + part + `}}`
		if rec := serve(s, "POST", "/api/backup/restore", body, "127.0.0.1:5000", nil, admin); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", part, rec.Code, rec.Body)
		}
	}
	if ctx.Services.Has("new") || db.Settings()["DASHBOARD_URL"] != "" || db.Meta("plugins.state") != "" {
		t.Fatal("a refused restore changed something")
	}
	good := `{"nightjar":"test","secrets":true,"meta":{"remote.hosts":[{"id":"nas","name":"NAS","address":"https://10.0.0.20:9101"}],` +
		`"incidents.feedback":{"12":{"verdict":"right"}},"mediacleanup.state":{"items":{}}}}`
	if rec := serve(s, "POST", "/api/backup/restore", good, "127.0.0.1:5000", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("a good restore: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(db.Meta("remote.hosts"), "10.0.0.20") {
		t.Fatalf("not restored: %q", db.Meta("remote.hosts"))
	}
}

// A restore is refused while an update is applied: the restart that puts it in force would wait
// for the update, and meanwhile what is kept in memory could be saved over it.
func TestRestoreWaitsForAnUpdate(t *testing.T) {
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	// Once blocking is set, every command (the update's first) waits until the test ends.
	release := make(chan struct{})
	var blocking atomic.Bool
	run := func(context.Context, updates.Command) (updates.Output, error) {
		if blocking.Load() {
			<-release
		}
		return updates.Output{Stdout: "1.0.0"}, nil
	}
	mgr := updates.New(updates.Options{Store: store, Run: run, GitHubAPI: "http://127.0.0.1:1"}, nil, false)
	t.Cleanup(func() { close(release); mgr.Wait(5 * time.Second) })
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), Updates: mgr}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Store: store, FrontendDir: t.TempDir()}, ctx)
	st, err := mgr.Save("", updates.Spec{Name: "Plex", Kind: updates.KindApt, Package: "plexmediaserver"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(what string, done func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	// The check a new target gets runs first.
	waitFor("the first check never ended", func() bool { got, _ := mgr.Get(st.ID); return got.Busy == "" })
	blocking.Store(true)
	if err := mgr.Apply(st.ID); err != nil {
		t.Fatal(err)
	}
	waitFor("the update never started", func() bool { return mgr.Applying() != "" })
	body := `{"nightjar":"test","secrets":true,"settings":{"DASHBOARD_URL":"https://other.example.com"}}`
	rec := serve(s, "POST", "/api/backup/restore", body, "127.0.0.1:5000", nil, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "update") {
		t.Fatalf("restore during an update: %d %s", rec.Code, rec.Body)
	}
	if store.Settings()["DASHBOARD_URL"] != "" {
		t.Fatal("the refused restore saved a setting")
	}
}

// A deleted check leaves the status page, so a later check given its name is not shown there under
// the old public label.
func TestDeletedCheckLeavesTheStatusPage(t *testing.T) {
	m := newManaged(t)
	m.send(t, http.MethodPost, "/api/checks", `{"name":"web","type":"tcp","target":"127.0.0.1:1"}`)
	m.send(t, http.MethodPost, "/api/checks", `{"name":"db","type":"tcp","target":"127.0.0.1:2"}`)
	design := `{"groups":[{"name":"Site","items":[{"check":"web","label":"Website"},{"check":"db"}]}]}`
	if code, body := m.send(t, http.MethodPut, "/api/statuspage", design); code != http.StatusOK {
		t.Fatalf("design: %d %v", code, body)
	}
	if code, _ := m.send(t, http.MethodDelete, "/api/checks/web", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if d := m.store.Meta("statuspage.design"); strings.Contains(d, `"web"`) || strings.Contains(d, "Website") || !strings.Contains(d, `"db"`) {
		t.Fatalf("design after the delete: %s", d)
	}
}

// Taking feedback or a notice back that cannot be saved is not reported done.
func TestTakingBackFeedbackThatCannotBeSaved(t *testing.T) {
	m := newManaged(t)
	now := float64(time.Now().Unix())
	m.store.AddNotification(storage.Notification{TS: now - 600, Area: "uptime", Severity: "critical", Title: "api is down", Subject: "check:api", Status: "sent"})
	_, body := m.send(t, http.MethodGet, "/api/incidents", "")
	list, _ := body["incidents"].([]any)
	if len(list) == 0 {
		t.Fatalf("no incident: %v", body)
	}
	id := fmt.Sprint(list[0].(map[string]any)["id"])
	m.store.SetMeta("incidents.feedback", "{not json")
	m.store.SetMeta("incidents.notices", "{not json")
	for _, part := range []string{"feedback", "notice"} {
		if code, out := m.send(t, http.MethodDelete, "/api/incidents/"+id+"/"+part, ""); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d %v", part, code, out)
		}
	}
}

// Each of the status page's 90 days starts at its own local midnight, also before a clock change.
func TestStatusDaysFollowClockChanges(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	was := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = was })
	m := newManaged(t)
	m.send(t, http.MethodPost, "/api/checks", `{"name":"web","type":"tcp","target":"127.0.0.1:1"}`)
	_, d := m.send(t, http.MethodGet, "/api/statuspage", "")
	days := d["days"].(map[string]any)["web"].([]any)
	y, mo, day := time.Now().In(loc).Date()
	for i, raw := range days {
		want := float64(time.Date(y, mo, day-89+i, 0, 0, 0, 0, loc).Unix())
		if got := raw.(map[string]any)["t"]; got != want {
			b, _ := json.Marshal(raw)
			t.Fatalf("day %d starts at %v, want %v: %s", i, got, want, b)
		}
	}
}
