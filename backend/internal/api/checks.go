package api

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"nightjar/internal/services"
)

// Uptime checks: list, add, change, delete and test them.

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Services == nil {
		writeJSON(w, http.StatusOK, map[string]any{"checks": []any{}, "types": services.CheckTypes})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": publicSpecs(s.ctx.Services.Specs(), whoFrom(r).Admin()), "types": services.CheckTypes})
}

// publicSpecs are checks as the browser sees them: passwords left out, and for a viewer the secrets
// in targets too (a heartbeat's token, an address's password).
func publicSpecs(specs []services.Spec, admin bool) []services.PublicSpec {
	out := make([]services.PublicSpec, len(specs))
	for i, sp := range specs {
		if admin {
			out[i] = sp.Public()
		} else {
			out[i] = sp.ForViewer()
		}
	}
	return out
}

// keepPassword gives a check sent without its password the one it has, since the browser never sees
// it: a redis check keeps its password until it is changed or clear_password is sent.
func (s *Server) keepPassword(spec *services.Spec, name string, body []byte) {
	var flags struct {
		Clear bool `json:"clear_password"`
	}
	json.Unmarshal(body, &flags)
	if spec.Type != "redis" || spec.Password != "" || flags.Clear || s.ctx.Services == nil {
		return
	}
	if old, ok := s.ctx.Services.SpecOf(name); ok && old.Type == "redis" {
		spec.Password = old.Password
	}
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
	s.keepPassword(&spec, oldName, body)
	if err := s.ctx.Services.Save(oldName, spec); err != nil {
		writeJSON(w, checkErrorStatus(err), errBody(err.Error()))
		return
	}
	s.renameInDesign(oldName, spec.Name)
	if oldName != "" && oldName != spec.Name {
		s.cfg.Store.RenameRepairs(oldName, spec.Name)
		s.ctx.Maintenance.Rename(oldName, spec.Name)
	}
	// A changed check starts any outage, and its repairs, over.
	s.ctx.Repairs.Forget(cmp.Or(oldName, spec.Name))
	code := http.StatusOK
	if oldName == "" {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"check": spec.Public()})
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
	// Testing a saved check that was not retyped uses its saved password; the form sends its original
	// name as "original" when it was renamed.
	var named struct {
		Original string `json:"original"`
	}
	json.Unmarshal(body, &named)
	s.keepPassword(&spec, cmp.Or(named.Original, spec.Name), body)
	spec.Timeout = min(spec.Timeout, maxTestTimeout)
	// A ping test sends only as many echoes as fit in that time.
	for spec.Type == "ping" && spec.PingCount > 1 && spec.PingDuration() > maxTestTimeout {
		spec.PingCount--
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": services.Run(spec)})
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.ctx.Services.Delete(r.PathValue("name")); err != nil {
		writeJSON(w, checkErrorStatus(err), errBody(err.Error()))
		return
	}
	s.ctx.Repairs.Forget(r.PathValue("name"))
	// The status page names checks by name: a later check given this one's name, private or not,
	// would otherwise appear there under this one's public label.
	s.removeFromDesign(r.PathValue("name"))
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
