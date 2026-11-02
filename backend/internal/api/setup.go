package api

import (
	"errors"
	"net/http"

	"servermonitor/internal/monitor"
	"servermonitor/internal/setup"
)

// The setup guide: what is set up, and what was found on this machine.

// resolveDetected swaps the "use Plex's own token" marker for the token itself, read on the server
// so it never travels to the browser.
func resolveDetected(values map[string]string) error {
	if values["PLEX_TOKEN"] == setup.DetectedToken {
		token := setup.PlexToken()
		if token == "" {
			return errors.New("this machine's Plex server has no saved token; paste one instead")
		}
		values["PLEX_TOKEN"] = token
	}
	return nil
}

// setupStatus says what is set up and what the monitor found on this machine to offer.
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.ctx.Config()
	var channels []string
	for _, c := range s.ctx.Channels {
		channels = append(channels, c.Name())
	}
	qb := setup.DetectQBittorrent()
	suggestions := []setup.Suggestion{}
	have := map[string]bool{}
	checks := 0
	if s.ctx.Services != nil {
		for _, spec := range s.ctx.Services.Specs() {
			have[spec.Target] = true
			checks++
		}
	}
	for _, sg := range setup.SuggestChecks(qb) {
		if !have[sg.Target] {
			suggestions = append(suggestions, sg)
		}
	}
	targets := 0
	if s.ctx.Updates != nil {
		targets = len(s.ctx.Updates.Statuses())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts": map[string]any{"configured": len(channels) > 0, "channels": channels, "webhook_set": cfg.WebhookURL != ""},
		"plex": map[string]any{
			"enabled": cfg.PlexMonitor, "url": cfg.PlexURL, "token_set": cfg.PlexToken != "",
			"detected": setup.DetectPlex(),
		},
		"qbittorrent": map[string]any{
			"enabled": cfg.QBittorrentMonitor, "host": cfg.QBittorrentHost, "port": cfg.QBittorrentPort,
			"username": cfg.QBittorrentUsername, "password_set": cfg.QBittorrentPassword != "",
			"detected": qb,
		},
		"checks":  map[string]any{"count": checks, "suggestions": suggestions},
		"updates": map[string]any{"enabled": cfg.SystemUpdateEnabled, "targets": targets, "window": monitor.UpdateWindow(cfg).String()},
	})
}
