package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

func TestTwoFactorActivityAndBackup(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{},
		Services: services.NewMonitor(nil, db)}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir(), Version: "test"}, ctx)
	var cookie *http.Cookie
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "10.0.0.9:1234"
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "nightjar_session" {
				cookie = c
			}
		}
		return rec.Code, out
	}
	if code, _ := call("POST", "/api/auth/setup", `{"username":"jay","password":"correct horse battery","setup_code":"`+acc.SetupCode()+`"}`); code != http.StatusOK {
		t.Fatal("setup")
	}

	// Two-factor: begin, confirm with a code, and the recovery codes come back once.
	_, begin := call("POST", "/api/auth/2fa/begin", `{}`)
	secret := begin["secret"].(string)
	if !strings.HasPrefix(begin["link"].(string), "otpauth://totp/Nightjar:jay?") {
		t.Fatalf("link %v", begin["link"])
	}
	if code, _ := call("POST", "/api/auth/2fa/enable", `{"code":"000000"}`); code != http.StatusBadRequest {
		t.Fatal("a wrong code turned it on")
	}
	now := time.Now().Unix()
	code, en := call("POST", "/api/auth/2fa/enable", `{"code":"`+accounts.TOTP(secret, now)+`"}`)
	if code != http.StatusOK || len(en["recovery"].([]any)) != 10 {
		t.Fatalf("enable: %d %v", code, en)
	}
	recovery := en["recovery"].([]any)[0].(string)

	// Signing in now needs the code: the password alone is asked for one.
	cookie = nil
	code, body := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`)
	if code != http.StatusUnauthorized || body["need_code"] != true || cookie != nil {
		t.Fatalf("password alone: %d %v", code, body)
	}
	// The code used to turn it on cannot be used again.
	if code, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery","code":"`+accounts.TOTP(secret, now)+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("a replayed code: %d", code)
	}
	if code, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery","code":"`+recovery+`"}`); code != http.StatusOK || cookie == nil {
		t.Fatalf("a recovery code: %d", code)
	}
	cookie = nil
	if code, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery","code":"`+recovery+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("a recovery code is good once: %d", code)
	}
	if code, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery","code":"`+en["recovery"].([]any)[1].(string)+`"}`); code != http.StatusOK {
		t.Fatal("another recovery code")
	}
	if _, cfg := call("GET", "/api/config", ""); cfg["account"].(map[string]any)["two_factor"] != true || cfg["account"].(map[string]any)["recovery_left"] != float64(8) {
		t.Fatalf("config: %v", cfg["account"])
	}

	// The activity log: changes are kept, by whom; tries and reads are not.
	call("POST", "/api/checks/test", `{"name":"x","type":"tcp","target":"127.0.0.1:1"}`)
	if code, _ := call("POST", "/api/checks", `{"name":"nas","type":"tcp","target":"127.0.0.1:1"}`); code != http.StatusCreated {
		t.Fatal("add check")
	}
	_, act := call("GET", "/api/activity", "")
	var lines []string
	for _, a := range act["activity"].([]any) {
		e := a.(map[string]any)
		lines = append(lines, e["who"].(string)+": "+e["action"].(string))
	}
	got := strings.Join(lines, " | ")
	for _, want := range []string{"jay: Added a check", "jay: Signed in", "jay: Turned on two-factor sign-in"} {
		if !strings.Contains(got, want) {
			t.Errorf("activity lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "test") {
		t.Errorf("a test is not a change: %s", got)
	}

	// A backup leaves secrets out unless asked; restoring it puts a deleted check back.
	call("PUT", "/api/settings", `{"values":{"CPU_THRESHOLD":"85","PLEX_TOKEN":"hunter2"}}`)
	_, b := call("GET", "/api/backup", "")
	settings := b["settings"].(map[string]any)
	if settings["CPU_THRESHOLD"] != "85" || settings["PLEX_TOKEN"] != nil || b["secrets"] != false || len(b["checks"].([]any)) != 1 {
		t.Fatalf("backup: %v", b)
	}
	if _, withSecrets := call("GET", "/api/backup?secrets=1", ""); withSecrets["settings"].(map[string]any)["PLEX_TOKEN"] != "hunter2" {
		t.Fatal("secrets when asked")
	}
	call("DELETE", "/api/checks/nas", "")
	raw, _ := json.Marshal(b)
	if code, out := call("POST", "/api/backup/restore", string(raw)); code != http.StatusOK || out["restored"].(map[string]any)["checks"] != float64(1) {
		t.Fatalf("restore: %d %v", code, out)
	}
	if !ctx.Services.Has("nas") {
		t.Fatal("the check is back")
	}
	if code, _ := call("POST", "/api/backup/restore", `{"nightjar":"x","meta":{"auth.admin":{}}}`); code != http.StatusBadRequest {
		t.Fatalf("a backup cannot replace the account: %d", code)
	}

	// Turning it off takes the password and a code.
	if code, _ := call("POST", "/api/auth/2fa/disable", `{"password":"wrong password here","code":"`+accounts.TOTP(secret, now+30)+`"}`); code != http.StatusBadRequest {
		t.Fatal("off without the password")
	}
	if code, _ := call("POST", "/api/auth/2fa/disable", `{"password":"correct horse battery","code":"`+accounts.TOTP(secret, now+30)+`"}`); code != http.StatusNoContent || acc.TwoFactor() {
		t.Fatalf("off: %d", code)
	}
}
