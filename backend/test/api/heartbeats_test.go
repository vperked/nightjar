package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeartbeatPingsAndIncidents(t *testing.T) {
	m := newManaged(t)
	code, body := m.send(t, http.MethodPost, "/api/checks", `{"name":"backup","type":"heartbeat","schedule":"0 3 * * *"}`)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, body)
	}
	token := body["check"].(map[string]any)["target"].(string)

	ping := func(path, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		m.srv.Handler().ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, string(b)
	}
	// No sign-in: the token is the credential.
	if code, text := ping("/ping/"+token+"/start", ""); code != http.StatusOK || text != "OK\n" {
		t.Fatalf("start: %d %q", code, text)
	}
	if code, _ := ping("/ping/"+token, "812 files"); code != http.StatusOK {
		t.Fatalf("done: %d", code)
	}
	if code, _ := ping("/ping/"+token+"/3", "failed"); code != http.StatusOK {
		t.Fatalf("exit code: %d", code)
	}
	if code, _ := ping("/ping/wrong-token-wrong-token", ""); code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", code)
	}
	if code, _ := ping("/ping/"+token+"/reboot", ""); code != http.StatusNotFound {
		t.Fatalf("unknown kind: %d", code)
	}

	// The check's repairs and the incidents need a sign-in.
	if code, body := m.send(t, http.MethodGet, "/api/repairs?check=backup", ""); code != http.StatusOK || len(body["repairs"].([]any)) != 0 {
		t.Fatalf("repairs: %d %v", code, body)
	}
	if code, body := m.send(t, http.MethodPost, "/api/checks/backup/repair", ""); code != http.StatusBadRequest {
		t.Fatalf("no repair to run: %d %v", code, body)
	}
	if code, body := m.send(t, http.MethodGet, "/api/incidents?days=3", ""); code != http.StatusOK || body["days"] != float64(3) {
		t.Fatalf("incidents: %d %v", code, body)
	}
	if code, _ := m.send(t, http.MethodGet, "/api/incidents", "", "X-Auth-Token", ""); code != http.StatusUnauthorized {
		t.Fatalf("incidents without signing in: %d", code)
	}
}
