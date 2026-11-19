package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
)

func TestReadingsNeedTheToken(t *testing.T) {
	s := agent.NewSampler("test")
	h := agent.Handler(s, "a-token-that-is-long-enough-1234", nil, true)
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", agent.Path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := get("a-token-that-is-long-enough-9999"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := get("a-token-that-is-long-enough-1234"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before the first sample: %d", rec.Code)
	}

	s.Sample(context.Background())
	rec := get("a-token-that-is-long-enough-1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("with the token: %d", rec.Code)
	}
	var r agent.Readings
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Version != agent.Version || r.Agent != "test" || r.CPU.Count == 0 || r.Memory.Total == 0 || r.System.Hostname == "" {
		t.Fatalf("readings: %+v", r)
	}
	// Lists are never null, so the dashboard can map over them.
	for _, list := range []string{`"fans":null`, `"disks":null`, `"drives":null`} {
		if strings.Contains(rec.Body.String(), list) {
			t.Errorf("%s in %s", list, rec.Body.String())
		}
	}
}

func TestAnEmptyTokenLetsNobodyIn(t *testing.T) {
	s := agent.NewSampler("test")
	s.Sample(context.Background())
	req := httptest.NewRequest("GET", agent.Path, nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	agent.Handler(s, "", nil, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an agent without a token must not serve anyone: %d", rec.Code)
	}
	if err := agent.Serve(context.Background(), "127.0.0.1:0", "short", "test", false); err != agent.ErrShortToken {
		t.Fatalf("a short token: %v", err)
	}
}

func TestInstallChecksItsInputFirst(t *testing.T) {
	for _, c := range []struct{ listen, token string }{
		{":9101", "short"},
		{":9101", "has a space in it, which breaks the env file"},
		{":9101", "dollar$signs-would-expand-in-the-shell-xx"},
		{"nope", "a-token-that-is-long-enough-1234"},
		{":70000", "a-token-that-is-long-enough-1234"},
	} {
		if err := agent.Install(c.listen, c.token, false, agent.Options{}, &strings.Builder{}); err == nil {
			t.Errorf("install %q %q should fail before touching the machine", c.listen, c.token)
		}
	}
	for in, want := range map[string]string{"9101": ":9101", ":9200": ":9200", "10.0.0.5:9101": "10.0.0.5:9101"} {
		if got, err := agent.ListenAddress(in); err != nil || got != want {
			t.Errorf("listen %q: %q %v", in, got, err)
		}
	}
	unit := agent.Unit()
	// It runs from its state folder, the one place it may write, so it can replace itself.
	for _, want := range []string{"ExecStart=" + agent.ExePath, "StateDirectory=nightjar-agent", "EnvironmentFile=" + agent.EnvPath, "ProtectSystem=strict", "NoNewPrivileges=true"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit should have %q", want)
		}
	}
}

// A plain-HTTP agent's token has crossed the network, and over HTTPS the token is the agent's key:
// moving it to HTTPS takes a new token, and asking for HTTPS without one says so instead of quietly
// staying on plain HTTP.
func TestMovingToHTTPSNeedsANewToken(t *testing.T) {
	const old, fresh = "the-old-plain-token-0123456789", "a-fresh-token-for-https-0123456789"
	plainAgent := agent.Settings{Listen: ":9101", Token: old, Plain: true}
	for _, c := range []struct {
		name      string
		saved     agent.Settings
		installed bool
		token     string
		plain     bool
		asked     bool
		refused   bool
	}{
		{"HTTPS asked for, no token", plainAgent, true, "", false, true, true},
		{"HTTPS on the old token", plainAgent, true, old, false, false, true},
		{"HTTPS on a new token", plainAgent, true, fresh, false, true, false},
		{"a bare reinstall stays plain", plainAgent, true, "", false, false, false},
		{"plain on the old token", plainAgent, true, old, true, false, false},
		{"already HTTPS", agent.Settings{Listen: ":9101", Token: old}, true, "", false, true, false},
		{"a first install", agent.Settings{}, false, fresh, false, true, false},
	} {
		err := agent.CheckMoveToHTTPS(c.saved, c.installed, c.token, c.plain, agent.Options{AskedTLS: c.asked})
		if refused := errors.Is(err, agent.ErrPlainToken); refused != c.refused {
			t.Errorf("%s: %v, refused %v", c.name, err, c.refused)
		}
	}
}

// Connections that idle or crawl are let go, so a pile of them cannot fill the agent's memory.
func TestTheAgentsServerLetsIdleConnectionsGo(t *testing.T) {
	srv := agent.NewHTTPServer(http.NotFoundHandler())
	if srv.IdleTimeout <= 0 || srv.ReadTimeout <= 0 || srv.ReadHeaderTimeout <= 0 || srv.WriteTimeout <= 0 {
		t.Fatalf("every timeout should be set: %+v", srv)
	}
}

func TestTheAgentServesHTTPSWithTheKeyItsTokenGives(t *testing.T) {
	const token = "a-token-that-is-long-enough-1234"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Serve(ctx, addr, token, "test", false)

	get := func(pin string) (*http.Response, error) {
		c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: agent.ClientTLS(pin)}}
		req, _ := http.NewRequest("GET", "https://"+addr+agent.Path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return c.Do(req)
	}
	var resp *http.Response
	var err error
	for range 50 { // the listener and the first sample take a moment
		if resp, err = get(token); err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("with the right pin: %v %v", resp, err)
	}
	resp.Body.Close()
	if _, err := get("another-token-that-is-long-enough"); !errors.Is(err, agent.ErrWrongKey) {
		t.Fatalf("pinned to another token's key, the dashboard must refuse it: %v", err)
	}
	if a, b := mustCert(t, token), mustCert(t, token); string(a) != string(b) {
		t.Fatal("the same token always gives the same key")
	}
}

func mustCert(t *testing.T, token string) []byte {
	c, err := agent.Certificate(token)
	if err != nil {
		t.Fatal(err)
	}
	return c.Certificate[0]
}

func TestTheDashboardManagesTheFirewall(t *testing.T) {
	const token = "a-token-that-is-long-enough-1234"
	var ran []string
	exec := func(_ context.Context, stdin []byte, name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "-C" {
			return "", errors.New("no such rule")
		}
		return "", nil
	}
	var saved []byte
	m := &firewall.Manager{Exec: exec, Proc: t.TempDir(), Load: func() []byte { return saved }, Save: func(b []byte) error { saved = b; return nil }}
	d := &ddos.Defender{Exec: exec}
	d.Update(context.Background(), ddos.Situation{Attacking: true, Methods: []string{ddos.ConnectionFlood}, Sources: []ddos.Source{{Address: "203.0.113.7", Established: 500}}, Interval: 5})
	s := agent.NewSampler("test")
	s.UseFirewall(m, &ddos.Watcher{Defender: d}, 9101)
	call := func(h http.Handler, method, path, body, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "45.33.1.2:50000"
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	h := agent.Handler(s, token, nil, true)
	if rec := call(h, "GET", agent.FirewallPath, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", rec.Code)
	}
	rec := call(h, "GET", agent.FirewallPath, "", token)
	var v agent.FirewallView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.Defense == nil || len(v.Defense.Blocked) != 1 {
		t.Fatalf("view: %d %s", rec.Code, rec.Body)
	}

	// A rule goes in; one that would cut off the dashboard asking does not.
	if rec := call(h, "POST", agent.FirewallPath+"/rules", `{"action": "block", "port": "9101"}`, token); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "the dashboard") {
		t.Fatalf("cutting off the dashboard: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, "POST", agent.FirewallPath+"/rules", `{"action": "block", "source": "198.51.100.0/24", "note": "scanners"}`, token)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || len(v.Firewall.Rules) != 1 || !strings.Contains(string(saved), "scanners") {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", agent.FirewallPath+"/rules/"+v.Firewall.Rules[0].ID, "", token); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}

	// Unblocking lifts the defense's block.
	if rec := call(h, "DELETE", agent.BlockedPath+"/203.0.113.7", "", token); rec.Code != http.StatusOK || len(d.View().Blocked) != 0 {
		t.Fatalf("unblock: %d %s", rec.Code, rec.Body)
	}

	// Over plain HTTP it shows the firewall but changes nothing.
	plain := agent.Handler(s, token, nil, false)
	if rec := call(plain, "GET", agent.FirewallPath, "", token); rec.Code != http.StatusOK {
		t.Fatalf("plain view: %d", rec.Code)
	}
	if rec := call(plain, "POST", agent.FirewallPath+"/rules", `{"action": "block", "source": "198.51.100.9"}`, token); rec.Code != http.StatusForbidden {
		t.Fatalf("plain change: %d", rec.Code)
	}
	// An agent without a firewall to manage says so.
	if rec := call(agent.Handler(agent.NewSampler("test"), token, nil, true), "GET", agent.FirewallPath, "", token); rec.Code != http.StatusNotFound {
		t.Fatalf("no firewall: %d", rec.Code)
	}
}
