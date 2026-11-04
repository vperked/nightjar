package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"nightjar/internal/plexstats"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"nightjar/internal/plex"
)

// Plex: stopping a stream from the dashboard.

const (
	defaultStopMessage = "The server owner stopped this stream."
	maxStopMessage     = 200
)

// plexStop ends one playback session. The message is what the player shows whoever was watching.
func (s *Server) plexStop(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Plex == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("Plex monitoring is off"))
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"message": "..."}`))
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = defaultStopMessage
	}
	if utf8.RuneCountInString(message) > maxStopMessage {
		writeJSON(w, http.StatusBadRequest, errBody("Keep the message under 200 characters"))
		return
	}
	stream, err := s.ctx.Plex.Stop(r.PathValue("id"), message)
	switch {
	case errors.Is(err, plex.ErrNoStream):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	case err != nil:
		writeJSON(w, statusRefused, errBody(err.Error()))
		return
	}
	log.Info("Plex stream stopped from the dashboard", "user", stream.User, "title", stream.Title, "player", stream.Player)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "plex": s.ctx.Plex.Snapshot(false)})
}

// plexStats sums up the last days on Plex (7 unless ?days= says otherwise, up to 90).
func (s *Server) plexStats(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Plex == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil {
		days = min(90, max(1, d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "stats": plexstats.Make(s.cfg.Store, s.ctx.Plex, days, time.Now())})
}
