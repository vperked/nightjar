package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"servermonitor/internal/config"
	"servermonitor/internal/discord"
	"servermonitor/internal/notify"
	"servermonitor/internal/plex"
	"servermonitor/internal/qbittorrent"
)

// Settings: read, save (live or with a restart), and try connections before saving.

// testAlerts sends a test message to every configured channel and reports each result.
func (s *Server) testAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"results": notify.Test(s.ctx.Channels)})
}

// settingField is one setting as the dashboard shows it: where its value comes from, and
// for secrets only whether one is set.
type settingField struct {
	config.Field
	Value  string `json:"value"`
	Set    bool   `json:"set"`
	Source string `json:"source"` // "dashboard", "environment" or "default"
}

type settingGroup struct {
	Name   string         `json:"name"`
	Fields []settingField `json:"fields"`
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	saved := s.cfg.Store.Settings()
	cfg := s.ctx.Config()
	var groups []settingGroup
	for _, f := range cfg.Fields() {
		if len(groups) == 0 || groups[len(groups)-1].Name != f.Group {
			groups = append(groups, settingGroup{Name: f.Group})
		}
		source := "default"
		if _, ok := saved[f.Key]; ok {
			source = "dashboard"
		} else if v, ok := os.LookupEnv(f.Key); ok && strings.TrimSpace(v) != "" {
			source = "environment"
		}
		g := &groups[len(groups)-1]
		g.Fields = append(g.Fields, settingField{Field: f, Value: f.Value(), Set: f.IsSet(), Source: source})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// saveSettings stores dashboard settings ("" restores the environment's value or the default)
// and restarts the monitor so every part picks them up.
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody("Expected {\"values\": {\"KEY\": \"value\"}}"))
		return
	}
	if problems := config.Validate(req.Values); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Some settings are invalid", "fields": problems})
		return
	}
	if err := resolveDetected(req.Values); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "fields": map[string]string{"PLEX_TOKEN": err.Error()}})
		return
	}
	before := s.cfg.Store.Settings()
	if err := s.cfg.Store.SaveSettings(req.Values); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err.Error()))
		return
	}
	cfg := config.LoadFrom(config.Layered(config.MapLookup(s.cfg.Store.Settings()), os.LookupEnv))
	// Only settings that really changed count: re-saving a field's current value needs nothing.
	var restartFor []string
	current := s.ctx.Config()
	newFields := map[string]string{}
	for _, f := range cfg.Fields() {
		newFields[f.Key] = f.Value()
	}
	for _, f := range current.Fields() {
		// Secrets never show their value, so a secret counts as changed when this save sets it to something new.
		v, inSave := req.Values[f.Key]
		changed := newFields[f.Key] != f.Value() || (f.Kind == config.KindSecret && inSave && v != before[f.Key])
		if changed && !f.Live {
			restartFor = append(restartFor, f.Key)
		}
	}
	if len(restartFor) == 0 || s.cfg.Restart == nil {
		s.ctx.Reconfigure(cfg)
		log.Info("Settings changed in the dashboard; applied without a restart", "keys", len(req.Values))
		writeJSON(w, http.StatusOK, map[string]any{"restarting": false, "applied": true})
		return
	}
	log.Info("Settings changed in the dashboard; restarting to apply them", "keys", restartFor)
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "restart_for": restartFor})
	// Let the response reach the browser before the server goes away.
	time.AfterFunc(300*time.Millisecond, s.cfg.Restart)
}

// testSettings tries a connection with the values typed in the dashboard (falling back to the
// saved ones), without saving anything: "plex", "qbittorrent", or "alerts", which sends a test
// alert to every channel those values set up.
func (s *Server) testSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string            `json:"target"`
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(`Expected {"target": "plex", "values": {...}}`))
		return
	}
	if req.Values == nil {
		req.Values = map[string]string{}
	}
	if problems := config.Validate(req.Values); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Some settings are invalid", "fields": problems})
		return
	}
	if err := resolveDetected(req.Values); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	cfg := config.LoadFrom(config.Layered(config.MapLookup(req.Values), config.MapLookup(s.cfg.Store.Settings()), os.LookupEnv))
	switch req.Target {
	case "plex":
		writeJSON(w, http.StatusOK, testPlex(cfg))
	case "qbittorrent":
		writeJSON(w, http.StatusOK, testQBittorrent(cfg))
	case "alerts":
		writeJSON(w, http.StatusOK, testChannels(cfg))
	default:
		writeJSON(w, http.StatusBadRequest, errBody("target must be plex, qbittorrent or alerts"))
	}
}

type testResult struct {
	OK      bool            `json:"ok"`
	Detail  string          `json:"detail"`
	Results []notify.Result `json:"results,omitempty"`
}

func testPlex(cfg config.Settings) testResult {
	if cfg.PlexToken == "" {
		return testResult{Detail: "A token is needed: most Plex servers refuse requests without one."}
	}
	c, err := plex.New(cfg.PlexURL, cfg.PlexToken, cfg.PlexVerifyTLS, 5*time.Second)
	if err != nil {
		return testResult{Detail: err.Error()}
	}
	srv, err := c.Server()
	if err != nil {
		return testResult{Detail: err.Error()}
	}
	return testResult{OK: true, Detail: fmt.Sprintf("Connected to %s (Plex %s)", cmp.Or(srv.Name, "Plex"), srv.Version)}
}

func testQBittorrent(cfg config.Settings) testResult {
	c, err := qbittorrent.New(cfg.QBittorrentHost, cfg.QBittorrentPort, cfg.QBittorrentUsername, cfg.QBittorrentPassword)
	if err != nil {
		return testResult{Detail: err.Error()}
	}
	list, err := c.List()
	if err != nil {
		return testResult{Detail: err.Error()}
	}
	return testResult{OK: true, Detail: fmt.Sprintf("Connected to qBittorrent %s · %d torrent(s)", c.Version(), len(list))}
}

func testChannels(cfg config.Settings) testResult {
	host, _ := os.Hostname()
	origin := discord.Origin{Host: host, DashboardURL: cfg.DashboardURL}
	channels, problems := notify.FromSettings(cfg, origin)
	if cfg.WebhookURL != "" {
		hook, err := discord.NewWebhook(cfg.WebhookURL, cfg.AlertLogPath)
		if err != nil {
			problems = append(problems, err)
		} else {
			hook.Origin = origin
			channels = append([]notify.Channel{hook}, channels...)
		}
	}
	res := testResult{Results: notify.Test(channels)}
	for _, p := range problems {
		res.Results = append(res.Results, notify.Result{Channel: "settings", Error: p.Error()})
	}
	if len(res.Results) == 0 {
		res.Detail = "No alert channel is set up yet."
		return res
	}
	res.OK = true
	for _, r := range res.Results {
		res.OK = res.OK && r.OK
	}
	return res
}
