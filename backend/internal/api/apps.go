package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"nightjar/internal/apps"
	"nightjar/internal/services"
)

// The app store: what can be installed, what is, and the install running now.

func (s *Server) needApps(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ctx.Apps == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("The app store is not running"))
			return
		}
		next(w, r)
	}
}

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apps": s.ctx.Apps.Statuses(), "job": s.ctx.Apps.Job(0), "user": apps.RunAs()})
}

// appJob follows the running install: ?after=<n> skips the log lines already shown.
func (s *Server) appJob(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	writeJSON(w, http.StatusOK, map[string]any{"job": s.ctx.Apps.Job(after)})
}

func (s *Server) installApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port  int  `json:"port"`
		Check bool `json:"check"` // add an uptime check for it afterwards
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a port and whether to add a check"))
		return
	}
	var then func(apps.App, int)
	if body.Check {
		then = s.addAppCheck
	}
	job, err := s.ctx.Apps.Install(r.PathValue("id"), body.Port, apps.RunAs(), then)
	if err != nil {
		writeJSON(w, appErrorStatus(err), errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// addAppCheck watches a newly installed app's web interface, unless a check by that name exists.
func (s *Server) addAppCheck(a apps.App, port int) {
	if port == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]any{"name": a.Name, "type": "http", "target": fmt.Sprintf("http://127.0.0.1:%d", port)})
	spec, err := services.ParseSpec(raw)
	if err == nil {
		err = s.ctx.Services.Save("", spec)
	}
	if err != nil && !errors.Is(err, services.ErrExists) {
		log.Warn("Could not add an uptime check for a new app", "app", a.ID, "err", err)
	}
}

func (s *Server) removeApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Purge bool `json:"purge"` // delete its settings and data too
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected whether to delete its data"))
		return
	}
	job, err := s.ctx.Apps.Remove(r.PathValue("id"), body.Purge)
	if err != nil {
		writeJSON(w, appErrorStatus(err), errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func appErrorStatus(err error) int {
	switch {
	case errors.Is(err, apps.ErrUnknown):
		return http.StatusNotFound
	case errors.Is(err, apps.ErrBusy), errors.Is(err, apps.ErrInstalled), errors.Is(err, apps.ErrNotOurs):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}
