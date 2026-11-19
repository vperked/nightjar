package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/firewall"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

func TestThisMachinesWholeFirewallIsManagedFromTheDashboard(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	t.Cleanup(db.Close)
	exec := func(_ context.Context, _ []byte, name string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "-C" {
			return "", context.Canceled
		}
		return "", nil
	}
	var saved []byte
	fw := &firewall.Manager{Exec: exec, Proc: t.TempDir(), Load: func() []byte { return saved }, Save: func(b []byte) error { saved = b; return nil }}
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Firewall: fw}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "8080", AuthToken: "tok", Store: db, FrontendDir: t.TempDir()}, ctx)
	type view struct {
		Firewall firewall.Status `json:"firewall"`
		Error    string          `json:"error"`
	}
	call := func(method, path, body string) (int, view) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.50:40000"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out view
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, v := call("POST", "/api/firewall/rules", `{"action":"allow","port":"80,443","proto":"tcp","note":"web"}`)
	if code != http.StatusOK || len(v.Firewall.Rules) != 1 {
		t.Fatalf("add: %d %+v", code, v)
	}
	code, v = call("POST", "/api/firewall/rules", `{"action":"block","source":"203.0.113.0/24"}`)
	if code != http.StatusOK || v.Firewall.Rules[0].Source != "203.0.113.0/24" {
		t.Fatalf("a block goes before the allow: %d %+v", code, v.Firewall.Rules)
	}
	// The browser asking must keep reaching the dashboard.
	if code, v := call("POST", "/api/firewall/rules", `{"action":"block","source":"198.51.100.0/24"}`); code != 422 || !strings.Contains(v.Error, "cut off you") {
		t.Fatalf("cutting off the dashboard: %d %+v", code, v)
	}
	block := v.Firewall.Rules[0]
	block.Paused = true
	b, _ := json.Marshal(block)
	if code, v = call("PUT", "/api/firewall/rules/"+block.ID, string(b)); code != http.StatusOK || !v.Firewall.Rules[0].Paused {
		t.Fatalf("pause: %d %+v", code, v)
	}
	if code, _ := call("PUT", "/api/firewall/rules/nope", string(b)); code != http.StatusNotFound {
		t.Fatalf("a missing rule: %d", code)
	}
	if code, v = call("PUT", "/api/firewall/order", `{"ids":["`+v.Firewall.Rules[1].ID+`","`+block.ID+`"]}`); code != http.StatusOK || v.Firewall.Rules[1].ID != block.ID {
		t.Fatalf("order: %d %+v", code, v)
	}
	if code, _ := call("PUT", "/api/firewall/order", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no order: %d", code)
	}

	// The default changes at once and waits to be kept; undone, it goes back at once.
	if code, _ := call("PUT", "/api/firewall/default", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no default: %d", code)
	}
	code, v = call("PUT", "/api/firewall/default", `{"default":"drop"}`)
	if code != http.StatusOK || v.Firewall.Default != "drop" || v.Firewall.Pending == nil {
		t.Fatalf("default: %d %+v", code, v.Firewall)
	}
	if code, v = call("POST", "/api/firewall/default/keep", `{"keep":false}`); code != http.StatusOK || v.Firewall.Default != "" || v.Firewall.Pending != nil {
		t.Fatalf("undo: %d %+v", code, v.Firewall)
	}
	if code, _ := call("POST", "/api/firewall/default/keep", `{"keep":true}`); code != 422 {
		t.Fatalf("nothing to keep: %d", code)
	}
	if code, v = call("DELETE", "/api/firewall/rules/"+block.ID, ""); code != http.StatusOK || len(v.Firewall.Rules) != 1 {
		t.Fatalf("remove: %d %+v", code, v)
	}
	// Locks pin a port to the service on it; with nothing listening there is nothing to lock it to.
	if code, _ := call("POST", "/api/firewall/locks", `{}`); code != http.StatusBadRequest {
		t.Fatalf("no port: %d", code)
	}
	if code, v := call("POST", "/api/firewall/locks", `{"port":5432,"proto":"tcp"}`); code != 422 || !strings.Contains(v.Error, "nothing listens") {
		t.Fatalf("nothing to lock to: %d %+v", code, v)
	}
	if code, _ := call("PUT", "/api/firewall/locks/nope", ""); code != http.StatusNotFound {
		t.Fatalf("relock a missing lock: %d", code)
	}
	if code, _ := call("DELETE", "/api/firewall/locks/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unlock a missing lock: %d", code)
	}
	if v.Firewall.Locks == nil {
		t.Fatalf("locks are listed, none or not: %+v", v.Firewall)
	}
}
