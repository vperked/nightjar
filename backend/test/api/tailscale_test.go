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

	"nightjar/internal/agent"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/remote"
	"nightjar/internal/storage"
	"nightjar/internal/tailscale"
)

func TestTailscaleIsManagedHereAndOnServers(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	t.Cleanup(db.Close)
	const agentToken = "0123456789abcdef0123456789abcdef"
	s0 := agent.NewSampler("test")
	s0.UseTailscale(&tailscale.Manager{Exec: tailscale.NewFake().Exec, LoginWait: time.Millisecond})
	s0.Sample(context.Background())
	srv := httptest.NewServer(agent.Handler(s0, agentToken, nil, true))
	defer srv.Close()
	rm := remote.New(remote.Options{Store: db})
	host, err := rm.Add(context.Background(), "box", srv.URL, agentToken)
	if err != nil {
		t.Fatal(err)
	}

	local := &tailscale.Manager{Exec: tailscale.NewFake().Exec, LoginWait: time.Millisecond}
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}, Tailscale: local, Remote: rm}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "8080", AuthToken: "tok", Store: db, FrontendDir: t.TempDir()}, ctx)
	type view struct {
		tailscale.State
		Error string `json:"error"`
	}
	call := func(method, path, body, from string) (int, view) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		if from != "" {
			req.RemoteAddr = from + ":5000"
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out view
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	for _, base := range []string{"/api/tailscale", "/api/hosts/" + host.ID + "/tailscale"} {
		code, v := call("GET", base, "", "")
		if code != http.StatusOK || !v.LoggedIn || len(v.Peers) != 2 || v.Self == nil || v.Self.Name != "home" {
			t.Fatalf("%s: %d %+v", base, code, v)
		}
		if code, v = call("PUT", base+"/settings", `{"accept_routes": true, "advertise_routes": ["192.168.1.0/24"]}`, ""); code != http.StatusOK || !v.Settings.AcceptRoutes || len(v.Settings.AdvertiseRoutes) != 1 {
			t.Fatalf("%s: settings: %d %+v", base, code, v)
		}
		if code, v = call("PUT", base+"/settings", `{"advertise_routes": ["192.168.1.1/24"]}`, ""); code != 422 || !strings.Contains(v.Error, "host bits") {
			t.Fatalf("%s: bad route: %d %+v", base, code, v)
		}
		if code, v = call("PUT", base+"/settings", `{"exit_node": "nExit"}`, ""); code != http.StatusOK || v.Settings.ExitNode != "nExit" || v.Pending == nil {
			t.Fatalf("%s: exit node: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/keep", `{"keep": false}`, ""); code != http.StatusOK || v.Settings.ExitNode != "" || v.Pending != nil {
			t.Fatalf("%s: undo: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/down", "", ""); code != http.StatusOK || v.Backend != "Stopped" {
			t.Fatalf("%s: down: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/up", "", ""); code != http.StatusOK || v.Backend != "Running" {
			t.Fatalf("%s: up: %d %+v", base, code, v)
		}
		if code, _ := call("POST", base+"/sideways", "", ""); code != http.StatusNotFound {
			t.Fatalf("%s: unknown action: %d", base, code)
		}
		if code, v = call("POST", base+"/logout", "", ""); code != http.StatusOK || v.LoggedIn {
			t.Fatalf("%s: logout: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/login", `{}`, ""); code != http.StatusOK || v.AuthURL == "" {
			t.Fatalf("%s: login link: %d %+v", base, code, v)
		}
		if code, v = call("POST", base+"/login", `{"auth_key": "tskey-auth-abc"}`, ""); code != http.StatusOK || !v.LoggedIn {
			t.Fatalf("%s: login key: %d %+v", base, code, v)
		}
	}
	// A browser reaching this machine over Tailscale may not disconnect it.
	if code, v := call("POST", "/api/tailscale/down", "", "100.64.0.9"); code != 422 || !strings.Contains(v.Error, "over Tailscale") {
		t.Fatalf("down over tailscale: %d %+v", code, v)
	}
	if code, _ := call("GET", "/api/hosts/nope/tailscale", "", ""); code != http.StatusNotFound {
		t.Fatalf("no such server: %d", code)
	}
}
