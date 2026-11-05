package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"nightjar/internal/units"
)

// systemd services and drive health.

func (s *Server) listUnits(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	if s.ctx.Units == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "units": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": cfg.ServiceWatchEnabled, "self": cfg.SystemdServiceName + ".service", "units": s.ctx.Units.Units()})
}

func (s *Server) unitLogs(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Units == nil {
		writeJSON(w, http.StatusNotFound, errBody(units.ErrUnknown.Error()))
		return
	}
	lines := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil {
		lines = min(max(n, 1), 1000)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	name := r.PathValue("name")
	out, err := s.ctx.Units.Logs(ctx, name, lines)
	if err != nil {
		writeJSON(w, unitErrorStatus(err), errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out, "findings": s.ctx.Units.Diagnose(name, out)})
}

// controlUnit starts, stops or restarts a service the monitor knows. Stopping the monitor itself
// is refused: the dashboard would go with it.
func (s *Server) controlUnit(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	if s.ctx.Units == nil {
		writeJSON(w, http.StatusNotFound, errBody(units.ErrUnknown.Error()))
		return
	}
	if action == "stop" && name == s.ctx.Config().SystemdServiceName+".service" {
		writeJSON(w, http.StatusBadRequest, errBody("Stopping the monitor from its own dashboard would take the dashboard down; use systemctl"))
		return
	}
	if action == "restart" && name == s.ctx.Config().SystemdServiceName+".service" {
		if _, ok := s.ctx.Units.Get(name); !ok {
			writeJSON(w, http.StatusNotFound, errBody(units.ErrUnknown.Error()))
			return
		}
		// Restarting the monitor ends this request with it: answer first. systemd carries the restart
		// out once asked, even though the systemctl that asked is stopped along with the monitor.
		log.Info("Service changed from the dashboard", "unit", name, "action", action)
		writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
		time.AfterFunc(300*time.Millisecond, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := s.ctx.Units.Control(ctx, action, name); err != nil {
				log.Warn("Could not restart the monitor", "unit", name, "err", err)
			}
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.ctx.Units.Control(ctx, action, name); err != nil {
		writeJSON(w, unitErrorStatus(err), errBody(err.Error()))
		return
	}
	log.Info("Service changed from the dashboard", "unit", name, "action", action)
	u, err := s.ctx.Units.Refresh(ctx, name)
	if err != nil {
		writeJSON(w, unitErrorStatus(err), errBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unit": u})
}

func unitErrorStatus(err error) int {
	if errors.Is(err, units.ErrUnknown) {
		return http.StatusNotFound
	}
	return statusRefused
}

func (s *Server) drives(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	if s.ctx.Drives == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "drives": []any{}})
		return
	}
	drives, problem := s.ctx.Drives.Drives()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": cfg.SmartMonitorEnabled, "error": problem, "drives": drives,
		"limits": map[string]int{"hdd": cfg.SmartHDDTempLimit, "ssd": cfg.SmartSSDTempLimit},
	})
}
