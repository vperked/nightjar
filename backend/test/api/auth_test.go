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

func TestAdminAccountGuardsEverything(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	ctx := &monitor.Context{Cfg: config.Settings{StatusPageEnabled: true}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "0.0.0.0", Port: "0", Accounts: acc, AuthToken: "script-token", Store: db, FrontendDir: t.TempDir()}, ctx)

	var cookie *http.Cookie
	call := func(method, path, body, ip string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = ip + ":1234"
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
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
		return rec, out
	}

	// Before the account exists: config says so, everything else is locked, the status page stays public.
	_, cfg := call("GET", "/api/config", "", "10.0.0.1")
	if cfg["auth_required"] != true || cfg["authorized"] != false || cfg["account"].(map[string]any)["configured"] != false {
		t.Fatalf("config before setup: %v", cfg)
	}
	if rec, _ := call("GET", "/api/services", "", "10.0.0.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("services before setup: %d", rec.Code)
	}
	if rec, _ := call("GET", "/api/status", "", "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("the public status page should stay public: %d", rec.Code)
	}
	if rec, _ := call("POST", "/api/auth/setup", `{"username":"jay","password":"correct horse battery","setup_code":"AAAAA-BBBBB"}`, "10.0.0.1"); rec.Code != http.StatusForbidden || cookie != nil {
		t.Fatalf("a wrong setup code: %d", rec.Code)
	}

	rec, body := call("POST", "/api/auth/setup", `{"username":"jay","password":"correct horse battery","setup_code":"`+acc.SetupCode()+`"}`, "10.0.0.1")
	if rec.Code != http.StatusOK || body["username"] != "jay" || cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup: %d %v %+v", rec.Code, body, cookie)
	}
	if rec, _ := call("GET", "/api/services", "", "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("signed in after setup: %d", rec.Code)
	}
	if rec, _ := call("POST", "/api/auth/setup", `{"username":"eve","password":"another long one","setup_code":"x"}`, "10.0.0.1"); rec.Code != http.StatusConflict {
		t.Fatalf("a second account: %d", rec.Code)
	}

	// Signing out ends the session for good, even if the cookie is replayed.
	stolen := cookie
	call("POST", "/api/auth/logout", "{}", "10.0.0.1")
	cookie = stolen
	if rec, _ := call("GET", "/api/services", "", "10.0.0.1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after sign-out: %d", rec.Code)
	}
	if cookie.Value != "" || cookie.MaxAge >= 0 {
		t.Fatalf("a stale cookie is cleared: %+v", cookie)
	}
	// A page full of requests with a stale cookie must not lock its owner out of signing back in.
	for range 15 {
		cookie = stolen
		call("GET", "/api/services", "", "10.0.0.7")
	}
	cookie = nil
	if rec, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "10.0.0.7"); rec.Code != http.StatusOK {
		t.Fatalf("signing in after a stale cookie: %d", rec.Code)
	}
	cookie = nil

	if rec, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"wrong one entirely"}`, "10.0.0.1"); rec.Code != http.StatusUnauthorized || cookie != nil {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	if rec, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "10.0.0.1"); rec.Code != http.StatusOK || cookie == nil {
		t.Fatalf("sign in: %d", rec.Code)
	}
	if rec, _ := call("POST", "/api/auth/password", `{"current":"correct horse battery","new":"an even better password"}`, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("change password: %d", rec.Code)
	}

	// Scripts can still use the token.
	req := httptest.NewRequest("GET", "/api/services", nil)
	req.Header.Set("Authorization", "Bearer script-token")
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, req)
	if r.Code != http.StatusOK {
		t.Fatalf("token: %d", r.Code)
	}

	// Guessing passwords is throttled per client, even behind a local reverse proxy.
	cookie = nil
	for range 10 {
		call("POST", "/api/auth/login", `{"username":"jay","password":"guess guess guess"}`, "203.0.113.9")
	}
	if rec, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"an even better password"}`, "203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a guessing client should be locked out: %d", rec.Code)
	}
	if rec, _ := call("POST", "/api/auth/login", `{"username":"jay","password":"an even better password"}`, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("other clients are not: %d", rec.Code)
	}

	// Listening on every address is fine once sign-in is required.
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.Shutdown(t.Context())
}
