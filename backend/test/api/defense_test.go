package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/ddos"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

func TestDefendingIsSwitchedAndAddressesUnblockedFromTheDashboard(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	t.Cleanup(db.Close)
	var ran []string
	exec := func(_ context.Context, _ []byte, name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "-C" {
			return "", context.Canceled
		}
		return "", nil
	}
	d := &ddos.Defender{On: func() bool { return db.Meta(ddos.DefendKey) != "off" }, Exec: exec}
	w := &ddos.Watcher{Defender: d}
	d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: []string{ddos.ConnectionFlood}, Sources: []ddos.Source{{Address: "203.0.113.7", Established: 500}}, Interval: 5})
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Attacks: w}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", Store: db, FrontendDir: t.TempDir()}, ctx)
	call := func(method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	defense := func(out map[string]any) map[string]any {
		return out["attacks"].(map[string]any)["defense"].(map[string]any)
	}

	code, out := call("GET", "/api/attacks", "")
	if code != http.StatusOK || defense(out)["on"] != true || len(defense(out)["blocked"].([]any)) != 1 {
		t.Fatalf("view: %d %v", code, out)
	}
	if code, _ := call("DELETE", "/api/attacks/blocked/198.51.100.1", ""); code != http.StatusNotFound {
		t.Fatalf("an address not blocked: %d", code)
	}
	code, out = call("DELETE", "/api/attacks/blocked/203.0.113.7", "")
	if code != http.StatusOK || len(defense(out)["blocked"].([]any)) != 0 {
		t.Fatalf("unblock: %d %v", code, out)
	}

	if code, _ := call("PUT", "/api/attacks/defense", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no answer: %d", code)
	}
	code, out = call("PUT", "/api/attacks/defense", `{"on": false}`)
	if code != http.StatusOK || defense(out)["on"] != false || db.Meta(ddos.DefendKey) != "off" {
		t.Fatalf("off: %d %v", code, out)
	}
	// Changes are written to the activity log.
	time.Sleep(10 * time.Millisecond)
	acts := db.Activities(10, 0)
	words := []string{}
	for _, a := range acts {
		words = append(words, a.Action)
	}
	if !strings.Contains(strings.Join(words, "|"), "Switched defending against attacks") || !strings.Contains(strings.Join(words, "|"), "Unblocked 203.0.113.7") {
		t.Fatalf("activity: %v", words)
	}
}
