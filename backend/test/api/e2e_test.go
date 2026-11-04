package api_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nightjar/internal/alerts"
	"nightjar/internal/api"
	"nightjar/internal/cleanup"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/metrics"
	"nightjar/internal/monitor"
	"nightjar/internal/plex"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// TestLoopToAPI runs real monitoring cycles against local services and reads the results back through the API.
func TestLoopToAPI(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer upstream.Close()
	pms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/sessions":
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"sessionKey":"7","type":"movie","title":"Heat","User":{"title":"sam"},"Session":{"id":"s7","bandwidth":8000}}]}}`))
		case "/status/sessions/terminate":
			if r.URL.Query().Get("sessionId") != "s7" || r.URL.Query().Get("reason") != "Bedtime" {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/":
			w.Write([]byte(`{"MediaContainer":{"friendlyName":"nas","version":"1.40.0"}}`))
		default:
			w.Write([]byte(`{"MediaContainer":{}}`))
		}
	}))
	defer pms.Close()
	plexClient, _ := plex.New(pms.URL, "tok", true, 2*time.Second)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()

	dir := t.TempDir()
	cfg := config.Settings{
		CPUThreshold: 100, MemoryThreshold: 100, GPUThreshold: 100, DiskThreshold: 100, DiskEarlyWarningThreshold: 99,
		DiskCleanupThreshold: 100, DiskAggressiveCleanupThreshold: 100, DiskEmergencyThreshold: 100,
		TempThreshold: 1000, NetworkBandwidthThreshold: 1e9, ProcessCPUThreshold: 100, ProcessMemoryThreshold: 100,
		CheckInterval: 1, AlertCooldown: 300, LogCheckInterval: 1000, HistoryRetentionDays: 90, MetricsReportInterval: 3600,
	}
	store := storage.Open(filepath.Join(dir, "h.db"))
	defer store.Close()
	logs := cleanup.NewLogMonitor(filepath.Join(dir, "app.log"), 100, 7)
	ctx := &monitor.Context{
		Cfg: cfg, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{},
		Bandwidth: &collect.BandwidthMeter{}, Logs: logs, Cleaner: &cleanup.Cleaner{Logs: logs},
		Metrics: metrics.New(), Gates: alerts.NewGates(300, 3600, 2, 300),
		Services: services.NewMonitor([]services.Spec{
			{Name: "web", Type: "http", Target: upstream.URL, Interval: 5, Timeout: 2, Retries: 0, VerifyTLS: true},
			{Name: "port", Type: "tcp", Target: ln.Addr().String(), Interval: 5, Timeout: 2},
			{Name: "closed", Type: "tcp", Target: "127.0.0.1:1", Interval: 5, Timeout: 2, Retries: 0},
		}, store),
		Plex: plex.NewMonitor(plexClient, 15, 2),
	}
	ctx.Cfg.StatusPageEnabled, ctx.Cfg.StatusPageTitle = true, "Status"
	loop := monitor.NewLoop(ctx)
	loop.RunCycle()
	loop.Wait()
	loop.RunCycle()
	loop.Wait()

	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", AllowedOrigin: "null", FrontendDir: dir}, ctx)
	auth := map[string]string{"X-Auth-Token": "tok"}

	_, body := get(t, s, "/api/services", auth)
	got := map[string]string{}
	for _, row := range body["services"].([]any) {
		r := row.(map[string]any)
		got[r["name"].(string)] = r["status"].(string)
	}
	want := map[string]string{"web": "up", "port": "up", "closed": "down"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}

	rec, body := get(t, s, "/api/stats", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats %d", rec.Code)
	}
	if body["memory"].(map[string]any)["total"].(float64) <= 0 {
		t.Fatal("stats should come from the loop's snapshot")
	}
	h := body["history"].([]any)
	if len(h) != 2 {
		t.Fatalf("expected one history sample per cycle, got %d", len(h))
	}
	// ?since= returns only newer samples, so the dashboard's polls stay small.
	first := strconv.FormatFloat(h[0].(map[string]any)["ts"].(float64), 'f', -1, 64)
	if _, body := get(t, s, "/api/stats?since="+first, auth); len(body["history"].([]any)) != 1 {
		t.Fatalf("since the first sample: %v", body["history"])
	}
	for _, list := range []string{"top_cpu", "top_memory"} {
		for _, p := range body["processes"].(map[string]any)[list].([]any) {
			if _, ok := p.(map[string]any)["cmdline"]; ok {
				t.Fatal("command lines stay out of the dashboard payload")
			}
		}
	}

	_, body = get(t, s, "/api/status", nil)
	if body["overall"] != "degraded" {
		t.Fatalf("overall %v", body["overall"])
	}
	_, body = get(t, s, "/api/plex", auth)
	px := body["plex"].(map[string]any)
	if px["status"] != "up" || px["direct"].(float64) != 1 || px["bandwidth_kbps"].(float64) != 8000 {
		t.Fatalf("plex %v", px)
	}
	if px["server"].(map[string]any)["name"] != "nas" {
		t.Fatalf("plex server %v", px["server"])
	}
	if rec, _ := get(t, s, "/api/plex", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("plex must require auth, got %d", rec.Code)
	}

	// Stopping a stream: the message reaches Plex, and the stream leaves the list at once.
	h2 := s.Handler()
	if code, _ := call(t, h2, http.MethodPost, "/api/plex/streams/7/stop", `{"message":"`+strings.Repeat("x", 201)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("an overlong message should be refused, got %d", code)
	}
	if code, body := call(t, h2, http.MethodPost, "/api/plex/streams/7/stop", `{"message":"Bedtime"}`); code != http.StatusOK || len(body["plex"].(map[string]any)["streams"].([]any)) != 0 {
		t.Fatalf("stop: %d %v", code, body)
	}
	if code, _ := call(t, h2, http.MethodPost, "/api/plex/streams/7/stop", `{}`); code != http.StatusNotFound {
		t.Fatalf("stopping an ended stream should be 404, got %d", code)
	}

	_, body = get(t, s, "/api/services/history?name=web&hours=1", auth)
	if pts := body["points"].([]any); len(pts) != 1 {
		t.Fatalf("history should be persisted, got %d points", len(pts))
	}
}
