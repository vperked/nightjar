package api_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/discord"
	"nightjar/internal/monitor"
	"nightjar/internal/notify"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

type managed struct {
	srv      *api.Server
	ctx      *monitor.Context
	store    *storage.Store
	restarts atomic.Int32
}

func newManaged(t *testing.T) *managed {
	m := &managed{store: storage.Open(":memory:")}
	t.Cleanup(m.store.Close)
	cfg := config.LoadFrom(config.MapLookup(map[string]string{"PLEX_TOKEN": "hunter2"}))
	ctx := &monitor.Context{Cfg: cfg, Services: services.NewMonitor(nil, m.store), Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors()}
	m.ctx = ctx
	m.srv = api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", AllowedOrigin: "null", FrontendDir: t.TempDir(),
		Store: m.store, Restart: func() { m.restarts.Add(1) }}, ctx)
	return m
}

func (m *managed) send(t *testing.T, method, path, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "monitor.local"
	req.Header.Set("X-Auth-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	m.srv.Handler().ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSettingsRoundTrip(t *testing.T) {
	m := newManaged(t)
	code, body := m.send(t, http.MethodGet, "/api/settings", "")
	if code != http.StatusOK {
		t.Fatalf("settings %d", code)
	}
	fields := map[string]map[string]any{}
	for _, g := range body["groups"].([]any) {
		for _, f := range g.(map[string]any)["fields"].([]any) {
			field := f.(map[string]any)
			fields[field["key"].(string)] = field
		}
	}
	if token := fields["PLEX_TOKEN"]; token["value"] != "" || token["set"] != true {
		t.Fatalf("secrets report only that they are set: %v", token)
	}
	if cpu := fields["CPU_THRESHOLD"]; cpu["value"] != "80" || cpu["source"] != "default" {
		t.Fatalf("cpu %v", cpu)
	}
	if host := fields["WEB_HOST"]; host["startup"] != true {
		t.Fatalf("startup settings are marked read-only: %v", host)
	}

	code, body = m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"abc","WEB_HOST":"0.0.0.0"}}`)
	if errs, _ := body["fields"].(map[string]any); code != http.StatusBadRequest || errs["CPU_THRESHOLD"] == nil || errs["WEB_HOST"] == nil {
		t.Fatalf("invalid settings: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"90","PLEX_TOKEN":"new"}}`); code != http.StatusAccepted {
		t.Fatalf("save %d", code)
	}
	if saved := m.store.Settings(); saved["CPU_THRESHOLD"] != "90" || saved["PLEX_TOKEN"] != "new" {
		t.Fatalf("saved %v", saved)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":""}}`); code != http.StatusOK {
		t.Fatal("an empty value resets a setting")
	}
	if _, kept := m.store.Settings()["CPU_THRESHOLD"]; kept {
		t.Fatal("reset should remove the saved value")
	}
}

func TestLiveSettingsApplyWithoutRestart(t *testing.T) {
	m := newManaged(t)
	code, body := m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"91","SEEDING_RATIO_LIMIT":"3"}}`)
	if code != http.StatusOK || body["restarting"] != false || m.restarts.Load() != 0 {
		t.Fatalf("live settings: %d %v, restarts %d", code, body, m.restarts.Load())
	}
	if c := m.ctx.Config(); c.CPUThreshold != 91 || c.SeedingRatioLimit != 3 {
		t.Fatalf("the new values are in force for the next cycle: %v %v", c.CPUThreshold, c.SeedingRatioLimit)
	}
	// Saving a setting's current value changes nothing, so it needs no restart either.
	if code, _ := m.send(t, http.MethodPut, "/api/settings", `{"values":{"PLEX_URL":"http://localhost:32400"}}`); code != http.StatusOK || m.restarts.Load() != 0 {
		t.Fatalf("unchanged value: %d, restarts %d", code, m.restarts.Load())
	}
	code, body = m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"92","PLEX_URL":"http://10.0.0.2:32400"}}`)
	if code != http.StatusAccepted || body["restarting"] != true || fmt.Sprint(body["restart_for"]) != "[PLEX_URL]" {
		t.Fatalf("a connection change restarts: %d %v", code, body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for m.restarts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if m.restarts.Load() != 1 {
		t.Fatal("restart was not requested")
	}
}

func TestWritesNeedSameOriginJSON(t *testing.T) {
	m := newManaged(t)
	if code, _ := m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"90"}}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-origin write: %d", code)
	}
	if code, _ := m.send(t, http.MethodPost, "/api/checks", `{"name":"x","target":"https://x"}`, "Content-Type", "text/plain"); code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON write: %d", code)
	}
	if code, _ := m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"90"}}`, "X-Auth-Token", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("write without the token: %d", code)
	}
	if code, _ := m.send(t, http.MethodPut, "/api/settings", `{"values":{"CPU_THRESHOLD":"90"}}`, "Origin", "http://monitor.local"); code != http.StatusOK {
		t.Fatalf("same-origin write: %d", code)
	}
}

func TestCheckEndpoints(t *testing.T) {
	m := newManaged(t)
	code, body := m.send(t, http.MethodPost, "/api/checks", `{"name":"web","type":"http","target":"https://example.com","interval":30}`)
	if code != http.StatusCreated || body["check"].(map[string]any)["interval"].(float64) != 30 {
		t.Fatalf("create: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/checks", `{"name":"web","target":"https://example.com"}`); code != http.StatusConflict {
		t.Fatalf("duplicate: %d", code)
	}
	if code, body = m.send(t, http.MethodPost, "/api/checks", `{"name":"bad","type":"http","target":"example.com"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid target: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/checks/web", `{"name":"site","type":"http","target":"https://example.com","paused":true}`); code != http.StatusOK {
		t.Fatalf("update: %d", code)
	}
	_, body = m.send(t, http.MethodGet, "/api/checks", "")
	if checks := body["checks"].([]any); len(checks) != 1 || checks[0].(map[string]any)["name"] != "site" {
		t.Fatalf("list: %v", body)
	}
	if code, _ = m.send(t, http.MethodDelete, "/api/checks/site", "", "Content-Type", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ = m.send(t, http.MethodDelete, "/api/checks/site", ""); code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", code)
	}
}

func TestAlertTestEndpoint(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer hook.Close()
	m := newManaged(t)
	ch, _ := notify.NewWebhook(hook.URL, discord.Origin{})
	m.ctx.Channels = []notify.Channel{ch}
	code, body := m.send(t, http.MethodPost, "/api/alerts/test", "{}")
	results := body["results"].([]any)
	if code != http.StatusOK || len(results) != 1 || results[0].(map[string]any)["ok"] != true {
		t.Fatalf("test alerts: %d %v", code, body)
	}
	if code, body := m.send(t, http.MethodPost, "/api/alerts/test", "{}"); code != http.StatusTooManyRequests {
		t.Fatalf("a second test straight away should be refused, got %d %v", code, body)
	}
}

func TestTestCheckRunsWithoutSaving(t *testing.T) {
	m := newManaged(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := ln.Addr().String()
	ln.Close()
	closed := open // nothing listens there now
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	open = ln.Addr().String()

	code, body := m.send(t, http.MethodPost, "/api/checks/test", `{"name":"db","type":"tcp","target":"`+open+`","timeout":2}`)
	if r := body["result"].(map[string]any); code != http.StatusOK || r["up"] != true || r["latency_ms"] == nil {
		t.Fatalf("open port: %d %v", code, body)
	}
	code, body = m.send(t, http.MethodPost, "/api/checks/test", `{"name":"db","type":"tcp","target":"`+closed+`","timeout":2}`)
	if r := body["result"].(map[string]any); code != http.StatusOK || r["up"] != false || r["detail"] == "" {
		t.Fatalf("closed port: %d %v", code, body)
	}
	if len(m.ctx.Services.Specs()) != 0 {
		t.Fatal("a test does not save the check")
	}
	if code, _ := m.send(t, http.MethodPost, "/api/checks/test", `{"name":"x","type":"tcp","target":"`+open+`"}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("tests are changes too, guarded against other sites: %d", code)
	}
}
