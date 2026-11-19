package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/remote"
	"nightjar/internal/updates"
)

// Updates on the servers the dashboard watches, through each one's agent: the same routes and
// answers as this machine's own (/api/updates), under /api/hosts/{id}/updates.

// hostUpdatesBudget is the longest a call to an agent about its updates takes.
const hostUpdatesBudget = 30 * time.Second

// detectBudget is the longest looking for apps takes, here or on an agent.
const detectBudget = remote.DetectWait

// hostUpdatesSummary is each server's updates as last polled, for the picker and the badge.
func (s *Server) hostUpdatesSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"hosts": s.ctx.Remote.UpdatesSummary()})
}

func (s *Server) hostUpdates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctx.Remote.HostUpdates(r.Context(), r.PathValue("id"))
	// The agent takes any command from whoever has its token; whether this dashboard lets them be
	// chosen is its own rule, as for this machine.
	v.CommandsEditable = s.signInRequired()
	answerHostUpdates(w, http.StatusOK, v, err)
}

// saveHostUpdate adds a target (POST) or replaces the one in the path (PUT).
func (s *Server) saveHostUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Request too large"))
		return
	}
	spec, err := updates.ParseSpec(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	host, target := r.PathValue("id"), r.PathValue("target")
	if !s.signInRequired() {
		// The same rule as this machine's: without sign-in, only what was there may run.
		var before updates.Spec
		if target != "" {
			v, err := s.ctx.Remote.HostUpdates(r.Context(), host)
			if err != nil {
				answerHostUpdates(w, http.StatusOK, nil, err)
				return
			}
			for _, t := range v.Targets {
				if t.ID == target {
					before = t.Spec
				}
			}
		}
		if spec.KeepUnsent(before).Locked() != before.Locked() {
			writeJSON(w, http.StatusForbidden, errBody(commandsLocked))
			return
		}
	}
	if target == "" {
		st, err := s.ctx.Remote.AddHostUpdate(r.Context(), host, spec)
		answerHostUpdates(w, http.StatusCreated, map[string]any{"target": st}, err)
		return
	}
	st, err := s.ctx.Remote.SaveHostUpdate(r.Context(), host, target, spec)
	answerHostUpdates(w, http.StatusOK, map[string]any{"target": st}, err)
}

func (s *Server) deleteHostUpdate(w http.ResponseWriter, r *http.Request) {
	err := s.ctx.Remote.DeleteHostUpdate(r.Context(), r.PathValue("id"), r.PathValue("target"))
	answerHostUpdates(w, http.StatusNoContent, nil, err)
}

// checkHostUpdates queues a check of every target on a server, or of the one in the path.
func (s *Server) checkHostUpdates(w http.ResponseWriter, r *http.Request) {
	err := s.ctx.Remote.CheckHostUpdates(r.Context(), r.PathValue("id"), r.PathValue("target"))
	answerHostUpdates(w, http.StatusAccepted, map[string]any{"queued": true}, err)
}

func (s *Server) applyHostUpdate(w http.ResponseWriter, r *http.Request) {
	err := s.ctx.Remote.ApplyHostUpdate(r.Context(), r.PathValue("id"), r.PathValue("target"))
	answerHostUpdates(w, http.StatusAccepted, map[string]any{"queued": true}, err)
}

// detectHostUpdates has a server look again for what to keep updated.
func (s *Server) detectHostUpdates(w http.ResponseWriter, r *http.Request) {
	// Detection on the server may take longer than the other calls' budget.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(remote.DetectWait + 30*time.Second))
	added, err := s.ctx.Remote.DetectHostUpdates(r.Context(), r.PathValue("id"))
	answerHostUpdates(w, http.StatusOK, map[string]any{"added": added}, err)
}

// setHostUpdateSettings changes a server's update schedule: {"enabled": true, "interval": 21600,
// "window": {"start": 240, "end": 360}, "notify": true}, the window in minutes after its midnight.
func (s *Server) setHostUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body agent.UpdateSettingsChange
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"enabled": true, "interval": 21600, "window": {"start": 240, "end": 360}, "notify": true}`))
		return
	}
	v, err := s.ctx.Remote.SetHostUpdateSettings(r.Context(), r.PathValue("id"), body)
	v.CommandsEditable = s.signInRequired()
	answerHostUpdates(w, http.StatusOK, v, err)
}

// answerHostUpdates answers with v and status, or with why the agent could not be asked or turned
// the request down. Its "not found", "bad request" and "busy" keep their status, as this machine's
// own routes answer them; other refusals go out as 422, since a proxy such as Cloudflare hides the
// body of a 5xx, and an agent's 401 must not read as this dashboard's sign-in running out.
func answerHostUpdates(w http.ResponseWriter, status int, v any, err error) {
	var ae *remote.AgentError
	switch {
	case errors.Is(err, remote.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case errors.Is(err, remote.ErrNoAgent):
		writeJSON(w, http.StatusConflict, errBody(err.Error()))
	case errors.As(err, &ae) && (ae.Status == http.StatusBadRequest || ae.Status == http.StatusNotFound || ae.Status == http.StatusConflict):
		writeJSON(w, ae.Status, errBody(ae.Msg))
	case err != nil:
		writeJSON(w, statusRefused, errBody(err.Error()))
	case status == http.StatusNoContent:
		w.WriteHeader(status)
	default:
		writeJSON(w, status, v)
	}
}
