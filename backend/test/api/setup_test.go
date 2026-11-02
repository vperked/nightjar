package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"servermonitor/internal/setup"
)

func fakePlexToken(t *testing.T, token string) {
	dir := t.TempDir()
	prefs := filepath.Join(dir, "Preferences.xml")
	os.WriteFile(prefs, []byte(`<Preferences PlexOnlineToken="`+token+`"/>`), 0o600)
	old, oldHomes, oldProbe, oldURL := setup.PlexPreferences, setup.HomeDirs, setup.Probe, setup.PlexURL
	t.Cleanup(func() {
		setup.PlexPreferences, setup.HomeDirs, setup.Probe, setup.PlexURL = old, oldHomes, oldProbe, oldURL
	})
	setup.PlexPreferences = []string{prefs}
	setup.HomeDirs = func() []string { return []string{dir} }
	setup.Probe = func(int) bool { return false }
	setup.PlexURL = "http://127.0.0.1:1"
}

func TestSetupNeverSendsThePlexToken(t *testing.T) {
	fakePlexToken(t, "super-secret-token")
	m := newManaged(t)
	req := httptest.NewRequest(http.MethodGet, "/api/setup", nil)
	req.Header.Set("X-Auth-Token", "tok")
	rec := httptest.NewRecorder()
	m.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "super-secret-token") {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if d := body["plex"]["detected"].(map[string]any); d["token_found"] != true {
		t.Fatalf("plex detected: %v", d)
	}

	// Saving the marker stores the real token, read on the server.
	code, _ := m.send(t, http.MethodPut, "/api/settings", `{"values":{"PLEX_TOKEN":"@detected"}}`)
	if code != http.StatusAccepted || m.store.Settings()["PLEX_TOKEN"] != "super-secret-token" {
		t.Fatalf("save detected token: %d %v", code, m.store.Settings()["PLEX_TOKEN"] != "")
	}
}

func TestConnectionTestsUseUnsavedValues(t *testing.T) {
	fakePlexToken(t, "plex-tok")
	plexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "plex-tok" && r.URL.Query().Get("X-Plex-Token") != "plex-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"friendlyName":"nas","version":"1.43.5"}}`))
	}))
	defer plexSrv.Close()
	m := newManaged(t)

	code, body := m.send(t, http.MethodPost, "/api/settings/test", `{"target":"plex","values":{"PLEX_URL":"`+plexSrv.URL+`","PLEX_TOKEN":"@detected"}}`)
	if code != http.StatusOK || body["ok"] != true || !strings.Contains(body["detail"].(string), "nas") {
		t.Fatalf("plex test: %d %v", code, body)
	}
	_, body = m.send(t, http.MethodPost, "/api/settings/test", `{"target":"plex","values":{"PLEX_URL":"`+plexSrv.URL+`","PLEX_TOKEN":"wrong"}}`)
	if body["ok"] != false || body["detail"] == "" {
		t.Fatalf("a refused token fails the test: %v", body)
	}
	if len(m.store.Settings()) != 0 {
		t.Fatal("tests never save")
	}

	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			r.ParseForm()
			if r.Form.Get("password") != "pw" {
				w.Write([]byte("Fails."))
				return
			}
			w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			w.Write([]byte("v5.1.4"))
		case "/api/v2/torrents/info":
			w.Write([]byte(`[{"hash":"a"},{"hash":"b"}]`))
		}
	}))
	defer qb.Close()
	_, body = m.send(t, http.MethodPost, "/api/settings/test", `{"target":"qbittorrent","values":{"QBITTORRENT_HOST":"`+qb.URL+`","QBITTORRENT_USERNAME":"admin","QBITTORRENT_PASSWORD":"pw"}}`)
	if body["ok"] != true || body["detail"] != "Connected to qBittorrent v5.1.4 · 2 torrent(s)" {
		t.Fatalf("qbittorrent test: %v", body)
	}
	_, body = m.send(t, http.MethodPost, "/api/settings/test", `{"target":"qbittorrent","values":{"QBITTORRENT_HOST":"`+qb.URL+`","QBITTORRENT_USERNAME":"admin","QBITTORRENT_PASSWORD":"nope"}}`)
	if body["ok"] != false || !strings.Contains(body["detail"].(string), "login failed") {
		t.Fatalf("a wrong password fails the test: %v", body)
	}

	var mu sync.Mutex
	var hooks []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hooks = append(hooks, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	_, body = m.send(t, http.MethodPost, "/api/settings/test", `{"target":"alerts","values":{"ALERT_WEBHOOK_URL":"`+hook.URL+`"}}`)
	if body["ok"] != true || len(hooks) != 1 || !strings.Contains(hooks[0], "Test alert") {
		t.Fatalf("alerts test: %v, delivered %v", body, hooks)
	}
	_, body = m.send(t, http.MethodPost, "/api/settings/test", `{"target":"alerts","values":{}}`)
	if body["ok"] != false || body["detail"] != "No alert channel is set up yet." {
		t.Fatalf("no channels: %v", body)
	}
}
