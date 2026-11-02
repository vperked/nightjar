package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDesignedStatusPage(t *testing.T) {
	m := newManaged(t)
	m.ctx.Cfg.StatusPageEnabled, m.ctx.Cfg.StatusPageTitle = true, "Service Status"
	for _, c := range []string{"plex", "nas-backup", "internal-db"} {
		if code, body := m.send(t, http.MethodPost, "/api/checks", `{"name":"`+c+`","type":"tcp","target":"127.0.0.1:1"}`); code != http.StatusCreated {
			t.Fatalf("add %s: %d %v", c, code, body)
		}
	}

	// Without a design, every check is listed and the editor starts from them all.
	code, body := m.send(t, http.MethodGet, "/api/statuspage", "")
	if code != http.StatusOK || body["designed"] != false || len(body["checks"].([]any)) != 3 {
		t.Fatalf("design: %d %v", code, body)
	}

	design := `{"title":"Perked status","description":"Media and home services.","announcement":"Plex maintenance tonight","tone":"warning",
		"show_uptime":true,"show_history":false,"footer":"Questions? Ask Jay.",
		"groups":[{"name":"Media","items":[{"check":"plex","label":"Plex"}]},{"name":"Storage","items":[{"check":"nas-backup","label":"Backups"}]}]}`
	if code, body = m.send(t, http.MethodPut, "/api/statuspage", design); code != http.StatusOK {
		t.Fatalf("save: %d %v", code, body)
	}

	req := func() map[string]any {
		_, b := m.send(t, http.MethodGet, "/api/status", "")
		return b
	}
	pub := req()
	raw, _ := json.Marshal(pub)
	if strings.Contains(string(raw), "internal-db") || strings.Contains(string(raw), "127.0.0.1") {
		t.Fatalf("checks left off the page, and targets, never reach it: %s", raw)
	}
	groups := pub["groups"].([]any)
	if pub["title"] != "Perked status" || len(groups) != 2 || groups[0].(map[string]any)["name"] != "Media" {
		t.Fatalf("groups: %v", pub)
	}
	plex := groups[0].(map[string]any)["services"].([]any)[0].(map[string]any)
	if plex["name"] != "Plex" || len(plex["recent"].([]any)) != 0 || pub["show_history"] != false {
		t.Fatalf("public names, and history hidden when asked: %v", plex)
	}
	if a := pub["announcement"].(map[string]any); a["text"] != "Plex maintenance tonight" || a["tone"] != "warning" {
		t.Fatalf("announcement %v", a)
	}

	// Renaming a check keeps it on the page.
	if code, _ := m.send(t, http.MethodPut, "/api/checks/plex", `{"name":"plex-server","type":"tcp","target":"127.0.0.1:1"}`); code != http.StatusOK {
		t.Fatalf("rename %d", code)
	}
	if g := req()["groups"].([]any)[0].(map[string]any); len(g["services"].([]any)) != 1 {
		t.Fatalf("renamed check fell off the page: %v", g)
	}

	for _, bad := range []string{
		`{"groups":[{"name":"A","items":[{"check":"plex-server"},{"check":"plex-server"}]}]}`,
		`{"tone":"party"}`,
		`{"title":"` + strings.Repeat("x", 81) + `"}`,
	} {
		if code, _ := m.send(t, http.MethodPut, "/api/statuspage", bad); code != http.StatusBadRequest {
			t.Fatalf("accepted %s", bad)
		}
	}
	if code, _ := m.send(t, http.MethodPut, "/api/statuspage", `{}`, "Origin", "https://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site design change: %d", code)
	}
}
