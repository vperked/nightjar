package api_test

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"servermonitor/internal/api"
	"servermonitor/internal/collect"
	"servermonitor/internal/config"
	"servermonitor/internal/monitor"
	"servermonitor/internal/services"
)

func newServer(t *testing.T, token string, statusPage bool) (*api.Server, *monitor.Context) {
	ctx := &monitor.Context{
		Cfg:       config.Settings{StatusPageEnabled: statusPage, StatusPageTitle: "Status"},
		Processes: collect.NewProcessScanner(),
		Sensors:   collect.NewSensors(),
		GPU:       &collect.GPUReader{},
		Bandwidth: &collect.BandwidthMeter{},
	}
	return api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: token, AllowedOrigin: "null", FrontendDir: t.TempDir()}, ctx), ctx
}

func get(t *testing.T, s *api.Server, path string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "10.0.0.1:1234"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestProtectedEndpointsNeedToken(t *testing.T) {
	s, _ := newServer(t, "secret", false)
	for _, path := range []string{"/api/stats", "/api/services", "/api/services/history?name=x"} {
		if rec, _ := get(t, s, path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", path, rec.Code)
		}
	}
	if rec, _ := get(t, s, "/api/services", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Error("wrong token accepted")
	}
	if rec, _ := get(t, s, "/api/services", map[string]string{"Authorization": "Bearer secret"}); rec.Code != http.StatusOK {
		t.Error("bearer token rejected")
	}
	if rec, _ := get(t, s, "/api/services", map[string]string{"X-Auth-Token": "secret"}); rec.Code != http.StatusOK {
		t.Error("X-Auth-Token rejected")
	}
}

func TestRepeatedFailuresAreThrottled(t *testing.T) {
	s, _ := newServer(t, "secret", false)
	// Asking without credentials (a signed-out browser) does not count.
	for range 20 {
		get(t, s, "/api/stats", nil)
	}
	if rec, _ := get(t, s, "/api/stats", map[string]string{"X-Auth-Token": "secret"}); rec.Code != http.StatusOK {
		t.Fatalf("requests without credentials should not lock the client out, got %d", rec.Code)
	}
	// Ten wrong tokens a minute lock the client out, even with the right one.
	for range 10 {
		get(t, s, "/api/stats", map[string]string{"X-Auth-Token": "guess"})
	}
	if rec, _ := get(t, s, "/api/stats", map[string]string{"X-Auth-Token": "secret"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestHealthIsPublicButTerseWithoutToken(t *testing.T) {
	s, _ := newServer(t, "secret", false)
	_, body := get(t, s, "/health", nil)
	if _, ok := body["checks"]; ok || body["status"] == nil {
		t.Fatalf("unauthenticated health leaked details: %v", body)
	}
	_, body = get(t, s, "/health", map[string]string{"X-Auth-Token": "secret"})
	if _, ok := body["checks"]; !ok {
		t.Fatalf("authenticated health missing checks: %v", body)
	}
}

func TestStatsShape(t *testing.T) {
	s, _ := newServer(t, "", false)
	rec, body := get(t, s, "/api/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	for _, k := range []string{"system", "cpu", "memory", "disk", "network", "processes"} {
		if _, ok := body[k]; !ok {
			t.Errorf("stats missing %s", k)
		}
	}
}

func TestServicesAndHistory(t *testing.T) {
	s, ctx := newServer(t, "", false)
	if _, body := get(t, s, "/api/services", nil); len(body["services"].([]any)) != 0 {
		t.Fatal("no checks yet")
	}
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "api", Type: "tcp", Target: "h:1", Interval: 60}}, nil)
	lat := 12.0
	ctx.Services.Record("api", services.Result{Up: true, LatencyMS: &lat}, now()-30)
	_, body := get(t, s, "/api/services", nil)
	rows := body["services"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["status"] != "up" {
		t.Fatalf("got %v", body)
	}
	if rec, _ := get(t, s, "/api/services/history?name=nope", nil); rec.Code != http.StatusNotFound {
		t.Fatal("unknown service should 404")
	}
	if rec, _ := get(t, s, "/api/services/history?name=api&hours=abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatal("bad hours should 400")
	}
	_, body = get(t, s, "/api/services/history?name=api&hours=1", nil)
	if pts := body["points"].([]any); len(pts) != 1 {
		t.Fatalf("points %v", pts)
	}
}

func TestStatusPageIsPublicAndHidesTargets(t *testing.T) {
	s, _ := newServer(t, "secret", false)
	if rec, _ := get(t, s, "/api/status", nil); rec.Code != http.StatusNotFound {
		t.Fatal("disabled status page should 404")
	}
	s, ctx := newServer(t, "secret", true)
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "db", Type: "tcp", Target: "10.0.0.5:5432", Interval: 60}}, nil)
	ctx.Services.Record("db", services.Result{Detail: "connect failed: secret internal detail"}, now())
	ctx.Services.Record("db", services.Result{Detail: "connect failed"}, now())
	ctx.Services.Record("db", services.Result{Detail: "connect failed"}, now())
	rec, body := get(t, s, "/api/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status page should be public, got %d", rec.Code)
	}
	raw := rec.Body.String()
	for _, leak := range []string{"10.0.0.5", "secret internal", "target", "detail"} {
		if contains(raw, leak) {
			t.Errorf("status page leaked %q: %s", leak, raw)
		}
	}
	if body["overall"] != "outage" {
		t.Fatalf("overall %v", body["overall"])
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func TestFrontendFallsBackToIndex(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644)
	_, ctx := newServer(t, "", false)
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AllowedOrigin: "null", FrontendDir: dir}, ctx)
	rec, _ := get(t, s, "/status", nil)
	if rec.Code != http.StatusOK || !contains(rec.Body.String(), "app") {
		t.Fatalf("SPA fallback failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := get(t, s, "/../../etc/passwd", nil); contains(rec.Body.String(), "root:") {
		t.Fatal("path traversal")
	}
	if rec, _ := get(t, s, "/api/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatal("unknown API route should 404, not the SPA")
	}
}

func TestRefusesPublicBindWithoutToken(t *testing.T) {
	s := api.New(api.Config{Host: "0.0.0.0", Port: "0"}, &monitor.Context{})
	if err := s.Start(); err != api.ErrPublicWithoutToken {
		t.Fatalf("got %v", err)
	}
}

func TestGzip(t *testing.T) {
	s, _ := newServer(t, "", false)
	req := httptest.NewRequest("GET", "/api/config", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("JSON should be gzipped, headers %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(zr).Decode(&body); err != nil || body["auth_required"] != false {
		t.Fatalf("decoded %v %v", body, err)
	}

	plain := httptest.NewRecorder()
	s.Handler().ServeHTTP(plain, httptest.NewRequest("GET", "/api/config", nil))
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatal("clients without gzip get plain responses")
	}
}
