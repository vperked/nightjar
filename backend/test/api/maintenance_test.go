package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"nightjar/internal/maintenance"
)

func TestMaintenanceAndPublicNotices(t *testing.T) {
	m := newManaged(t)
	m.ctx.Maintenance = maintenance.New(m.store)
	m.ctx.Services.Resting = func(name string, now float64) bool {
		_, on := m.ctx.Maintenance.For(name, time.Unix(int64(now), 0))
		return on
	}
	m.ctx.Cfg.StatusPageEnabled = true
	if code, _ := m.send(t, http.MethodPost, "/api/checks", `{"name":"nas","type":"tcp","target":"127.0.0.1:1"}`); code != http.StatusCreated {
		t.Fatal("add check")
	}
	now := time.Now().Unix()
	code, body := m.send(t, http.MethodPost, "/api/maintenance", fmt.Sprintf(`{"title":"Updating the NAS","checks":["nas"],"start":%d,"end":%d,"public":true,"note":"Back by three."}`, now-60, now+3600))
	if code != http.StatusCreated {
		t.Fatalf("add window: %d %v", code, body)
	}
	id := body["window"].(map[string]any)["id"].(string)
	if code, _ := m.send(t, http.MethodPost, "/api/maintenance", `{"title":"bad","start":10,"end":5}`); code != http.StatusBadRequest {
		t.Fatalf("bad window: %d", code)
	}

	// The check rests: it is not due to run, and shows as in maintenance.
	if due := m.ctx.Services.Claim(float64(now)); len(due) != 0 {
		t.Fatalf("a check in maintenance does not run: %v", due)
	}
	_, svcs := m.send(t, http.MethodGet, "/api/services", "")
	if st := svcs["services"].([]any)[0].(map[string]any)["status"]; st != "maintenance" {
		t.Fatalf("status %v", st)
	}
	_, pub := m.send(t, http.MethodGet, "/api/status", "")
	win := pub["maintenance"].([]any)
	if len(win) != 1 || win[0].(map[string]any)["on"] != true || win[0].(map[string]any)["covers"].([]any)[0] != "nas" {
		t.Fatalf("the status page shows it: %v", pub["maintenance"])
	}
	if len(pub["incidents"].([]any)) != 0 {
		t.Fatalf("no notices yet: %v", pub["incidents"])
	}
	if code, _ := m.send(t, http.MethodDelete, "/api/maintenance/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := m.send(t, http.MethodPut, "/api/incidents/12345/notice", `{"title":"x"}`); code != http.StatusNotFound {
		t.Fatalf("no such incident: %d", code)
	}
	if code, body := m.send(t, http.MethodGet, "/api/report", ""); code != http.StatusOK || body["at"] != "Mon 09:00" {
		t.Fatalf("report: %d %v", code, body)
	}
	if code, body := m.send(t, http.MethodGet, "/api/docker/containers", ""); code != http.StatusOK || body["available"] != false {
		t.Fatalf("no docker in tests: %d %v", code, body)
	}
}
