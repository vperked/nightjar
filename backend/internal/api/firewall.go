package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"nightjar/internal/agent"
	"nightjar/internal/firewall"
	"nightjar/internal/remote"
)

// ---------- a server's firewall, through its agent ----------

func (s *Server) hostFirewall(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.Firewall(r.Context(), r.PathValue("id"))
	answerAgent(w, v, err)
}

// addHostRule adds a rule to a server's firewall: {"action": "block", "source": "203.0.113.7",
// "port": "22", "proto": "tcp", "note": "..."}.
func (s *Server) addHostRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := readRule(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.AddFirewallRule(r.Context(), r.PathValue("id"), rule)
	answerAgent(w, v, err)
}

func (s *Server) updateHostRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := readRule(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.UpdateFirewallRule(r.Context(), r.PathValue("id"), r.PathValue("rule"), rule)
	answerAgent(w, v, err)
}

func (s *Server) orderHostRules(w http.ResponseWriter, r *http.Request) {
	ids, ok := readOrder(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.OrderFirewall(r.Context(), r.PathValue("id"), ids)
	answerAgent(w, v, err)
}

func (s *Server) setHostDefault(w http.ResponseWriter, r *http.Request) {
	def, ok := readDefault(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.SetFirewallDefault(r.Context(), r.PathValue("id"), def)
	answerAgent(w, v, err)
}

func (s *Server) keepHostDefault(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	v, err := s.ctx.Remote.KeepFirewallDefault(r.Context(), r.PathValue("id"), keep)
	answerAgent(w, v, err)
}

func (s *Server) removeHostRule(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.RemoveFirewallRule(r.Context(), r.PathValue("id"), r.PathValue("rule"))
	answerAgent(w, v, err)
}

// setHostDefense switches a server's defense against attacks: {"on": false}.
func (s *Server) setHostDefense(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.On == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"on": true} or {"on": false}`))
		return
	}
	v, err := s.ctx.Remote.SetDefense(r.Context(), r.PathValue("id"), *body.On)
	answerAgent(w, v, err)
}

func (s *Server) unblockHost(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.Unblock(r.Context(), r.PathValue("id"), r.PathValue("address"))
	answerAgent(w, v, err)
}

// answerAgent passes on what the agent said, or why it could not be asked. Refusals go out as 422,
// since a proxy such as Cloudflare hides the body of a 5xx.
func answerAgent(w http.ResponseWriter, v agent.FirewallView, err error) {
	var ae *remote.AgentError
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case errors.Is(err, remote.ErrNoAgent):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		writeJSON(w, http.StatusNotFound, errBody(ae.Msg))
	case err != nil:
		writeJSON(w, statusRefused, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func readRule(w http.ResponseWriter, r *http.Request) (firewall.Rule, bool) {
	var rule firewall.Rule
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&rule); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a rule"))
		return rule, false
	}
	return rule, true
}

// readOrder reads {"ids": [...]}, every rule's ID in the new order.
func readOrder(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.IDs == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"ids": [...]}`))
		return nil, false
	}
	return body.IDs, true
}

// readDefault reads {"default": "drop"} or {"default": ""}.
func readDefault(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Default *string `json:"default"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Default == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"default": "drop"} or {"default": ""}`))
		return "", false
	}
	return *body.Default, true
}

// readKeep reads {"keep": true} or {"keep": false}.
func readKeep(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body struct {
		Keep *bool `json:"keep"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil || body.Keep == nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"keep": true} or {"keep": false}`))
		return false, false
	}
	return *body.Keep, true
}

// ---------- this machine's firewall ----------

func (s *Server) localFirewall(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Firewall == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "firewall": s.ctx.Firewall.Status(r.Context())})
}

// localKeep is who must keep reaching this machine besides SSH logins: the browser asking, on the web
// ports and the dashboard's own.
func (s *Server) localKeep(r *http.Request) []firewall.Reach {
	return []firewall.Reach{{Address: s.clientIP(r), Ports: s.dashboardPorts(), What: "you, on this dashboard"}}
}

// dashboardPorts are the ports the dashboard is reached on: the web's, and its own.
func (s *Server) dashboardPorts() []uint16 {
	ports := []uint16{80, 443}
	if p, err := strconv.ParseUint(s.cfg.Port, 10, 16); err == nil {
		ports = append(ports, uint16(p))
	}
	return ports
}

// localChange runs a change of this machine's firewall and answers with the firewall after it.
func (s *Server) localChange(w http.ResponseWriter, r *http.Request, change func() error) {
	if s.ctx.Firewall == nil {
		writeJSON(w, http.StatusConflict, errBody("This dashboard does not manage its firewall"))
		return
	}
	if err := change(); err != nil {
		status := statusRefused
		if errors.Is(err, firewall.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errBody(err.Error()))
		return
	}
	s.localFirewall(w, r)
}

// addLocalRule adds a rule to this machine's firewall. The browser asking must keep reaching the
// dashboard, on the web ports and the dashboard's own.
func (s *Server) addLocalRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := readRule(w, r)
	if !ok {
		return
	}
	s.localChange(w, r, func() error {
		_, err := s.ctx.Firewall.Add(r.Context(), rule, s.localKeep(r))
		return err
	})
}

// updateLocalRule changes a rule: what it matches, its note, or whether it is paused.
func (s *Server) updateLocalRule(w http.ResponseWriter, r *http.Request) {
	rule, ok := readRule(w, r)
	if !ok {
		return
	}
	s.localChange(w, r, func() error {
		_, err := s.ctx.Firewall.Update(r.Context(), r.PathValue("rule"), rule, s.localKeep(r))
		return err
	})
}

func (s *Server) orderLocalRules(w http.ResponseWriter, r *http.Request) {
	ids, ok := readOrder(w, r)
	if !ok {
		return
	}
	s.localChange(w, r, func() error { return s.ctx.Firewall.Order(r.Context(), ids) })
}

// setLocalDefault changes what happens to incoming traffic no rule decides. It goes back by itself
// within a minute unless kept, so a change that cuts the dashboard off undoes itself.
func (s *Server) setLocalDefault(w http.ResponseWriter, r *http.Request) {
	def, ok := readDefault(w, r)
	if !ok {
		return
	}
	s.localChange(w, r, func() error {
		_, err := s.ctx.Firewall.SetDefault(r.Context(), def)
		return err
	})
}

func (s *Server) keepLocalDefault(w http.ResponseWriter, r *http.Request) {
	keep, ok := readKeep(w, r)
	if !ok {
		return
	}
	s.localChange(w, r, func() error { return s.ctx.Firewall.Keep(r.Context(), keep) })
}

func (s *Server) removeLocalRule(w http.ResponseWriter, r *http.Request) {
	s.localChange(w, r, func() error { return s.ctx.Firewall.Remove(r.Context(), r.PathValue("rule")) })
}
