package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"servermonitor/internal/api"
	"servermonitor/internal/config"
	"servermonitor/internal/monitor"
	"servermonitor/internal/storage"
	"servermonitor/internal/updates"
)

// updatesServer is an API whose update manager runs no real commands.
func updatesServer(t *testing.T, token string) http.Handler {
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	run := func(context.Context, updates.Command) (updates.Output, error) {
		return updates.Output{Stdout: "1.0.0"}, nil
	}
	ctx := &monitor.Context{
		Cfg:     config.LoadFrom(config.MapLookup(map[string]string{"SYSTEM_UPDATE_ENABLED": "true"})),
		Updates: updates.New(updates.Options{Store: store, Run: run, GitHubAPI: "http://127.0.0.1:1"}, nil, false),
	}
	srv := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: token, AllowedOrigin: "null", FrontendDir: t.TempDir(), Store: store,
		Detect: func() []updates.Spec {
			return []updates.Spec{{ID: "os", Name: "OS packages", Kind: updates.KindSystem}, {ID: "tool", Name: "Tool", Kind: updates.KindRelease, Repo: "a/b", VersionCommand: "tool -v"}}
		}}, ctx)
	return srv.Handler()
}

func call(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestUpdatesCommandsNeedAToken(t *testing.T) {
	open := updatesServer(t, "")
	code, body := call(t, open, http.MethodPost, "/api/updates", `{"name":"Evil","kind":"release","repo":"a/b","version_command":"curl x | sh"}`)
	if code != http.StatusForbidden || !strings.Contains(body["error"].(string), "WEB_AUTH_TOKEN") {
		t.Fatalf("a token-less dashboard must not choose commands: %d %v", code, body)
	}
	if code, body := call(t, open, http.MethodPost, "/api/updates", `{"name":"Plex","kind":"apt","package":"plexmediaserver"}`); code != http.StatusCreated {
		t.Fatalf("targets without commands can be added: %d %v", code, body)
	}
	// Detected targets bring their own commands; toggling auto-update keeps them.
	if code, body := call(t, open, http.MethodPost, "/api/updates/detect", `{}`); code != http.StatusOK || len(body["added"].([]any)) != 2 {
		t.Fatalf("detect: %d %v", code, body)
	}
	if code, body := call(t, open, http.MethodPut, "/api/updates/tool", `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v","auto":true}`); code != http.StatusOK || body["target"].(map[string]any)["auto"] != true {
		t.Fatalf("toggle auto: %d %v", code, body)
	}
	if code, _ := call(t, open, http.MethodPut, "/api/updates/tool", `{"id":"tool","name":"Tool","kind":"release","repo":"a/b","version_command":"rm -rf /"}`); code != http.StatusForbidden {
		t.Fatalf("changing a command without a token: %d", code)
	}

	locked := updatesServer(t, "tok")
	if code, body := call(t, locked, http.MethodPost, "/api/updates", `{"name":"Tool","kind":"release","repo":"a/b","version_command":"tool -v","update_command":"tool upgrade"}`); code != http.StatusCreated {
		t.Fatalf("with a token, commands are editable: %d %v", code, body)
	}
}

func TestUpdatesEndpoints(t *testing.T) {
	h := updatesServer(t, "tok")
	call(t, h, http.MethodPost, "/api/updates/detect", `{}`)
	code, body := call(t, h, http.MethodGet, "/api/updates", "")
	if code != http.StatusOK || body["enabled"] != true || body["commands_editable"] != true || len(body["targets"].([]any)) != 2 {
		t.Fatalf("list: %d %v", code, body)
	}
	if w := body["window"].(map[string]any); w["label"] != "04:00–06:00" {
		t.Fatalf("window: %v", w)
	}
	if code, _ := call(t, h, http.MethodPost, "/api/updates/check", `{}`); code != http.StatusAccepted {
		t.Fatalf("check all: %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/api/updates/nope/update", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown target: %d", code)
	}
	// A release target without an update command is only checked.
	if code, body := call(t, h, http.MethodPost, "/api/updates/tool/update", `{}`); code != http.StatusBadRequest {
		t.Fatalf("check-only target: %d %v", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, _ := call(t, h, http.MethodDelete, "/api/updates/tool", "")
		if code == http.StatusNoContent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delete: %d", code)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, body := call(t, h, http.MethodGet, "/api/updates", ""); len(body["targets"].([]any)) != 1 {
		t.Fatalf("after delete: %v", body["targets"])
	}
}
