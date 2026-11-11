package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"nightjar/internal/config"
	"nightjar/internal/incidents"
	"nightjar/internal/maintenance"
	"nightjar/internal/notify"
	"nightjar/internal/plugins"
	"nightjar/internal/remote"
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

// backupSecrets are settings shown in the dashboard that a backup without secrets still leaves out:
// on ntfy.sh, whoever knows the topic in NTFY_URL can read the alerts and send their own.
var backupSecrets = map[string]bool{"NTFY_URL": true}

// maxBackup is how big a backup may be to restore.
const maxBackup = 16 << 20

func (s *Server) makeBackup(w http.ResponseWriter, r *http.Request) {
	withSecrets := r.URL.Query().Get("secrets") == "1"
	b := backup{Nightjar: s.cfg.Version, Made: nowSeconds(), Secrets: withSecrets, Checks: []services.Spec{}, Settings: map[string]string{}, Meta: map[string]any{}}
	if s.ctx.Services != nil {
		b.Checks = s.ctx.Services.Specs()
		if !withSecrets {
			// A redis check's password, a heartbeat's token (which alone lets a job ping), and the
			// password or token in an address all stay behind.
			for i := range b.Checks {
				b.Checks[i] = b.Checks[i].WithoutSecrets()
			}
		}
	}
	secret := map[string]bool{}
	cfg := s.ctx.Config()
	for _, f := range cfg.Fields() {
		secret[f.Key] = f.Kind == config.KindSecret || backupSecrets[f.Key]
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
				if !withSecrets {
					v = s.withoutSecrets(k, v)
				}
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
	// The parts restored are kept in memory too, and saved from there; only the restart that follows
	// stops that undoing the restore. An update under way holds the restart until it ends, so the
	// restore waits for it instead.
	if s.ctx.Updates != nil && s.ctx.Updates.Applying() != "" {
		writeJSON(w, http.StatusConflict, errBody("An update is being applied; restore the backup once it has finished"))
		return
	}
	var b backup
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBackup)).Decode(&b); err != nil || b.Nightjar == "" {
		writeJSON(w, http.StatusBadRequest, errBody("That is not a Nightjar backup"))
		return
	}
	// Everything is checked before anything is changed.
	var specs []services.Spec
	for _, raw := range b.Checks {
		// A backup without secrets leaves them out: the same check here keeps its own. This comes
		// before parsing, which would give a heartbeat without a token a new one.
		if !b.Secrets && s.ctx.Services != nil {
			raw.KeepSecrets(s.ctx.Services.SpecOf(raw.Name))
		}
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
	meta := map[string]string{}
	for k, v := range b.Meta {
		if !contains(backupKeys, k) {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Sprintf("The backup holds %q, which Nightjar does not restore", k)))
			return
		}
		if !b.Secrets {
			v = s.keepSecrets(k, v)
		}
		data, _ := json.Marshal(v)
		if err := checkPart(k, data); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(fmt.Sprintf("The backup's %q is not valid: %v", k, err)))
			return
		}
		meta[k] = string(data)
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
	// The rest goes in at once, or not at all.
	if len(meta) > 0 || len(b.Settings) > 0 {
		if err := s.cfg.Store.Restore(meta, b.Settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
			return
		}
	}
	done["parts"] = len(meta)
	restart := false
	if len(b.Settings) > 0 {
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

// checkPart reports whether a part of a backup reads as what Nightjar keeps under its key, so a
// part it could not load (and would then start without, or overwrite) is never restored.
func checkPart(key string, data []byte) error {
	if string(data) == "null" {
		return errors.New("it is empty")
	}
	var err error
	switch key {
	case "notify.prefs":
		err = json.Unmarshal(data, new(notify.Prefs))
	case "maintenance.windows":
		err = json.Unmarshal(data, new([]maintenance.Window))
	case "dashboard.appearance":
		var a Appearance
		if err = json.Unmarshal(data, &a); err == nil {
			err = a.validate()
		}
	case "statuspage.design":
		// Not validated as a saved design is: renaming checks may have left one Nightjar shows as it
		// is but would not take from the editor.
		err = json.Unmarshal(data, new(StatusDesign))
	case "remote.hosts":
		err = json.Unmarshal(data, new([]remote.Host))
	case "plugins.state":
		err = json.Unmarshal(data, new(map[string]plugins.State))
	case "incidents.feedback":
		err = json.Unmarshal(data, new(map[int64]incidents.Feedback))
	case "incidents.notices":
		err = json.Unmarshal(data, new(map[int64]incidents.Notice))
	default:
		// The media cleanup's state is its own; it is at least an object.
		err = json.Unmarshal(data, new(map[string]any))
	}
	return err
}

// Besides the secret settings, two parts hold secrets: each server's agent token in remote.hosts
// (whoever has it can change that server's firewall, or replace its agent with a program of their
// own), and the plugins' secret settings in plugins.state. A backup without secrets leaves them out,
// and restoring one keeps the secrets already here.

// withoutSecrets is a part of the backup with its secrets taken out.
func (s *Server) withoutSecrets(key string, v any) any {
	switch key {
	case "remote.hosts":
		hosts, _ := v.([]any)
		for _, h := range hosts {
			if m, ok := h.(map[string]any); ok {
				delete(m, "token")
			}
		}
	case "plugins.state":
		plain := s.plainPluginSettings()
		states, _ := v.(map[string]any)
		for id, st := range states {
			m, _ := st.(map[string]any)
			settings, _ := m["settings"].(map[string]any)
			// A setting is kept only when the plugin says it is not a secret; one Nightjar
			// cannot look up (the plugin is gone, or plugins are off) is left out.
			for k := range settings {
				if !plain[id][k] {
					delete(settings, k)
				}
			}
		}
	}
	return v
}

// plainPluginSettings is, by plugin, the settings that are not secrets.
func (s *Server) plainPluginSettings() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if s.ctx.Plugins == nil {
		return out
	}
	for _, p := range s.ctx.Plugins.List() {
		out[p.ID] = map[string]bool{}
		for _, set := range p.Settings {
			if set.Type != "secret" {
				out[p.ID][set.Key] = true
			}
		}
	}
	return out
}

// keepSecrets is a part of a backup made without secrets, with the secrets already here put back:
// the agent tokens of the servers it has, and the plugin settings it left out. A token goes back only
// to the same server at the same address: one the backup has somewhere else (saved before the server
// moved) would send it to whatever answers there, so that server has to be paired again.
func (s *Server) keepSecrets(key string, v any) any {
	var current any
	if raw := s.cfg.Store.Meta(key); raw == "" || json.Unmarshal([]byte(raw), &current) != nil {
		return v
	}
	switch key {
	case "remote.hosts":
		here := map[string]map[string]any{}
		old, _ := current.([]any)
		for _, h := range old {
			if m, ok := h.(map[string]any); ok && m["token"] != nil && m["id"] != nil {
				here[fmt.Sprint(m["id"])] = m
			}
		}
		hosts, _ := v.([]any)
		for _, h := range hosts {
			m, ok := h.(map[string]any)
			if !ok || m["id"] == nil {
				continue
			}
			if t, _ := m["token"].(string); t == "" {
				if was, ok := here[fmt.Sprint(m["id"])]; ok && fmt.Sprint(was["address"]) == fmt.Sprint(m["address"]) {
					m["token"] = was["token"]
				}
			}
		}
	case "plugins.state":
		old, _ := current.(map[string]any)
		states, _ := v.(map[string]any)
		for id, st := range states {
			m, ok := st.(map[string]any)
			if !ok {
				continue
			}
			was, _ := old[id].(map[string]any)
			oldSettings, _ := was["settings"].(map[string]any)
			if len(oldSettings) == 0 {
				continue
			}
			settings, _ := m["settings"].(map[string]any)
			if settings == nil {
				settings = map[string]any{}
				m["settings"] = settings
			}
			for k, val := range oldSettings {
				if _, ok := settings[k]; !ok {
					settings[k] = val
				}
			}
		}
	}
	return v
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
