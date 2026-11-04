package api

import (
	"errors"
	"io"
	"net/http"

	"nightjar/internal/monitor"
	"nightjar/internal/storage"
	"nightjar/internal/updates"
)

// Update targets: list, check, install and manage them.

// commandsLocked keeps a token-less dashboard from choosing what the monitor runs as root:
// without a token, any local process could reach the API.
const commandsLocked = "Commands and paths can be edited here only when WEB_AUTH_TOKEN is set. Without a token, any program on this machine could use the API to run commands as root."

// needUpdates answers 503 when the monitor runs without an update manager.
func (s *Server) needUpdates(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ctx.Updates == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("Updates are not available"))
			return
		}
		next(w, r)
	}
}

func (s *Server) updates(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	window := monitor.UpdateWindow(cfg)
	body := map[string]any{
		"enabled":           cfg.SystemUpdateEnabled,
		"interval":          cfg.SystemUpdateIntervalSeconds,
		"window":            map[string]any{"start": window.Start, "end": window.End, "label": window.String()},
		"commands_editable": s.signInRequired(),
		"kinds":             updates.Kinds,
		"checking":          false,
		"last_check":        0,
		"targets":           []updates.Status{},
	}
	if m := s.ctx.Updates; m != nil {
		body["checking"], body["last_check"] = m.Activity()
		body["targets"] = m.Statuses()
	}
	writeJSON(w, http.StatusOK, body)
}

// saveUpdateTarget adds a target (POST) or replaces the one in the path (PUT).
func (s *Server) saveUpdateTarget(w http.ResponseWriter, r *http.Request) {
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
	id := r.PathValue("id")
	if !s.signInRequired() {
		var before [4]string
		if old, ok := s.ctx.Updates.Get(id); ok {
			before = old.Commands()
		}
		if spec.Commands() != before {
			writeJSON(w, http.StatusForbidden, errBody(commandsLocked))
			return
		}
	}
	st, err := s.ctx.Updates.Save(id, spec)
	if err != nil {
		writeJSON(w, updateErrorStatus(err), errBody(err.Error()))
		return
	}
	code := http.StatusOK
	if id == "" {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"target": st})
}

func (s *Server) deleteUpdateTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.ctx.Updates.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, updateErrorStatus(err), errBody(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkUpdates queues a check of every target, or of the one in the path.
func (s *Server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	if id := r.PathValue("id"); id != "" {
		if _, ok := s.ctx.Updates.Get(id); !ok {
			writeJSON(w, http.StatusNotFound, errBody(updates.ErrNotFound.Error()))
			return
		}
		s.ctx.Updates.Check(id)
	} else {
		s.ctx.Updates.CheckAll()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if err := s.ctx.Updates.Apply(r.PathValue("id")); err != nil {
		writeJSON(w, updateErrorStatus(err), errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// detectUpdates adds what detection finds that is not listed yet.
func (s *Server) detectUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"added": s.ctx.Updates.AddDetected(s.detect())})
}

func updateErrorStatus(err error) int {
	switch {
	case errors.Is(err, updates.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, updates.ErrExists), errors.Is(err, updates.ErrBusy):
		return http.StatusConflict
	case errors.Is(err, storage.ErrUnavailable):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}
