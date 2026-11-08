package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/statusproxy"
)

const statusDomain = "status.example.com"

// statusSite is a dashboard with a built page, the status page public, on a domain of its own and
// at /uptime. nginx is a temporary folder whose commands all succeed.
func statusSite(t *testing.T) (*api.Server, string) {
	t.Helper()
	front := t.TempDir()
	os.WriteFile(filepath.Join(front, "index.html"), []byte(`<!doctype html><html lang="en"><body>app</body></html>`), 0o644)
	os.MkdirAll(filepath.Join(front, "assets"), 0o755)
	os.WriteFile(filepath.Join(front, "assets", "app.js"), []byte("1"), 0o644)
	nginx := t.TempDir()
	os.MkdirAll(filepath.Join(nginx, "sites-available"), 0o755)
	os.MkdirAll(filepath.Join(nginx, "sites-enabled"), 0o755)
	ctx := &monitor.Context{
		Cfg:       config.Settings{StatusPageEnabled: true, StatusPageTitle: "Status", StatusPageDomain: statusDomain, StatusPagePath: "/uptime", WebPort: 8080},
		Processes: collect.NewProcessScanner(),
		Sensors:   collect.NewSensors(),
		GPU:       &collect.GPUReader{},
		Bandwidth: &collect.BandwidthMeter{},
	}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", AuthToken: "tok", FrontendDir: front, StatusProxy: func(o *statusproxy.Options) {
		o.NginxDir = nginx
		o.IsRoot = func() bool { return true }
		o.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
		o.Run = func(context.Context, string, ...string) (string, error) { return "", nil }
	}}, ctx)
	return s, nginx
}

func ask(s *api.Server, method, host, path, remote string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Host, req.RemoteAddr = host, remote
	req.Header.Set("X-Auth-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const marked = `data-page="status"`

// On its own domain the status page is the whole site: the page, its files and its data, and
// nothing of the dashboard, even with the API token.
func TestTheStatusDomainServesOnlyTheStatusPage(t *testing.T) {
	s, _ := statusSite(t)
	from := "203.0.113.5:4000"
	if rec := ask(s, "GET", statusDomain, "/", from, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), marked) {
		t.Fatalf("the page: %d %s", rec.Code, rec.Body)
	} else if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "https:") {
		t.Fatal("the status page may show the owner's images and fonts from other sites")
	}
	for _, p := range []string{"/assets/app.js", "/api/status"} {
		if rec := ask(s, "GET", statusDomain, p, from, nil); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/config", "/api/stats", "/api/settings", "/health"} {
		if rec := ask(s, "GET", statusDomain, p, from, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s on the status domain: %d", p, rec.Code)
		}
	}
	if rec := ask(s, "GET", statusDomain, "/settings", from, nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("a dashboard page there goes back to the status page: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := ask(s, "PUT", statusDomain, "/api/appearance", from, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a change through the status domain: %d", rec.Code)
	}
	if rec := ask(s, "POST", statusDomain, "/", from, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("anything but reading the page: %d", rec.Code)
	}
	// The same host with a port, as a proxy may pass it on, is the same domain.
	if rec := ask(s, "GET", statusDomain+":443", "/api/config", from, nil); rec.Code != http.StatusNotFound {
		t.Errorf("with a port: %d", rec.Code)
	}
}

// Behind a reverse proxy on this machine, the host it was asked for decides; a header made up by
// anyone else does not.
func TestTheStatusDomainBehindAProxy(t *testing.T) {
	s, _ := statusSite(t)
	fwd := map[string]string{"X-Forwarded-Host": statusDomain}
	if rec := ask(s, "GET", "127.0.0.1:8080", "/api/config", "127.0.0.1:5000", fwd); rec.Code != http.StatusNotFound {
		t.Errorf("through the proxy, the status domain shows no dashboard: %d", rec.Code)
	}
	if rec := ask(s, "GET", "127.0.0.1:8080", "/api/config", "203.0.113.5:5000", fwd); rec.Code != http.StatusOK {
		t.Errorf("a forwarded host from outside is not believed: %d", rec.Code)
	}
}

// On the dashboard's own address the status page is at the chosen path and at /status.
func TestTheStatusPathOnTheDashboard(t *testing.T) {
	s, _ := statusSite(t)
	for p, want := range map[string]bool{"/uptime": true, "/status": true, "/": false, "/services": false} {
		rec := ask(s, "GET", "monitor.example.com", p, "10.0.0.1:1", nil)
		if got := strings.Contains(rec.Body.String(), marked); rec.Code != http.StatusOK || got != want {
			t.Errorf("%s: %d, marked %v", p, rec.Code, got)
		}
	}
	if rec := ask(s, "GET", "monitor.example.com", "/api/config", "10.0.0.1:1", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status_page_path":"/uptime"`) {
		t.Errorf("the config says where it is: %s", rec.Body)
	}
}

func TestSettingUpNginxForTheStatusDomain(t *testing.T) {
	s, nginx := statusSite(t)
	rec := ask(s, "POST", "monitor.example.com", "/api/statuspage/proxy", "10.0.0.1:1", nil)
	var r struct {
		OK    bool               `json:"ok"`
		Steps []statusproxy.Step `json:"steps"`
		State statusproxy.State  `json:"state"`
	}
	json.Unmarshal(rec.Body.Bytes(), &r)
	if rec.Code != http.StatusOK || !r.OK || !r.State.Installed {
		t.Fatalf("set up: %d %s", rec.Code, rec.Body)
	}
	site, err := os.ReadFile(filepath.Join(nginx, "sites-available", "nightjar-status-"+statusDomain+".conf"))
	if err != nil || !strings.Contains(string(site), "server_name "+statusDomain+";") || !strings.Contains(string(site), "proxy_pass http://127.0.0.1:8080;") {
		t.Fatalf("the site: %s %v", site, err)
	}
	if _, err := os.Lstat(filepath.Join(nginx, "sites-enabled", "nightjar-status-"+statusDomain+".conf")); err != nil {
		t.Fatal("the site is enabled")
	}
	rec = ask(s, "DELETE", "monitor.example.com", "/api/statuspage/proxy", "10.0.0.1:1", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("take it out: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(nginx, "sites-available", "nightjar-status-"+statusDomain+".conf")); !os.IsNotExist(err) {
		t.Fatal("the site is gone")
	}
}
