package remote_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nightjar/internal/agent"
	"nightjar/internal/ddos"
	"nightjar/internal/firewall"
	"nightjar/internal/remote"
)

func TestAnAgentsFirewallIsManagedThroughIt(t *testing.T) {
	ctx := context.Background()
	exec := func(_ context.Context, _ []byte, name string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "-C" {
			return "", errors.New("no such rule")
		}
		return "", nil
	}
	var saved []byte
	s := agent.NewSampler("test")
	s.UseFirewall(&firewall.Manager{Exec: exec, Proc: t.TempDir(), Load: func() []byte { return saved }, Save: func(b []byte) error { saved = b; return nil }}, &ddos.Watcher{Defender: &ddos.Defender{Exec: exec}}, 9101)
	s.Sample(ctx)
	srv := httptest.NewServer(agent.Handler(s, token, nil, true))
	defer srv.Close()

	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	v, err := m.Firewall(ctx, h.ID)
	if err != nil || v.Defense == nil || !v.Defense.On || len(v.Firewall.Rules) != 0 {
		t.Fatalf("firewall: %+v %v", v, err)
	}
	v, err = m.AddFirewallRule(ctx, h.ID, firewall.Rule{Action: "block", Source: "203.0.113.0/24", Note: "scanners"})
	if err != nil || len(v.Firewall.Rules) != 1 || v.Firewall.Rules[0].Source != "203.0.113.0/24" {
		t.Fatalf("add: %+v %v", v, err)
	}
	// The agent's refusal comes back in its words.
	var ae *remote.AgentError
	if _, err := m.AddFirewallRule(ctx, h.ID, firewall.Rule{Action: "block"}); !errors.As(err, &ae) || !strings.Contains(err.Error(), "cut this machine off") {
		t.Fatalf("refused: %v", err)
	}
	// The whole firewall goes through the agent: a second rule, edited, paused, moved; the default.
	if !v.Firewall.Full || len(v.Firewall.Open) == 0 || v.Firewall.Open[len(v.Firewall.Open)-1].Port != 9101 {
		t.Fatalf("a full firewall that keeps the agent's port open: %+v", v.Firewall)
	}
	v, err = m.AddFirewallRule(ctx, h.ID, firewall.Rule{Action: "block", Direction: "out", Port: "25", Note: "no mail"})
	if err != nil || len(v.Firewall.Rules) != 2 {
		t.Fatalf("add outgoing: %+v %v", v, err)
	}
	out := v.Firewall.Rules[1]
	out.Paused, out.Port = true, "25,465"
	if v, err = m.UpdateFirewallRule(ctx, h.ID, out.ID, out); err != nil || !v.Firewall.Rules[1].Paused || v.Firewall.Rules[1].Port != "25,465" {
		t.Fatalf("update: %+v %v", v, err)
	}
	if v, err = m.OrderFirewall(ctx, h.ID, []string{out.ID, v.Firewall.Rules[0].ID}); err != nil || v.Firewall.Rules[0].ID != out.ID {
		t.Fatalf("order: %+v %v", v, err)
	}
	if v, err = m.SetFirewallDefault(ctx, h.ID, "drop"); err != nil || v.Firewall.Default != "drop" || v.Firewall.Pending == nil {
		t.Fatalf("default: %+v %v", v.Firewall, err)
	}
	if v, err = m.KeepFirewallDefault(ctx, h.ID, true); err != nil || v.Firewall.Pending != nil || !strings.Contains(string(saved), `"default":"drop"`) {
		t.Fatalf("keep: %+v %v", v.Firewall, err)
	}
	if _, err := m.KeepFirewallDefault(ctx, h.ID, true); !errors.As(err, &ae) || !strings.Contains(err.Error(), "no change waiting") {
		t.Fatalf("nothing to keep: %v", err)
	}
	for _, r := range v.Firewall.Rules {
		if v, err = m.RemoveFirewallRule(ctx, h.ID, r.ID); err != nil {
			t.Fatalf("remove: %+v %v", v, err)
		}
	}
	if len(v.Firewall.Rules) != 0 {
		t.Fatalf("removed: %+v", v.Firewall.Rules)
	}
	if _, err := m.Unblock(ctx, h.ID, "198.51.100.1"); !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("unblock an address not blocked: %v", err)
	}
	if _, err := m.Firewall(ctx, "nope"); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("no such server: %v", err)
	}
}

func TestAnOldAgentIsSaidToBeTooOld(t *testing.T) {
	ctx := context.Background()
	s := agent.NewSampler("test")
	s.Sample(ctx)
	// An agent from before it managed firewalls: readings, and 404 for the rest.
	readings := agent.Handler(s, token, nil, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == agent.Path {
			readings.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Firewall(ctx, h.ID); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("an old agent: %v", err)
	}
}

// An agent from before the whole firewall has rules but not the calls to edit, order or set the
// default; Go's mux answers 405 where the path is there for another method.
func TestAnAgentWithoutTheWholeFirewallIsSaidToBeTooOld(t *testing.T) {
	ctx := context.Background()
	s := agent.NewSampler("test")
	s.Sample(ctx)
	readings := agent.Handler(s, token, nil, true)
	mux := http.NewServeMux()
	mux.Handle(agent.Path, readings)
	mux.HandleFunc("DELETE "+agent.FirewallPath+"/rules/{id}", func(w http.ResponseWriter, r *http.Request) {})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	m := remote.New(remote.Options{Store: openDB(t), Alerts: &alerts{}})
	h, err := m.Add(ctx, "box", srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	var ae *remote.AgentError
	if _, err := m.UpdateFirewallRule(ctx, h.ID, "x", firewall.Rule{Action: "block", Port: "25"}); !errors.As(err, &ae) || ae.Status != http.StatusNotFound || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("405 from an old agent: %v", err)
	}
	if _, err := m.SetFirewallDefault(ctx, h.ID, "drop"); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("404 from an old agent: %v", err)
	}
}
