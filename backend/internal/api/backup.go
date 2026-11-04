package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"nightjar/internal/config"
	"nightjar/internal/services"
)

// A backup is one JSON file with Nightjar's own setup, for moving to a new machine or undoing a
// mistake: the checks, the settings made in the dashboard (secrets only when asked for), and the
// rest it keeps: alert rules, maintenance windows, the appearance, the status page, other servers,
// plugin settings, and what the owner said about incidents and kept from the cleanup. History
// (check results, alerts, readings) stays behind.

// backupKeys are the meta keys a backup carries.
var backupKeys = []string{"notify.prefs", "maintenance.windows", "dashboard.appearance", "statuspage.design", "remote.hosts",
	"plugins.state", "incidents.feedback", "incidents.notices", "mediacleanup.state"}

type backup struct {
	Nightjar string            `json:"nightjar"` // the version that made it
	Made     float64           `json:"made"`
	Secrets  bool              `json:"secrets"` // whether passwords, tokens and keys are in it
	Checks   []services.Spec   `json:"checks"`
	Settings map[string]string `json:"settings"`
	Meta     map[string]any    `json:"meta"`
}

// maxBackup is how big a backup may be to restore.
const maxBackup = 16 << 20

func (s *Server) makeBackup(w http.ResponseWriter, r *http.Request) {
	withSecrets := r.URL.Query().Get("secrets") == "1"
	b := backup{Nightjar: s.cfg.Version, Made: nowSeconds(), Secrets: withSecrets, Checks: []services.Spec{}, Settings: map[string]string{}, Meta: map[string]any{}}
	if s.ctx.Services != nil {
		b.Checks = s.ctx.Services.Specs()
	}
	secret := map[string]bool{}
	cfg := s.ctx.Config()
	for _, f := range cfg.Fields() {
		secret[f.Key] = f.Kind == config.KindSecret
	}
	for k, v := range s.cfg.Store.Settings() {
		if !secret[k] || withSecrets {
			b.Settings[k] = v
		}
	}
	for _, k := range backupKeys {
		if raw := s.cfg.Store.Meta(k); raw != "" {
			var v any
			if json.Unmarshal([]byte(raw), &v) == nil {
				b.Meta[k] = v
			}
		}
	}
	s.record(r, "Downloaded a backup", "settings", "", map[bool]string{true: "with secrets", false: "without secrets"}[withSecrets])
	host, _ := os.Hostname()
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="nightjar-%s-%s.json"`, host, time.Now().Format("2006-01-02")))
	writeJSON(w, http.StatusOK, b)
}

// restoreBackup puts a backup's setup back: its checks are added or replaced (others are left),
// its settings saved, and the rest replaced. Nightjar restarts when settings that need it changed.
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var b backup
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBackup)).Decode(&b); err != nil || b.Nightjar == "" {
		writeJSON(w, http.StatusBadRequest, errBody("That is not a Nightjar backup"))
		return
	}
	// Everything is checked before anything is changed.
	var specs []services.Spec
	for _, raw := range b.Checks {
		data, _ := json.Marshal(raw)
		spec, err := services.ParseSpec(data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("A check in the backup is not valid: "+err.Error()))
			return
		}
		specs = append(specs, spec)
	}
	if problems := config.Validate(b.Settings); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Some settings in the backup are not valid", "fields": problems})
		return
	}
	for k := range b.Meta {
		if !contains(backupKeys, k) {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Sprintf("The backup holds %q, which Nightjar does not restore", k)))
			return
		}
	}

	done := map[string]int{}
	if s.ctx.Services != nil {
		for _, spec := range specs {
			old := ""
			if s.ctx.Services.Has(spec.Name) {
				old = spec.Name
			}
			if err := s.ctx.Services.Save(old, spec); err != nil && !errors.Is(err, services.ErrExists) {
				writeJSON(w, http.StatusInternalServerError, errBody("Could not restore the check "+spec.Name+": "+err.Error()))
				return
			}
			done["checks"]++
		}
	}
	for k, v := range b.Meta {
		data, _ := json.Marshal(v)
		if err := s.cfg.Store.SetMeta(k, string(data)); err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
			return
		}
		done["parts"]++
	}
	restart := false
	if len(b.Settings) > 0 {
		if err := s.cfg.Store.SaveSettings(b.Settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
			return
		}
		done["settings"] = len(b.Settings)
		restart = true
	}
	// Alert rules, windows and the rest are read at start, so a restart puts all of it in force.
	restart = restart || done["parts"] > 0
	restart = restart && s.cfg.Restart != nil
	writeJSON(w, http.StatusOK, map[string]any{"restored": done, "restarting": restart})
	if restart {
		time.AfterFunc(300*time.Millisecond, s.cfg.Restart)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
