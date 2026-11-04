package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nightjar/internal/notify"
	"nightjar/internal/storage"
)

// The notification center: every alert the monitor raised, the routing rules, quiet hours and mutes.

func (s *Server) hub(w http.ResponseWriter) *notify.Hub {
	if s.ctx == nil || s.ctx.Notifications == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("Notifications are not running"))
		return nil
	}
	return s.ctx.Notifications
}

// notifications lists the history, newest first: ?q= searches, ?severity=critical,warning,
// ?area=, ?unread=1 and ?before=<id> (paging) filter it.
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := storage.NotificationQuery{Search: strings.TrimSpace(q.Get("q")), Area: q.Get("area"), Unread: q.Get("unread") == "1"}
	if sev := q.Get("severity"); sev != "" {
		query.Severities = strings.Split(sev, ",")
	}
	query.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	query.Limit, _ = strconv.Atoi(q.Get("limit"))
	items, total := s.cfg.Store.Notifications(query)
	if items == nil {
		items = []storage.Notification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "counts": s.cfg.Store.NotificationCounts(), "kept": s.cfg.Store.Available(),
	})
}

// notificationSummary is what the header's bell polls: unread counts, mutes and quiet hours.
func (s *Server) notificationSummary(w http.ResponseWriter, r *http.Request) {
	hub := s.hub(w)
	if hub == nil {
		return
	}
	quiet, held := hub.QuietNow()
	writeJSON(w, http.StatusOK, map[string]any{
		"counts": s.cfg.Store.NotificationCounts(), "mutes": hub.Mutes(), "quiet": quiet, "held": held,
	})
}

func (s *Server) markNotifications(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []int64 `json:"ids"`
		All    bool    `json:"all"`
		Unread bool    `json:"unread"` // mark them unread instead
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (!body.All && len(body.IDs) == 0) {
		writeJSON(w, http.StatusBadRequest, errBody("Send the ids to mark, or all"))
		return
	}
	if err := s.cfg.Store.MarkNotificationsRead(body.IDs, body.All, !body.Unread); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": s.cfg.Store.NotificationCounts()})
}

func (s *Server) notificationRules(w http.ResponseWriter, r *http.Request) {
	hub := s.hub(w)
	if hub == nil {
		return
	}
	rules, defaults := hub.Rules()
	quiet, held := hub.QuietNow()
	writeJSON(w, http.StatusOK, map[string]any{
		"areas": notify.Areas, "channels": hub.Channels(), "rules": rules, "defaults": defaults,
		"quiet": hub.Quiet(), "quiet_active": quiet, "held": held, "mutes": hub.Mutes(),
	})
}

func (s *Server) saveNotificationRules(w http.ResponseWriter, r *http.Request) {
	hub := s.hub(w)
	if hub == nil {
		return
	}
	var body struct {
		Rules map[string]map[string]notify.Level `json:"rules"`
		Quiet notify.Quiet                       `json:"quiet"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected rules and quiet hours"))
		return
	}
	if err := hub.SetRules(body.Rules, body.Quiet); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	s.notificationRules(w, r)
}

// mute silences a check, service, drive or area for a number of minutes (0 unmutes it).
func (s *Server) mute(w http.ResponseWriter, r *http.Request) {
	hub := s.hub(w)
	if hub == nil {
		return
	}
	var body struct {
		Subject string `json:"subject"`
		Label   string `json:"label"`
		Minutes int    `json:"minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a subject and minutes"))
		return
	}
	if err := hub.Mute(body.Subject, body.Label, time.Duration(body.Minutes)*time.Minute); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mutes": hub.Mutes()})
}
