package remote

// Agents follow the dashboard's own build. After a poll shows an agent on another build, the dashboard
// sends it its own for that processor, over the HTTPS the agent's token pins, at most once an hour
// for each agent. The agent checks the build and restarts on it (see agent.UpdatePath).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/agentbin"
)

// updateEvery is how often an agent that is behind is offered the dashboard's build.
const updateEvery = time.Hour

// AgentUpdate is what the dashboard knows about keeping one agent on its build, for its page.
type AgentUpdate struct {
	// State is "current", "updating", "updated", "failed", or "manual" for an agent that cannot
	// update itself until it is reinstalled.
	State  string  `json:"state"`
	Detail string  `json:"detail,omitempty"`
	At     float64 `json:"at,omitempty"`
}

// loadBuild is the dashboard's agent build for an architecture, with its hash.
func (m *Monitor) loadBuild(arch string) ([]byte, string, error) {
	if m.opt.AgentBuild != nil {
		return m.opt.AgentBuild(arch)
	}
	return agentbin.Load(arch)
}

// considerUpdate looks at a poll's readings and, when the agent is behind, sends it the dashboard's
// build in the background. Called with m.mu held.
func (m *Monitor) considerUpdateLocked(e *entry, r *agent.Readings) {
	if m.opt.AutoUpdate != nil && !m.opt.AutoUpdate() {
		return
	}
	arch := r.System.Architecture
	if arch == "" {
		return // an agent too old to say which processor it has
	}
	_, hash, err := m.loadBuild(arch)
	if err != nil {
		return // this dashboard carries no build for it
	}
	switch {
	case r.Build == hash:
		if e.update.State != "updated" {
			e.update = AgentUpdate{State: "current"}
		}
		return
	case r.Build == "" || !r.CanUpdate:
		e.update = AgentUpdate{State: "manual", Detail: "Reinstall the agent once (sudo nightjar-agent install, with the same token) and it keeps itself up to date from then on."}
		if strings.HasPrefix(e.host.Address, "http://") {
			e.update.Detail = "It serves plain HTTP, over which it takes no updates; reinstall it with HTTPS."
		}
		return
	case e.updating || m.opt.Now().Sub(e.updateTried) < updateEvery:
		return
	}
	e.updating, e.updateTried = true, m.opt.Now()
	e.update = AgentUpdate{State: "updating", At: float64(m.opt.Now().Unix())}
	h := e.host
	go m.pushUpdate(e, h, arch)
}

func (m *Monitor) pushUpdate(e *entry, h Host, arch string) {
	err := m.sendBuild(h, arch)
	m.mu.Lock()
	defer m.mu.Unlock()
	e.updating = false
	now := float64(m.opt.Now().Unix())
	if err != nil {
		e.update = AgentUpdate{State: "failed", Detail: err.Error(), At: now}
		log.Warn("Agent update failed", "name", h.Name, "err", err)
		return
	}
	e.update = AgentUpdate{State: "updated", Detail: "Updated to this dashboard's build; it restarted on it.", At: now}
	log.Info("Agent updated", "name", h.Name, "arch", arch)
}

func (m *Monitor) sendBuild(h Host, arch string) error {
	build, hash, err := m.loadBuild(arch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Address+agent.UpdatePath, bytes.NewReader(build))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set(agent.BuildHeader, hash)
	req.Header.Set("Content-Type", "application/octet-stream")
	// The readings client gives up after a few seconds; a build takes longer.
	c := *m.client(h.Address, h.Token)
	c.Timeout = 5 * time.Minute
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("could not send it the update: %s", plainError(err))
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the agent turned the update down: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}
