package api

import (
	"errors"
	"io"
	"net/http"

	"servermonitor/internal/services"
)

// Uptime checks: list, add, change, delete and test them.

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Services == nil {
		writeJSON(w, http.StatusOK, map[string]any{"checks": []any{}, "types": services.CheckTypes})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": s.ctx.Services.Specs(), "types": services.CheckTypes})
}

// saveCheck creates a check (POST) or replaces the one named in the path (PUT), which may rename it.
func (s *Server) saveCheck(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Request too large"))
		return
	}
	spec, err := services.ParseSpec(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	oldName := r.PathValue("name")
	if err := s.ctx.Services.Save(oldName, spec); err != nil {
		writeJSON(w, checkErrorStatus(err), errBody(err.Error()))
		return
	}
	s.renameInDesign(oldName, spec.Name)
	code := http.StatusOK
	if oldName == "" {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"check": spec})
}

// maxTestTimeout keeps a test within the server's write timeout.
const maxTestTimeout = 20

// testCheck runs a check once without saving it, so the dashboard can show whether the target
// answers before the check is added or changed.
func (s *Server) testCheck(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("Request too large"))
		return
	}
	spec, err := services.ParseSpec(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}
	spec.Timeout = min(spec.Timeout, maxTestTimeout)
	writeJSON(w, http.StatusOK, map[string]any{"result": services.Run(spec)})
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.ctx.Services.Delete(r.PathValue("name")); err != nil {
		writeJSON(w, checkErrorStatus(err), errBody(err.Error()))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func checkErrorStatus(err error) int {
	switch {
	case errors.Is(err, services.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, services.ErrExists):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
