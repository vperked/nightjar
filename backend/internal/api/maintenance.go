package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"nightjar/internal/maintenance"
)

func (s *Server) maintenanceList(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"windows": s.ctx.Maintenance.List(), "upcoming": s.ctx.Maintenance.Upcoming(now, 14)})
}

// saveMaintenance adds a window (POST) or replaces one (PUT /{id}).
func (s *Server) saveMaintenance(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Maintenance == nil {
		writeJSON(w, http.StatusServiceUnavailable, errBody("Maintenance windows are not available"))
		return
	}
	var win maintenance.Window
	if err := json.NewDecoder(r.Body).Decode(&win); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a maintenance window"))
		return
	}
	win.ID = r.PathValue("id")
	saved, err := s.ctx.Maintenance.Save(win)
	switch {
	case errors.Is(err, maintenance.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	code := http.StatusOK
	if r.PathValue("id") == "" {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"window": saved})
}

func (s *Server) deleteMaintenance(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Maintenance == nil {
		writeJSON(w, http.StatusNotFound, errBody(maintenance.ErrNotFound.Error()))
		return
	}
	if err := s.ctx.Maintenance.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, errBody(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publicMaintenance is the windows shown on the status page that are on now or begin within a
// week, with the public names of what they cover.
func (s *Server) publicMaintenance() []map[string]any {
	out := []map[string]any{}
	names := s.publicNames()
	for _, o := range s.ctx.Maintenance.Upcoming(time.Now(), 7) {
		if !o.Window.Public {
			continue
		}
		covers := []string{}
		for _, c := range o.Window.Checks {
			if n, ok := names[c]; ok {
				covers = append(covers, n)
			}
		}
		// A window over checks none of which are on the page is no business of visitors.
		if len(o.Window.Checks) > 0 && len(covers) == 0 {
			continue
		}
		out = append(out, map[string]any{"title": o.Window.Title, "note": o.Window.Note, "start": o.Start, "end": o.End, "on": o.On, "covers": covers, "all": len(o.Window.Checks) == 0})
	}
	return out
}

// containers lists Docker's containers, for the check form.
func (s *Server) containers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.ctx.Docker.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": err.Error(), "containers": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "containers": list})
}
