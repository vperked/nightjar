package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"nightjar/internal/plugins"
)

// Plugins: the owner's own checks, cards and alert channels, found in the plugins folder.

func (s *Server) needPlugins(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.ctx.Plugins == nil {
			writeJSON(w, http.StatusServiceUnavailable, errBody("Plugins are not loaded"))
			return
		}
		next(w, r)
	}
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"dir": s.ctx.Plugins.Dir(), "plugins": s.ctx.Plugins.List()})
}

// rescanPlugins looks in the plugins folder again, for plugins added or changed since the start.
// When the plugins that deliver alerts changed, Nightjar restarts to set up their channels, and the
// answer says so with "restarting".
func (s *Server) rescanPlugins(w http.ResponseWriter, r *http.Request) {
	before := notifyPlugins(s.ctx.Plugins.List())
	if err := s.ctx.Plugins.Scan(); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("Could not read the plugins folder: "+err.Error()))
		return
	}
	list := s.ctx.Plugins.List()
	restarting := s.ctx.Plugins.NotifyChanged != nil && notifyPlugins(list) != before
	writeJSON(w, http.StatusOK, map[string]any{"dir": s.ctx.Plugins.Dir(), "plugins": list, "restarting": restarting})
}

// notifyPlugins names the plugins that deliver alerts, by id and name, to tell when they change.
func notifyPlugins(list []plugins.View) string {
	var out []string
	for _, v := range list {
		if slices.Contains(v.Provides, plugins.Notify) {
			out = append(out, v.ID+"\x00"+v.Name)
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}

func (s *Server) configurePlugin(w http.ResponseWriter, r *http.Request) {
	var c plugins.Change
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected the plugin's settings"))
		return
	}
	id := r.PathValue("id")
	if err := s.ctx.Plugins.Configure(id, c); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, plugins.ErrUnknown) {
			code = http.StatusNotFound
		}
		writeJSON(w, code, errBody(err.Error()))
		return
	}
	for _, v := range s.ctx.Plugins.List() {
		if v.ID == id {
			writeJSON(w, http.StatusOK, map[string]any{"plugin": v})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errBody(plugins.ErrUnknown.Error()))
}

// tryPlugin runs a plugin's check or card once, so the owner can see what it says.
func (s *Server) tryPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode     string `json:"mode"`
		Argument string `json:"argument"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Expected a mode: check or card"))
		return
	}
	id := r.PathValue("id")
	if _, ok := s.ctx.Plugins.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, errBody(plugins.ErrUnknown.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(plugins.Trying(r.Context()), 2*time.Minute)
	defer cancel()
	switch req.Mode {
	case plugins.Check:
		writeJSON(w, http.StatusOK, map[string]any{"check": s.ctx.Plugins.RunCheck(ctx, id, req.Argument)})
	case plugins.Card:
		writeJSON(w, http.StatusOK, map[string]any{"card": s.ctx.Plugins.RunCard(ctx, id)})
	default:
		writeJSON(w, http.StatusBadRequest, errBody("The mode must be check or card; alert channels are tried from Notifications"))
	}
}

func (s *Server) pluginCards(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"cards": s.ctx.Plugins.Cards()})
}
