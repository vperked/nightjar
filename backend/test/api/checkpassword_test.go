package api_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// A redis check's password is kept, but never sent back: not in the list, not after a save, not in a
// backup without secrets. Saving without retyping it keeps it; clear_password removes it.
func TestCheckPasswordNeverLeavesTheServer(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	t.Cleanup(db.Close)
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{},
		Services: services.NewMonitor(nil, db)}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Store: db, FrontendDir: t.TempDir()}, ctx)
	call := func(method, path, body string) (int, string) {
		rec := serve(s, method, path, body, "127.0.0.1:5000", nil, nil)
		return rec.Code, rec.Body.String()
	}
	saved := func() string {
		sp, _ := ctx.Services.SpecOf("cache")
		return sp.Password
	}

	code, out := call("POST", "/api/checks", `{"name":"cache","type":"redis","target":"127.0.0.1:6379","password":"s3cret pass"}`)
	if code != http.StatusCreated || strings.Contains(out, "s3cret") || !strings.Contains(out, `"password_set":true`) {
		t.Fatalf("create: %d %s", code, out)
	}
	if saved() != "s3cret pass" {
		t.Fatalf("stored password %q", saved())
	}
	if _, out := call("GET", "/api/checks", ""); strings.Contains(out, "s3cret") || !strings.Contains(out, `"password_set":true`) {
		t.Fatalf("list leaks or loses the password: %s", out)
	}
	// Saved again, as the form sends it, without the password: it is kept.
	if code, out := call("PUT", "/api/checks/cache", `{"name":"cache","type":"redis","target":"127.0.0.1:6380"}`); code != http.StatusOK || strings.Contains(out, "s3cret") {
		t.Fatalf("update: %d %s", code, out)
	}
	if saved() != "s3cret pass" {
		t.Fatalf("an edit without the password lost it: %q", saved())
	}
	if _, out := call("GET", "/api/backup", ""); strings.Contains(out, "s3cret") {
		t.Fatalf("a backup without secrets has the password: %s", out)
	}
	if _, out := call("GET", "/api/backup?secrets=1", ""); !strings.Contains(out, "s3cret pass") {
		t.Fatalf("a backup with secrets lacks the password: %s", out)
	}
	// Restoring a backup without secrets keeps the password the check has.
	body := `{"nightjar":"test","secrets":false,"checks":[{"name":"cache","type":"redis","target":"127.0.0.1:6380"}]}`
	if code, out := call("POST", "/api/backup/restore", body); code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, out)
	}
	if saved() != "s3cret pass" {
		t.Fatalf("restoring without secrets lost the password: %q", saved())
	}
	// Another type never keeps one.
	if code, _ := call("PUT", "/api/checks/cache", `{"name":"cache","type":"tcp","target":"127.0.0.1:6380","password":"x"}`); code != http.StatusOK || saved() != "" {
		t.Fatalf("a tcp check kept a password: %q", saved())
	}
	call("PUT", "/api/checks/cache", `{"name":"cache","type":"redis","target":"127.0.0.1:6380","password":"new one"}`)
	if code, _ := call("PUT", "/api/checks/cache", `{"name":"cache","type":"redis","target":"127.0.0.1:6380","clear_password":true}`); code != http.StatusOK || saved() != "" {
		t.Fatalf("clear_password left %q", saved())
	}
}
