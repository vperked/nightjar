package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/apps"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

// An install's log can hold the app's first-time admin password, fished out of its journal. The
// admin reads it; a viewer sees that a job ran and how it ended, but not a line of its log.
func TestViewerDoesNotReadAppJobLogs(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "v.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	if err := acc.AddViewer("sam", "sam's long password"); err != nil {
		t.Fatal(err)
	}
	manifests := t.TempDir()
	os.WriteFile(filepath.Join(manifests, "qbittorrent.json"), []byte(`{"version":"2.0.0","port":8080}`), 0o644)
	store := apps.New(apps.Options{
		ManifestDir: manifests, LockDir: t.TempDir(), Arch: "amd64",
		Systemctl: func(...string) string { return "" }, Processes: func() []string { return nil },
		Run: func(_ context.Context, _, _ string, line func(string)) error {
			line("A temporary password is provided for this session: hunter2hunter2")
			return nil
		},
	})
	if _, err := store.Remove("qbittorrent", false); err != nil {
		t.Fatal(err)
	}
	store.Wait(10 * time.Second)

	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Apps: store}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir()}, ctx)

	signIn := func(user, pass string) *http.Cookie {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
		req.RemoteAddr = "10.0.0.7:1234"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "nightjar_session" {
				return c
			}
		}
		t.Fatalf("%s could not sign in: %d %s", user, rec.Code, rec.Body)
		return nil
	}
	get := func(c *http.Cookie, path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = "10.0.0.7:1234"
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	admin, viewer := signIn("jay", "correct horse battery"), signIn("sam", "sam's long password")

	for _, path := range []string{"/api/apps", "/api/apps/job"} {
		code, body := get(admin, path)
		if code != http.StatusOK || !strings.Contains(body, "hunter2hunter2") {
			t.Errorf("admin GET %s: %d %s, want the log", path, code, body)
		}
		code, body = get(viewer, path)
		var out struct {
			Job *apps.Job `json:"job"`
		}
		json.Unmarshal([]byte(body), &out)
		if code != http.StatusOK || strings.Contains(body, "hunter2") || out.Job == nil || !out.Job.OK || len(out.Job.Lines) != 0 {
			t.Errorf("viewer GET %s: %d %s, want the job without its log", path, code, body)
		}
	}
}
