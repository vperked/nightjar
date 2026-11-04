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
