package remote

// The dashboard manages an agent's firewall through the agent: its rules, its defense against
// attacks, and the addresses that defense blocked. Each call goes over the HTTPS the token pins.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/firewall"
)

// Firewall is an agent's firewall and defense.
func (m *Monitor) Firewall(ctx context.Context, id string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodGet, agent.FirewallPath, nil)
}

// AddFirewallRule adds a rule on an agent.
func (m *Monitor) AddFirewallRule(ctx context.Context, id string, r firewall.Rule) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPost, agent.FirewallPath+"/rules", r)
}

// RemoveFirewallRule takes a rule off an agent.
func (m *Monitor) RemoveFirewallRule(ctx context.Context, id, rule string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodDelete, agent.FirewallPath+"/rules/"+url.PathEscape(rule), nil)
}

// UpdateFirewallRule changes a rule on an agent: what it matches, its note, or whether it is paused.
func (m *Monitor) UpdateFirewallRule(ctx context.Context, id, rule string, r firewall.Rule) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPut, agent.FirewallPath+"/rules/"+url.PathEscape(rule), r)
}

// OrderFirewall puts an agent's rules in the order given.
func (m *Monitor) OrderFirewall(ctx context.Context, id string, ids []string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPut, agent.FirewallPath+"/order", map[string][]string{"ids": ids})
}

// SetFirewallDefault changes what an agent's firewall does with incoming traffic no rule decides; it
// goes back by itself unless kept with KeepFirewallDefault.
func (m *Monitor) SetFirewallDefault(ctx context.Context, id, def string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPut, agent.FirewallPath+"/default", map[string]string{"default": def})
}

// KeepFirewallDefault keeps an agent's change of the default, or puts it back.
func (m *Monitor) KeepFirewallDefault(ctx context.Context, id string, keep bool) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPost, agent.FirewallPath+"/default/keep", map[string]bool{"keep": keep})
}

// LockPort pins a port on an agent to the service listening on it.
func (m *Monitor) LockPort(ctx context.Context, id string, port uint16, proto string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPost, agent.FirewallPath+"/locks", map[string]any{"port": port, "proto": proto})
}

// RelockPort pins a locked port on an agent to the service holding it now.
func (m *Monitor) RelockPort(ctx context.Context, id, lock string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPut, agent.FirewallPath+"/locks/"+url.PathEscape(lock), nil)
}

// UnlockPort takes a lock off a port on an agent.
func (m *Monitor) UnlockPort(ctx context.Context, id, lock string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodDelete, agent.FirewallPath+"/locks/"+url.PathEscape(lock), nil)
}

// SetDefense switches an agent's defense against attacks on or off.
func (m *Monitor) SetDefense(ctx context.Context, id string, on bool) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodPut, agent.DefensePath, map[string]bool{"on": on})
}

// Unblock lets an address an agent's defense blocked back in.
func (m *Monitor) Unblock(ctx context.Context, id, address string) (agent.FirewallView, error) {
	return m.firewallCall(ctx, id, http.MethodDelete, agent.BlockedPath+"/"+url.PathEscape(address), nil)
}

// ErrNoAgent is a server without an agent to ask.
var ErrNoAgent = errors.New("this server has no agent; install it to manage its firewall from here")

func (m *Monitor) firewallCall(ctx context.Context, id, method, path string, body any) (agent.FirewallView, error) {
	var v agent.FirewallView
	if err := m.agentCall(ctx, id, method, path, body, ErrNoAgent, "this agent is too old for that; it updates itself to this dashboard's version shortly", &v); err != nil {
		return v, err
	}
	if v.Firewall.Rules == nil {
		v.Firewall.Rules = []firewall.Rule{}
	}
	if v.Firewall.Drops == nil {
		v.Firewall.Drops = []firewall.Drop{}
	}
	if v.Firewall.Open == nil {
		v.Firewall.Open = []firewall.Open{}
	}
	if v.Firewall.Listening == nil {
		v.Firewall.Listening = []firewall.Port{}
	}
	return v, nil
}

// agentCall asks a server's agent to manage something on it, and reads its answer into out.
// noAgent is the error for a server without an agent to ask, and tooOld what an agent from before
// the call is told to do.
func (m *Monitor) agentCall(ctx context.Context, id, method, path string, body any, noAgent error, tooOld string, out any) error {
	// Reading the firewall runs a few commands on the server; a change may wait on iptables' lock, or
	// on sshd checking and reloading its config.
	status, raw, name, err := m.agentDo(ctx, id, method, path, body, noAgent, 30*time.Second)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if tooOldAgent(status, msg) {
			msg, status = tooOld, http.StatusNotFound
		}
		return &AgentError{Status: status, Msg: msg}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s did not answer like an agent: %v", name, err)
	}
	return nil
}

// tooOldAgent says whether an answer is an agent from before a call: 404, or 405 where the path is
// there for another method.
func tooOldAgent(status int, msg string) bool {
	return (status == http.StatusNotFound && strings.Contains(msg, "page not found")) || status == http.StatusMethodNotAllowed
}

// agentDo sends a request to a server's agent and returns its status and body, and the server's
// name for errors. noAgent is the error for a server without an agent to ask.
func (m *Monitor) agentDo(ctx context.Context, id, method, path string, body any, noAgent error, timeout time.Duration) (int, []byte, string, error) {
	m.mu.Lock()
	e := m.find(id)
	var h Host
	if e != nil {
		h = e.host
	}
	m.mu.Unlock()
	if e == nil {
		return 0, nil, "", ErrNotFound
	}
	if h.isSSHOnly() || h.isCheck() {
		return 0, nil, h.Name, noAgent
	}
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, h.Address+path, payload)
	if err != nil {
		return 0, nil, h.Name, err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Content-Type", "application/json")
	client := *m.client(h.Address, h.Token)
	client.Timeout = timeout
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, h.Name, fmt.Errorf("no answer from %s: %s", h.Name, plainError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, h.Name, nil
}

// AgentError is an agent turning a request down, with its reason.
type AgentError struct {
	Status int
	Msg    string
}

func (e *AgentError) Error() string { return e.Msg }
