package remote

// The dashboard manages who may log in to an agent's server over SSH through the agent: the keys
// each account lets in, and whether sshd still takes passwords.

import (
	"context"
	"net/http"

	"nightjar/internal/agent"
	"nightjar/internal/sshd"
)

// errNoAgentSSH is a server without an agent, asked about its SSH logins; it is ErrNoAgent.
var errNoAgentSSH = noAgentError("this server has no agent; install it to manage its SSH logins from here")

// noAgentError is ErrNoAgent in words for what was asked.
type noAgentError string

func (e noAgentError) Error() string        { return string(e) }
func (e noAgentError) Is(target error) bool { return target == ErrNoAgent }

// sshdTooOld is what an agent from before it could manage SSH logins is told to do.
const sshdTooOld = "this server's agent is too old for this; update it from the Servers page"

// SSHD is who may log in to an agent's server over SSH.
func (m *Monitor) SSHD(ctx context.Context, id string) (sshd.Access, error) {
	return m.sshdCall(ctx, id, http.MethodGet, agent.SSHDPath, nil)
}

// AddSSHKey adds a public key to an account on an agent's server.
func (m *Monitor) AddSSHKey(ctx context.Context, id, user, key string) (sshd.Access, error) {
	return m.sshdCall(ctx, id, http.MethodPost, agent.SSHDPath+"/keys", map[string]string{"user": user, "key": key})
}

// SetKeysOnly turns password logins on an agent's server off (on) or back on; off goes back by
// itself unless kept with KeepKeysOnly.
func (m *Monitor) SetKeysOnly(ctx context.Context, id string, on bool, user string) (sshd.Access, error) {
	return m.sshdCall(ctx, id, http.MethodPut, agent.SSHDPath+"/keys-only", map[string]any{"on": on, "user": user})
}

// KeepKeysOnly keeps keys only on an agent's server, or puts passwords back now.
func (m *Monitor) KeepKeysOnly(ctx context.Context, id string, keep bool) (sshd.Access, error) {
	return m.sshdCall(ctx, id, http.MethodPost, agent.SSHDPath+"/keys-only/keep", map[string]bool{"keep": keep})
}

func (m *Monitor) sshdCall(ctx context.Context, id, method, path string, body any) (sshd.Access, error) {
	var v sshd.Access
	if err := m.agentCall(ctx, id, method, path, body, errNoAgentSSH, sshdTooOld, &v); err != nil {
		return v, err
	}
	if v.Ports == nil {
		v.Ports = []uint16{}
	}
	if v.Users == nil {
		v.Users = []sshd.User{}
	}
	for i := range v.Users {
		if v.Users[i].Fingerprints == nil {
			v.Users[i].Fingerprints = []string{}
		}
	}
	return v, nil
}
