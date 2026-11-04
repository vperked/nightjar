package api_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/plugins"
	"nightjar/internal/services"
)

func TestPluginsAPI(t *testing.T) {
	m := newManaged(t)
	if code, _ := m.send(t, http.MethodGet, "/api/plugins", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("without plugins loaded: %d", code)
	}
	root := t.TempDir()
	os.Chmod(root, 0o755)
	dir := filepath.Join(root, "echo")
	os.Mkdir(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"echo","name":"Echo","provides":["check","card"],"run":"run.sh",
		"settings":[{"key":"TOKEN","label":"Token","type":"secret","required":true}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\nif [ \"$1\" = card ]; then echo '{\"items\":[{\"label\":\"Arg\",\"value\":\"none\"}]}'; else echo \"got $NIGHTJAR_ARGUMENT\"; fi\n"), 0o755)
	pl := plugins.New(root, m.store)
	pl.Scan()
	m.ctx.Plugins = pl
	services.PluginCheck = func(s services.Spec, id, arg string) services.Result {
		r := pl.RunCheck(context.Background(), id, arg)
		return services.Result{Up: r.Up, Detail: r.Detail, LatencyMS: r.LatencyMS}
	}
	t.Cleanup(func() { services.PluginCheck = nil })

	code, body := m.send(t, http.MethodGet, "/api/plugins", "")
	if list := body["plugins"].([]any); code != http.StatusOK || len(list) != 1 || list[0].(map[string]any)["enabled"] != false {
		t.Fatalf("list: %d %v", code, body)
	}
	// Trying it works while it is off; turning it on needs its token, which never comes back.
	if _, body = m.send(t, http.MethodPost, "/api/plugins/echo/try", `{"mode":"check","argument":"hello"}`); body["check"].(map[string]any)["detail"] != "got hello" {
		t.Fatalf("try: %v", body)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/plugins/echo", `{"enabled":true}`); code != http.StatusBadRequest {
		t.Fatalf("turned on without its token: %d", code)
	}
	code, body = m.send(t, http.MethodPut, "/api/plugins/echo", `{"enabled":true,"settings":{"TOKEN":"tok-123"}}`)
	if code != http.StatusOK || body["plugin"].(map[string]any)["enabled"] != true {
		t.Fatalf("turn on: %d %v", code, body)
	}
	if _, raw := m.send(t, http.MethodGet, "/api/plugins", ""); strings.Contains(fmt.Sprint(raw), "tok-123") {
		t.Fatal("the secret came back")
	}
	if code, _ = m.send(t, http.MethodPut, "/api/plugins/nope", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown plugin: %d", code)
	}

	// A check of type plugin runs it.
	if code, body = m.send(t, http.MethodPost, "/api/checks", `{"name":"echo","type":"plugin","target":"echo:world"}`); code != http.StatusCreated {
		t.Fatalf("add plugin check: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/checks", `{"name":"bad","type":"plugin","target":"Not An Id"}`); code != http.StatusBadRequest {
		t.Fatalf("accepted a bad plugin target: %d", code)
	}
	if r := services.Run(services.Spec{Type: "plugin", Target: "echo:world", Timeout: 5}); !r.Up || r.Detail != "got world" {
		t.Fatalf("run: %+v", r)
	}

	pl.Refresh(context.Background())
	if _, body = m.send(t, http.MethodGet, "/api/plugins/cards", ""); len(body["cards"].([]any)) != 1 {
		t.Fatalf("cards: %v", body)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/plugins/echo", `{}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site change: %d", code)
	}
}
