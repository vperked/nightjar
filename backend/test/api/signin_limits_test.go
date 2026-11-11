package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// cookieNamed is the cookie of that name a response set, or nil.
func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// A client on this machine names its own address, so it can take a new one for every guess. Wrong
// passwords also count against the account: past the free tries, its sign-ins are held back whatever
// the address, but not for a browser that signed in to it before.
func TestAccountIsHeldBackWhateverTheAddress(t *testing.T) {
	s, _ := signedIn(t)
	right := `{"username":"jay","password":"correct horse battery"}`
	rec := serve(s, "POST", "/api/auth/login", right, "127.0.0.1:5000", map[string]string{"X-Real-IP": "198.51.100.4"}, nil)
	device := cookieNamed(rec, "nightjar_device")
	if rec.Code != http.StatusOK || device == nil || device.Path != "/api/auth/" || !device.HttpOnly {
		t.Fatalf("sign-in: %d, device cookie %+v", rec.Code, device)
	}
	if session := cookieNamed(rec, "nightjar_session"); session == nil || session.MaxAge != int(accounts.SessionMaxAge.Seconds()) {
		t.Fatalf("the session cookie should last as long as the session can: %+v", session)
	}

	fresh := func(i int) map[string]string {
		return map[string]string{"X-Real-IP": fmt.Sprintf("10.9.%d.%d", i/250, i%250)}
	}
	tries := 0
	for ; tries < 40; tries++ {
		rec := serve(s, "POST", "/api/auth/login", `{"username":" JAY ","password":"a wrong guess"}`, "127.0.0.1:5000", fresh(tries), nil)
		if rec.Code == http.StatusTooManyRequests {
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", tries, rec.Code)
		}
	}
	if tries < 10 || tries >= 40 {
		t.Fatalf("held back after %d guesses from as many addresses", tries)
	}
	// Now even the right password, from yet another address, is held back.
	rec = serve(s, "POST", "/api/auth/login", right, "127.0.0.1:5000", fresh(99), nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "account") {
		t.Fatalf("the account is not held back: %d %s", rec.Code, rec.Body)
	}
	// The owner's browser, known by its device cookie, still signs in.
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(right))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "198.51.100.4")
	req.AddCookie(device)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the owner's own browser is kept out: %d %s", rec.Code, rec.Body)
	}
	// Another account is not held back by guesses at this one.
	if rec := serve(s, "POST", "/api/auth/login", `{"username":"sam","password":"a wrong guess"}`, "127.0.0.1:5000", fresh(98), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("another name: %d", rec.Code)
	}
}

// Guessing the setup code is held back the same way.
func TestSetupCodeIsHeldBackWhateverTheAddress(t *testing.T) {
	db := storage.Open(filepath.Join(t.TempDir(), "s.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	ctx := &monitor.Context{Cfg: config.Settings{}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, Store: db, FrontendDir: t.TempDir()}, ctx)
	body := func(code string) string {
		return `{"username":"jay","password":"correct horse battery","setup_code":"` + code + `"}`
	}
	held := false
	for i := range 40 {
		rec := serve(s, "POST", "/api/auth/setup", body("00000-00000"), "127.0.0.1:5000", map[string]string{"X-Real-IP": fmt.Sprintf("10.8.0.%d", i)}, nil)
		if rec.Code == http.StatusTooManyRequests {
			held = true
			break
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("guess %d: %d", i, rec.Code)
		}
	}
	if !held {
		t.Fatal("guessing the setup code from ever new addresses is never held back")
	}
	if rec := serve(s, "POST", "/api/auth/setup", body(acc.SetupCode()), "127.0.0.1:5000", map[string]string{"X-Real-IP": "10.7.0.1"}, nil); rec.Code != http.StatusTooManyRequests || acc.Configured() {
		t.Fatalf("the right code while held back: %d", rec.Code)
	}
}

// One IPv6 machine has a whole /64 to send from: its failures count against the /64.
func TestFailuresCountPerIPv6Network(t *testing.T) {
	s, _ := newServer(t, "secret", false)
	try := func(remote, token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
		req.RemoteAddr = remote
		req.Header.Set("X-Auth-Token", token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 10 {
		if code := try(fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1), "guess"); code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	if code := try("[2001:db8:1:2:aaaa::1]:4000", "secret"); code != http.StatusTooManyRequests {
		t.Fatalf("another address in the same /64: %d", code)
	}
	if code := try("[2001:db8:1:3::1]:4000", "secret"); code != http.StatusOK {
		t.Fatalf("another /64: %d", code)
	}
}

// A dashboard listening only on this machine refuses a request whose Host names some other site with
// the dashboard's own port: that is a page rebound by DNS to 127.0.0.1. Addresses, localhost, names in
// the settings and whatever came through a proxy on another port still work.
func TestUnknownHostIsRefusedOnALoopbackDashboard(t *testing.T) {
	newHosted := func(listen string) *api.Server {
		ctx := &monitor.Context{Cfg: config.Settings{DashboardURL: "https://Monitor.Example.com:8080/", StatusPageDomain: "status.example.com"},
			Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
		return api.New(api.Config{Host: listen, Port: "8080", AuthToken: "secret", FrontendDir: t.TempDir()}, ctx)
	}
	ask := func(s *api.Server, host string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.Host, req.RemoteAddr = host, "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	s := newHosted("127.0.0.1")
	refused := map[string]bool{"evil.example:8080": true, "EVIL.example.:8080": true}
	for _, host := range []string{
		"evil.example:8080", "EVIL.example.:8080",
		"127.0.0.1:8080", "[::1]:8080", "192.168.1.20:8080", "localhost:8080", "nightjar.localhost:8080",
		"monitor.example.com:8080", // DASHBOARD_URL's
		"status.example.com:8080",  // STATUS_PAGE_DOMAIN's
		"monitor.home.arpa",        // through nginx's proxy_set_header Host $host
		"monitor.home.arpa:8443",   // through a proxy on another port
		"",
	} {
		if got := ask(s, host); (got == http.StatusForbidden) != refused[host] {
			t.Errorf("Host %q: %d, refused should be %v", host, got, refused[host])
		}
	}
	// Listening on the network, it is reached by name anyway.
	if got := ask(newHosted("0.0.0.0"), "evil.example:8080"); got != http.StatusOK {
		t.Errorf("a dashboard listening on every address refused a host name: %d", got)
	}
}

// hours=NaN or Inf parse as numbers, but cannot be written back as JSON.
func TestServiceHistoryRefusesNonFiniteHours(t *testing.T) {
	s, ctx := newServer(t, "", false)
	ctx.Services = services.NewMonitor([]services.Spec{{Name: "api", Type: "tcp", Target: "h:1", Interval: 60}}, nil)
	for _, h := range []string{"NaN", "Inf", "-Inf", "+Inf"} {
		if rec, _ := get(t, s, "/api/services/history?name=api&hours="+h, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("hours=%s: %d", h, rec.Code)
		}
	}
	if rec, _ := get(t, s, "/api/services/history?name=api&hours=2", nil); rec.Code != http.StatusOK {
		t.Errorf("hours=2: %d", rec.Code)
	}
}
