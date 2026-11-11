package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

// A viewer sees what the admin sees, except secrets, and can change nothing; the admin manages viewers.
func TestViewerIsReadOnly(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "v.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir()}, ctx)

	type browser struct{ cookie *http.Cookie }
	call := func(b *browser, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "10.0.0.7:1234"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if b.cookie != nil {
			req.AddCookie(b.cookie)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "nightjar_session" {
				b.cookie = c
			}
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	admin, viewer := &browser{}, &browser{}
	if code, _ := call(admin, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`); code != http.StatusOK {
		t.Fatalf("admin sign-in: %d", code)
	}
	if code, out := call(admin, "POST", "/api/auth/viewers", `{"username":"Jay","password":"another long one"}`); code != http.StatusConflict {
		t.Fatalf("a viewer named like the admin: %d %v", code, out)
	}
	if code, out := call(admin, "POST", "/api/auth/viewers", `{"username":"sam","password":"short"}`); code != http.StatusBadRequest {
		t.Fatalf("a short password: %d %v", code, out)
	}
	if code, out := call(admin, "POST", "/api/auth/viewers", `{"username":"sam","password":"sam's long password"}`); code != http.StatusOK || len(out["viewers"].([]any)) != 1 {
		t.Fatalf("add viewer: %d %v", code, out)
	}

	code, out := call(viewer, "POST", "/api/auth/login", `{"username":"SAM","password":"sam's long password"}`)
	if code != http.StatusOK || out["role"] != "viewer" || out["username"] != "sam" {
		t.Fatalf("viewer sign-in: %d %v", code, out)
	}
	_, cfg := call(viewer, "GET", "/api/config", "")
	if a := cfg["account"].(map[string]any); cfg["authorized"] != true || a["role"] != "viewer" || a["username"] != "sam" {
		t.Fatalf("viewer config: %v", cfg)
	}
	_, cfg = call(admin, "GET", "/api/config", "")
	if a := cfg["account"].(map[string]any); a["role"] != "admin" || a["username"] != "jay" {
		t.Fatalf("admin config: %v", cfg)
	}

	// Reads work; secrets and changes do not.
	for _, path := range []string{"/api/services", "/api/checks", "/api/analytics", "/api/activity", "/api/notifications"} {
		if code, _ := call(viewer, "GET", path, ""); code != http.StatusOK {
			t.Errorf("viewer GET %s: %d, want 200", path, code)
		}
	}
	for _, path := range []string{"/api/settings", "/api/backup", "/api/setup", "/api/auth/viewers", "/api/units/x.service/logs"} {
		if code, out := call(viewer, "GET", path, ""); code != http.StatusForbidden || out["read_only"] != true {
			t.Errorf("viewer GET %s: %d %v, want 403", path, code, out)
		}
	}
	writes := [][3]string{
		{"POST", "/api/checks", `{"name":"x","type":"tcp","target":"127.0.0.1:1"}`},
		{"PUT", "/api/settings", `{"values":{}}`},
		{"POST", "/api/auth/viewers", `{"username":"eve","password":"eve's long password"}`},
		{"DELETE", "/api/auth/viewers/sam", ""},
	}
	for _, wr := range writes {
		if code, _ := call(viewer, wr[0], wr[1], wr[2]); code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", wr[0], wr[1], code)
		}
	}
	if code, _ := call(admin, "GET", "/api/settings", ""); code != http.StatusOK {
		t.Errorf("admin settings: %d", code)
	}

	// A viewer changes its own password: the right current one is needed, its other browsers are
	// signed out, this one stays, and the admin's password is untouched.
	other := &browser{}
	if code, _ := call(other, "POST", "/api/auth/login", `{"username":"sam","password":"sam's long password"}`); code != http.StatusOK {
		t.Fatalf("viewer second browser: %d", code)
	}
	if code, _ := call(viewer, "POST", "/api/auth/password", `{"current":"wrong wrong wrong","new":"sam's changed password"}`); code != http.StatusForbidden {
		t.Errorf("viewer password change with a wrong current one: %d, want 403", code)
	}
	if code, out := call(viewer, "POST", "/api/auth/password", `{"current":"sam's long password","new":"sam's changed password"}`); code != http.StatusOK {
		t.Fatalf("viewer password change: %d %v", code, out)
	}
	if code, _ := call(viewer, "GET", "/api/services", ""); code != http.StatusOK {
		t.Errorf("the viewer's own browser after its password change: %d", code)
	}
	if code, _ := call(other, "GET", "/api/services", ""); code != http.StatusUnauthorized {
		t.Errorf("the viewer's other browser after its password change: %d, want 401", code)
	}
	if code, _ := call(admin, "GET", "/api/settings", ""); code != http.StatusOK {
		t.Errorf("admin after the viewer's password change: %d", code)
	}
	if code, _ := call(&browser{}, "POST", "/api/auth/login", `{"username":"sam","password":"sam's changed password"}`); code != http.StatusOK {
		t.Errorf("viewer sign-in with the changed password: %d", code)
	}

	// The admin changing their own password leaves the viewer signed in.
	if code, out := call(admin, "POST", "/api/auth/password", `{"current":"correct horse battery","new":"an even better password"}`); code != http.StatusOK {
		t.Fatalf("admin password change: %d %v", code, out)
	}
	if code, _ := call(viewer, "GET", "/api/services", ""); code != http.StatusOK {
		t.Errorf("viewer after the admin's password change: %d", code)
	}

	// A new password for the viewer, then removing it, signs it out.
	if code, _ := call(admin, "PUT", "/api/auth/viewers/sam", `{"password":"sam's newer password"}`); code != http.StatusOK {
		t.Fatalf("set viewer password: %d", code)
	}
	if code, _ := call(viewer, "GET", "/api/services", ""); code != http.StatusUnauthorized {
		t.Errorf("viewer after a new password: %d, want 401", code)
	}
	viewer = &browser{}
	if code, _ := call(viewer, "POST", "/api/auth/login", `{"username":"sam","password":"sam's newer password"}`); code != http.StatusOK {
		t.Fatalf("viewer sign-in with the new password: %d", code)
	}
	if code, _ := call(admin, "DELETE", "/api/auth/viewers/sam", ""); code != http.StatusOK {
		t.Fatalf("remove viewer: %d", code)
	}
	if code, _ := call(viewer, "GET", "/api/services", ""); code != http.StatusUnauthorized {
		t.Errorf("removed viewer: %d, want 401", code)
	}
	if code, _ := call(&browser{}, "POST", "/api/auth/login", `{"username":"sam","password":"sam's newer password"}`); code != http.StatusUnauthorized {
		t.Errorf("removed viewer signing in: %d, want 401", code)
	}

	// Who did what is the account's name.
	found := false
	for _, a := range db.Activities(50, 0) {
		if a.Who == "jay" && strings.Contains(a.Action, "viewer") {
			found = true
		}
	}
	if !found {
		t.Error("viewer changes are not in the activity log under the admin's name")
	}
}
