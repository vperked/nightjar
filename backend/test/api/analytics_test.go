package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

func TestAnalyticsReadsTheMinutesKept(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	now := float64(time.Now().Unix()/60) * 60
	cpu := 30.0
	var rows []storage.Sample
	for m := 1; m <= 120; m++ {
		rows = append(rows, storage.Sample{Host: "local", TS: now - float64(m*60), Up: 1, CPU: &cpu, CPUMax: &cpu})
	}
	db.AddSamples(rows)
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "script-token", Store: db, FrontendDir: t.TempDir()}, ctx)
	get := func(path string) (int, map[string]any) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer script-token")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, body := get("/api/analytics?range=24h")
	hosts, _ := body["hosts"].([]any)
	if code != http.StatusOK || len(hosts) != 1 || body["step"] != 300.0 {
		t.Fatalf("all servers: %d %v", code, body)
	}
	local := hosts[0].(map[string]any)
	sum := local["summary"].(map[string]any)
	if local["id"] != "local" || local["kind"] != "local" || sum["cpu_avg"] != 30.0 || sum["uptime"] != nil || len(local["points"].([]any)) < 24 {
		t.Fatalf("this server: %v", local)
	}
	if code, body := get("/api/analytics/local?range=1h"); code != http.StatusOK || len(body["host"].(map[string]any)["points"].([]any)) != 59 {
		t.Fatalf("one server over an hour: %d %v", code, body)
	}
	if code, _ := get("/api/analytics/nope"); code != http.StatusNotFound {
		t.Fatalf("an unknown server: %d", code)
	}
}
