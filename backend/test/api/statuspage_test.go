package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestStatusPageCustomCode(t *testing.T) {
	m := newManaged(t)
	m.ctx.Cfg.StatusPageEnabled = true
	design := `{"css":"  .status-page { max-width: 900px }\n","header_html":"<img src=\"https://example.com/logo.png\" alt=\"Perked\">","footer_html":"<a href=\"https://example.com\">Home</a>","groups":[]}`
	if code, body := m.send(t, http.MethodPut, "/api/statuspage", design); code != http.StatusOK {
		t.Fatalf("save: %d %v", code, body)
	}
	_, pub := m.send(t, http.MethodGet, "/api/status", "")
	if pub["css"] != ".status-page { max-width: 900px }" || !strings.Contains(pub["header_html"].(string), "logo.png") || pub["footer_html"] != `<a href="https://example.com">Home</a>` {
		t.Fatalf("the page carries the owner's code: %v", pub)
	}
	if code, _ := m.send(t, http.MethodPut, "/api/statuspage", `{"css":"`+strings.Repeat("x", 50001)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accepted CSS over the limit: %d", code)
	}

	// The page may load images and fonts from other sites; scripts stay its own, and the dashboard
	// keeps the strict policy.
	policy := func(path string) string {
		rec := httptest.NewRecorder()
		m.srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Header().Get("Content-Security-Policy")
	}
	if p := policy("/status"); !strings.Contains(p, "img-src 'self' data: https:") || !strings.Contains(p, "script-src 'self';") {
		t.Fatalf("status page policy: %s", p)
	}
	if p := policy("/"); strings.Contains(p, "https:") {
		t.Fatalf("dashboard policy loosened: %s", p)
	}
}

func TestStatusPageShowsNinetyDays(t *testing.T) {
	m := newManaged(t)
	m.ctx.Cfg.StatusPageEnabled = true
	if code, body := m.send(t, http.MethodPost, "/api/checks", `{"name":"web","type":"tcp","target":"127.0.0.1:1","interval":60}`); code != http.StatusCreated {
		t.Fatalf("add: %d %v", code, body)
	}
	// Yesterday: 100 checks, 10 failed. Three days ago: all fine. Nothing before.
	_, offset := time.Now().Zone()
	midnight := float64((time.Now().Unix()+int64(offset))/86400*86400 - int64(offset))
	for i := range 100 {
		m.store.Record("web", midnight-86400+float64(i*60), i >= 10, nil, "")
		m.store.Record("web", midnight-3*86400+float64(i*60), true, nil, "")
	}
	_, pub := m.send(t, http.MethodGet, "/api/status", "")
	web := pub["services"].([]any)[0].(map[string]any)
	days := web["days"].([]any)
	if len(days) != 90 {
		t.Fatalf("90 days, every one present: %d", len(days))
	}
	yesterday, threeAgo, today := days[88].(map[string]any), days[86].(map[string]any), days[89].(map[string]any)
	if yesterday["t"] != midnight-86400 || yesterday["uptime"] != 90.0 || yesterday["down_s"] != 600.0 {
		t.Fatalf("yesterday: %v", yesterday)
	}
	if threeAgo["uptime"] != 100.0 || today["uptime"] != nil || days[0].(map[string]any)["uptime"] != nil {
		t.Fatalf("days without checks have no uptime: %v %v", threeAgo, today)
	}
	if web["uptime_90d"] != 95.0 {
		t.Fatalf("uptime over the days is weighed by checks: %v", web["uptime_90d"])
	}
	// The designer previews with the same days.
	if _, d := m.send(t, http.MethodGet, "/api/statuspage", ""); len(d["days"].(map[string]any)["web"].([]any)) != 90 {
		t.Fatalf("designer days: %v", d["days"])
	}
}
