package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"nightjar/internal/mediacleanup"
)

func (s *Server) cleanupView(w http.ResponseWriter, r *http.Request) {
	if s.ctx.MediaCleanup == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "items": []any{}, "kept": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, s.ctx.MediaCleanup.View())
}

// cleanupScan runs a pass now: it lists what qualifies and, with the cleanup on, removes what is due.
func (s *Server) cleanupScan(w http.ResponseWriter, r *http.Request) {
	if s.ctx.MediaCleanup == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("The media cleanup is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := s.ctx.MediaCleanup.Scan(ctx); err != nil {
		writeJSON(w, statusRefused, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.ctx.MediaCleanup.View())
}

// cleanupKeep spares an item (PUT) or lets it be listed again (DELETE).
func (s *Server) cleanupKeep(w http.ResponseWriter, r *http.Request) {
	if s.ctx.MediaCleanup == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("The media cleanup is not available"))
		return
	}
	err := s.ctx.MediaCleanup.Keep(r.PathValue("key"), r.Method == http.MethodPut)
	switch {
	case errors.Is(err, mediacleanup.ErrUnknown):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
	default:
		writeJSON(w, http.StatusOK, s.ctx.MediaCleanup.View())
	}
}
