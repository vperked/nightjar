package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"servermonitor/internal/units"
)

func TestUnitRoutes(t *testing.T) {
	m := newManaged(t)
	var mu sync.Mutex
	var calls []string
	u := units.New()
	u.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		mu.Unlock()
		switch {
		case name == "journalctl":
			return []byte("line one\nline two\n"), nil
		case args[0] == "list-units":
			return []byte(`[{"unit":"sonarr.service","load":"loaded","active":"active"},{"unit":"server-monitor.service","load":"loaded","active":"active"}]`), nil
		case args[0] == "show":
			var out []string
			for _, n := range args[3:] {
				out = append(out, "Id="+n+"\nActiveState=active\nSubState=running\nUnitFileState=enabled\nNRestarts=0")
			}
			return []byte(strings.Join(out, "\n\n")), nil
		}
		return nil, nil
	}
	u.Poll(context.Background(), time.Now(), nil, 3)
	m.ctx.Units = u
	m.ctx.Cfg.SystemdServiceName, m.ctx.Cfg.ServiceWatchEnabled = "server-monitor", true

	code, body := m.send(t, http.MethodGet, "/api/units", "")
	if code != http.StatusOK || len(body["units"].([]any)) != 2 || body["self"] != "server-monitor.service" {
		t.Fatalf("list: %d %v", code, body)
	}
	if code, body = m.send(t, http.MethodGet, "/api/units/sonarr.service/logs?lines=5", ""); code != http.StatusOK || len(body["lines"].([]any)) != 2 {
		t.Fatalf("logs: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodGet, "/api/units/nope.service/logs", ""); code != http.StatusNotFound {
		t.Fatalf("unknown logs: %d", code)
	}
	if code, body = m.send(t, http.MethodPost, "/api/units/sonarr.service/restart", `{}`); code != http.StatusOK || body["unit"].(map[string]any)["active"] != "active" {
		t.Fatalf("restart: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/units/server-monitor.service/stop", `{}`); code != http.StatusBadRequest {
		t.Fatalf("stopping the monitor from its own dashboard: %d", code)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/units/sonarr.service/restart", `{}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site restart: %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(calls, "|"), "systemctl restart -- sonarr.service") || strings.Contains(strings.Join(calls, "|"), "stop -- server-monitor") {
		t.Fatalf("calls %v", calls)
	}
}
