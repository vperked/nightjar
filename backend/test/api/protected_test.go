package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

// public are the routes anyone may call: the page's own config, health, the status page, signing in,
// and the endpoints with their own credential (a heartbeat's token, Discord's signature).
var public = map[string]bool{
	"GET /health":                true,
	"GET /api/config":            true,
	"GET /api/status":            true,
	"POST /api/auth/setup":       true,
	"POST /api/auth/login":       true,
	"POST /api/auth/logout":      true,
	"POST /discord/interactions": true,
}

// Every route the server has, read from its source, so a new one cannot be added unguarded.
func routes(t *testing.T) []string {
	src, err := os.ReadFile("../../internal/api/server.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\("((?:GET|POST|PUT|DELETE) [^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	if len(out) < 90 {
		t.Fatalf("found only %d routes; has the way they are written changed?", len(out))
	}
	return out
}

func TestEveryRouteButThePublicOnesNeedsSigningIn(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	ctx := &monitor.Context{Cfg: config.Settings{StatusPageEnabled: true}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "0.0.0.0", Port: "0", Accounts: acc, AuthToken: "script-token-script-token", Store: db, FrontendDir: t.TempDir()}, ctx)
	param := regexp.MustCompile(`\{[a-z]+\}`)

	checked := 0
	for i, route := range routes(t) {
		if public[route] {
			continue
		}
		method, pattern, _ := strings.Cut(route, " ")
		path := strings.TrimSuffix(param.ReplaceAllString(pattern, "x1"), "{$}")
		for _, who := range []struct{ name, header string }{{"nobody", ""}, {"a forged session", "nightjar_session=forged"}} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			req.RemoteAddr = fmt.Sprintf("10.1.%d.%d:1234", i/250, i%250)
			req.Header.Set("Content-Type", "application/json")
			if who.header != "" {
				req.Header.Set("Cookie", who.header)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s as %s: %d, want 401", route, who.name, rec.Code)
			}
		}
		checked++
	}
	if checked < 85 {
		t.Fatalf("checked only %d routes", checked)
	}

	// A wrong API token is turned away, and guessing is soon locked out.
	codes := map[int]int{}
	for range 12 {
		req := httptest.NewRequest(http.MethodGet, "/api/services", nil)
		req.RemoteAddr = "10.9.9.9:1234"
		req.Header.Set("Authorization", "Bearer guessing")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		codes[rec.Code]++
	}
	if codes[http.StatusOK] != 0 || codes[http.StatusTooManyRequests] == 0 {
		t.Fatalf("token guessing: %v", codes)
	}
}

func TestSignedOutConfigSaysOnlyWhatSigningInNeeds(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "c.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery staple", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	ctx := &monitor.Context{Cfg: config.Settings{StatusPageEnabled: true, HiddenSections: []string{"network"}}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "0.0.0.0", Port: "0", Accounts: acc, AuthToken: "script-token-script-token", Store: db, FrontendDir: t.TempDir(), Version: "1.2.3"}, ctx)
	get := func(token string) (map[string]any, http.Header) {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out, rec.Header()
	}
	out, h := get("")
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"jay", "1.2.3", "network", "recovery_left", "plex_enabled"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("signed out, config gives away %q: %s", secret, raw)
		}
	}
	if acct := out["account"].(map[string]any); acct["configured"] != true {
		t.Fatalf("it still says the account exists: %s", raw)
	}
	if v := h.Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("no cross-origin reads unless a site is named: %q", v)
	}
	in, _ := get("script-token-script-token")
	if in["version"] != "1.2.3" || in["account"].(map[string]any)["username"] != "jay" {
		t.Fatalf("signed in, it has everything: %v", in)
	}
}
