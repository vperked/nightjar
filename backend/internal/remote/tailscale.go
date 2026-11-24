package remote

// The dashboard manages an agent's server's Tailscale through the agent: connecting and logging in,
// and the settings (exit node, subnet routes, Tailscale SSH and the rest).

import (
	"context"
	"net/http"

	"nightjar/internal/agent"
	"nightjar/internal/tailscale"
)

var errNoAgentTailscale = noAgentError("this server has no agent; install it to manage its Tailscale from here")

// Tailscale is an agent's server's Tailscale.
func (m *Monitor) Tailscale(ctx context.Context, id string) (tailscale.State, error) {
	return m.tailscaleCall(ctx, id, http.MethodGet, agent.TailscalePath, nil)
}

// TailscaleLogin logs an agent's server in to a tailnet: with an auth key, or by a link to open.
func (m *Monitor) TailscaleLogin(ctx context.Context, id, key string) (tailscale.State, error) {
	return m.tailscaleCall(ctx, id, http.MethodPost, agent.TailscalePath+"/login", map[string]string{"auth_key": key})
}

// TailscaleAction connects (up), disconnects (down) or logs out (logout) an agent's server.
func (m *Monitor) TailscaleAction(ctx context.Context, id, action string) (tailscale.State, error) {
	return m.tailscaleCall(ctx, id, http.MethodPost, agent.TailscalePath+"/"+action, nil)
}

// SetTailscale changes an agent's server's Tailscale settings.
func (m *Monitor) SetTailscale(ctx context.Context, id string, s tailscale.Settings) (tailscale.State, error) {
	return m.tailscaleCall(ctx, id, http.MethodPut, agent.TailscalePath+"/settings", s)
}

// KeepTailscale keeps a risky change on an agent's server, or puts it back now.
func (m *Monitor) KeepTailscale(ctx context.Context, id string, keep bool) (tailscale.State, error) {
	return m.tailscaleCall(ctx, id, http.MethodPost, agent.TailscalePath+"/keep", map[string]bool{"keep": keep})
}

func (m *Monitor) tailscaleCall(ctx context.Context, id, method, path string, body any) (tailscale.State, error) {
	var v tailscale.State
	if err := m.agentCall(ctx, id, method, path, body, errNoAgentTailscale, sshdTooOld, &v); err != nil {
		return v, err
	}
	if v.Peers == nil {
		v.Peers = []tailscale.Node{}
	}
	if v.Health == nil {
		v.Health = []string{}
	}
	if v.Settings.AdvertiseRoutes == nil {
		v.Settings.AdvertiseRoutes = []string{}
	}
	for i := range v.Peers {
		if v.Peers[i].IPs == nil {
			v.Peers[i].IPs = []string{}
		}
	}
	return v, nil
}
