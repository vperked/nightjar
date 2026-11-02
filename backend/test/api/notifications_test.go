package api_test

import (
	"net/http"
	"strconv"
	"testing"

	"servermonitor/internal/discord"
	"servermonitor/internal/logx"
	"servermonitor/internal/notify"
)

type quietChannel struct{ name string }

func (c quietChannel) Name() string      { return c.name }
func (c quietChannel) LastError() string { return "" }
func (c quietChannel) SendEmbed(string, string, discord.Severity, []discord.Field, string) bool {
	return true
}

func TestNotificationCenter(t *testing.T) {
	m := newManaged(t)
	hub := notify.NewHub(m.store, []notify.Destination{{Channel: quietChannel{"Discord"}}, {Channel: quietChannel{"ntfy"}}})
	m.ctx.Notifications = hub
	discord.About(hub.Route("resources"), "check:Plex").SendEmbed("Service Down: Plex", "timeout", discord.Critical, nil, "Uptime")
	hub.Route("torrents").SendEmbed("Download Complete", "Alien (1979)", discord.OK, nil, "qBittorrent")

	code, body := m.send(t, http.MethodGet, "/api/notifications?q=plex", "")
	items := body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || body["total"] != float64(1) {
		t.Fatalf("search: %d %v", code, body)
	}
	plex := items[0].(map[string]any)
	if plex["subject"] != "check:Plex" || plex["area"] != "uptime" || plex["read"] != false {
		t.Fatalf("item: %v", plex)
	}
	if c := body["counts"].(map[string]any); c["unread"] != float64(2) || c["alerts"] != float64(1) || c["worst"] != "critical" {
		t.Fatalf("counts: %v", c)
	}

	if code, body = m.send(t, http.MethodPost, "/api/notifications/read", `{"all":true}`); code != http.StatusOK || body["counts"].(map[string]any)["unread"] != float64(0) {
		t.Fatalf("read all: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/notifications/read", `{}`); code != http.StatusBadRequest {
		t.Fatalf("nothing to mark should be refused, got %d", code)
	}

	// Rules: every area for every channel, then a change.
	code, body = m.send(t, http.MethodGet, "/api/notifications/rules", "")
	if code != http.StatusOK || len(body["channels"].([]any)) != 2 || body["rules"].(map[string]any)["updates"].(map[string]any)["ntfy"] != "all" {
		t.Fatalf("rules: %d %v", code, body)
	}
	code, body = m.send(t, http.MethodPut, "/api/notifications/rules",
		`{"rules":{"torrents":{"ntfy":"off"}},"quiet":{"enabled":true,"start":"23:00","end":"07:00","critical":true}}`)
	if code != http.StatusOK || body["rules"].(map[string]any)["torrents"].(map[string]any)["ntfy"] != "off" || body["quiet"].(map[string]any)["enabled"] != true {
		t.Fatalf("save rules: %d %v", code, body)
	}
	if code, _ = m.send(t, http.MethodPut, "/api/notifications/rules", `{"rules":{"torrents":{"ntfy":"loud"}},"quiet":{"start":"23:00","end":"07:00"}}`); code != http.StatusBadRequest {
		t.Fatalf("a bad level should be refused, got %d", code)
	}

	// Mutes show in the bell's summary until undone.
	if code, body = m.send(t, http.MethodPost, "/api/notifications/mute", `{"subject":"unit:sonarr.service","label":"sonarr","minutes":60}`); code != http.StatusOK || len(body["mutes"].([]any)) != 1 {
		t.Fatalf("mute: %d %v", code, body)
	}
	if _, body = m.send(t, http.MethodGet, "/api/notifications/summary", ""); len(body["mutes"].([]any)) != 1 {
		t.Fatalf("summary: %v", body)
	}
	if _, body = m.send(t, http.MethodPost, "/api/notifications/mute", `{"subject":"unit:sonarr.service","minutes":0}`); len(body["mutes"].([]any)) != 0 {
		t.Fatalf("unmute: %v", body)
	}
	if code, _ = m.send(t, http.MethodPost, "/api/notifications/mute", `{"subject":"everything","minutes":60}`); code != http.StatusBadRequest {
		t.Fatalf("a bad subject should be refused, got %d", code)
	}
}

func TestLogsFollowTheMonitorsOwnLog(t *testing.T) {
	m := newManaged(t)
	logx.Component("Web").Warn("Failed sign-in", "ip", "203.0.113.9")
	code, body := m.send(t, http.MethodGet, "/api/logs?level=warn&q=203.0.113.9", "")
	entries := body["entries"].([]any)
	if code != http.StatusOK || len(entries) == 0 {
		t.Fatalf("logs: %d %v", code, body)
	}
	last := entries[len(entries)-1].(map[string]any)
	if last["message"] != "Failed sign-in" || last["component"] != "web" {
		t.Fatalf("entry: %v", last)
	}
	latest := body["latest"].(float64)
	if _, body = m.send(t, http.MethodGet, "/api/logs?after="+strconv.Itoa(int(latest)), ""); len(body["entries"].([]any)) != 0 {
		t.Fatalf("nothing newer yet: %v", body["entries"])
	}
}
