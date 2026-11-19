package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/accounts"
	"nightjar/internal/api"
	"nightjar/internal/collect"
	"nightjar/internal/config"
	"nightjar/internal/monitor"
	"nightjar/internal/storage"
)

// signedIn is a server with the admin account made, and a session cookie for it.
func signedIn(t *testing.T) (*api.Server, *http.Cookie) {
	t.Helper()
	db := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	token, err := acc.NewSession("test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &monitor.Context{Cfg: config.Settings{StatusPageEnabled: true}, Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
	s := api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: acc, AuthToken: "script-token-script-token", Store: db, FrontendDir: t.TempDir()}, ctx)
	return s, &http.Cookie{Name: "nightjar_session", Value: token}
}

func serve(s *api.Server, method, path, body, remote string, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Behind a proxy on this machine, a client cannot slip the sign-in limit by making up the header its
// proxy does not set, whichever of X-Real-IP and X-Forwarded-For that is.
func TestThrottleCannotBeDodgedWithMadeUpProxyHeaders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers func(i int) map[string]string
	}{
		// nginx as the docs set it up: X-Real-IP is the proxy's, X-Forwarded-For the client's own.
		{"nginx with X-Real-IP", func(i int) map[string]string {
			return map[string]string{"X-Real-IP": "203.0.113.7", "X-Forwarded-For": fmt.Sprintf("10.9.%d.%d", i/250, i%250)}
		}},
		// Caddy, cloudflared or nginx with $proxy_add_x_forwarded_for: the last hop is the proxy's.
		{"proxy with X-Forwarded-For", func(i int) map[string]string {
			return map[string]string{"X-Real-IP": fmt.Sprintf("10.9.%d.%d", i/250, i%250), "X-Forwarded-For": fmt.Sprintf("10.8.0.%d, 203.0.113.7", i%250)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := signedIn(t)
			for i := range 10 {
				if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"a wrong guess"}`, "127.0.0.1:5000", tc.headers(i), nil); rec.Code != http.StatusUnauthorized {
					t.Fatalf("guess %d: %d", i, rec.Code)
				}
			}
			if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", tc.headers(99), nil); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("the guessing client is not locked out: %d", rec.Code)
			}
			// Someone else behind the same proxy still signs in.
			if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", map[string]string{"X-Real-IP": "198.51.100.4"}, nil); rec.Code != http.StatusOK {
				t.Fatalf("another client: %d", rec.Code)
			}
		})
	}
}

// proxyEvidence is how many requests through the proxy the dashboard waits for before it believes
// which header the proxy leaves out.
const proxyEvidence = 16

// Once the proxy's own requests show which header it leaves out, a client that fills that header with
// the owner's address counts its failures only against itself: the owner still signs in.
func TestMadeUpProxyHeaderCannotLockOutSomeoneElse(t *testing.T) {
	const owner, attacker = "198.51.100.4", "203.0.113.66"
	for _, tc := range []struct {
		name  string
		plain map[string]string // what the proxy sends for a browser that adds nothing
		bad   map[string]string // the attacker's guesses, with the other header made up
		good  map[string]string // the owner signing in
	}{
		{"nginx with X-Real-IP",
			map[string]string{"X-Real-IP": owner},
			map[string]string{"X-Real-IP": attacker, "X-Forwarded-For": owner},
			map[string]string{"X-Real-IP": owner}},
		{"proxy with X-Forwarded-For",
			map[string]string{"X-Forwarded-For": owner},
			map[string]string{"X-Real-IP": owner, "X-Forwarded-For": owner + ", " + attacker},
			map[string]string{"X-Forwarded-For": owner}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cookie := signedIn(t)
			for range proxyEvidence {
				if rec := serve(s, "GET", "/api/config", "", "127.0.0.1:5000", tc.plain, cookie); rec.Code != http.StatusOK {
					t.Fatalf("status: %d", rec.Code)
				}
			}
			for i := range 10 {
				if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"a wrong guess"}`, "127.0.0.1:5000", tc.bad, nil); rec.Code != http.StatusUnauthorized {
					t.Fatalf("guess %d: %d", i, rec.Code)
				}
			}
			if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", tc.bad, nil); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("the guessing client is not locked out: %d", rec.Code)
			}
			if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", tc.good, nil); rec.Code != http.StatusOK {
				t.Fatalf("the owner is locked out by someone else's guesses: %d", rec.Code)
			}
		})
	}
}

// What the proxy was seen to leave out survives a restart, so a made-up header cannot lock the owner
// out in the time before the proxy's next plain request.
func TestLearnedProxyHeadersOutliveARestart(t *testing.T) {
	const owner, attacker = "198.51.100.4", "203.0.113.66"
	db := storage.Open(filepath.Join(t.TempDir(), "p.db"))
	t.Cleanup(db.Close)
	acc := accounts.New(db)
	if err := acc.Create("jay", "correct horse battery", acc.SetupCode()); err != nil {
		t.Fatal(err)
	}
	start := func() *api.Server {
		ctx := &monitor.Context{Processes: collect.NewProcessScanner(), Sensors: collect.NewSensors(), GPU: &collect.GPUReader{}, Bandwidth: &collect.BandwidthMeter{}}
		return api.New(api.Config{Host: "127.0.0.1", Port: "0", Accounts: accounts.New(db), Store: db, FrontendDir: t.TempDir()}, ctx)
	}
	first := start()
	for range proxyEvidence {
		serve(first, "GET", "/api/config", "", "127.0.0.1:5000", map[string]string{"X-Real-IP": owner}, nil)
	}
	for deadline := time.Now().Add(5 * time.Second); db.Meta("proxy.unset_headers") == ""; {
		if time.Now().After(deadline) {
			t.Fatal("what the proxy leaves out was not saved")
		}
		time.Sleep(10 * time.Millisecond)
	}

	s := start() // restarted: no plain request through the proxy yet
	bad := map[string]string{"X-Real-IP": attacker, "X-Forwarded-For": owner}
	for range 10 {
		serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"a wrong guess"}`, "127.0.0.1:5000", bad, nil)
	}
	if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", map[string]string{"X-Real-IP": owner}, nil); rec.Code != http.StatusOK {
		t.Fatalf("after a restart the owner is locked out by someone else's guesses: %d", rec.Code)
	}
}

// Sign-ins sent all at once cannot all pass the limit before the first failure is counted.
func TestConcurrentSignInsAreCountedBeforeTheyAreChecked(t *testing.T) {
	s, _ := signedIn(t)
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"a wrong guess"}`, "203.0.113.9:1234", nil, nil)
			mu.Lock()
			codes[rec.Code]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if codes[http.StatusUnauthorized] > 10 || codes[http.StatusTooManyRequests] < 20 {
		t.Fatalf("more guesses got checked than the limit allows: %v", codes)
	}
}

// Others sharing the admin's address cannot lock the admin out of a dashboard already open, and the
// public endpoints count wrong tokens like the rest.
func TestSessionsOutliveTheirAddressBeingLockedOut(t *testing.T) {
	s, cookie := signedIn(t)
	const ip = "198.51.100.20:1234"
	for range 10 {
		serve(s, "GET", "/api/config", "", ip, map[string]string{"Authorization": "Bearer a-guess"}, nil)
	}
	// The token is right, but the client guessing from here is locked out: config does not confirm it.
	rec := serve(s, "GET", "/api/config", "", ip, map[string]string{"Authorization": "Bearer script-token-script-token"}, nil)
	var cfg map[string]any
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg["authorized"] != false {
		t.Fatalf("a locked-out client learned the token is right: %v", cfg)
	}
	if rec := serve(s, "GET", "/api/services", "", ip, map[string]string{"Authorization": "Bearer script-token-script-token"}, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a locked-out token: %d", rec.Code)
	}
	if rec := serve(s, "GET", "/api/services", "", ip, nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("the admin's own session from the same address: %d", rec.Code)
	}
}

// Changing or deleting a check named like a try is still a change.
func TestActivityKeepsChecksNamedLikeTries(t *testing.T) {
	m := newManaged(t)
	for _, name := range []string{"test", "try"} {
		if code, body := m.send(t, http.MethodPost, "/api/checks", `{"name":"`+name+`","type":"tcp","target":"127.0.0.1:1"}`); code != http.StatusCreated {
			t.Fatalf("add %s: %d %v", name, code, body)
		}
		if code, _ := m.send(t, http.MethodDelete, "/api/checks/"+name, ""); code >= 400 {
			t.Fatalf("delete %s: %d", name, code)
		}
	}
	m.send(t, http.MethodPost, "/api/checks/test", `{"name":"x","type":"tcp","target":"127.0.0.1:1"}`)
	_, act := m.send(t, http.MethodGet, "/api/activity", "")
	var lines []string
	for _, a := range act["activity"].([]any) {
		lines = append(lines, a.(map[string]any)["action"].(string))
	}
	got := strings.Join(lines, " | ")
	for _, want := range []string{"Deleted the check test", "Deleted the check try"} {
		if !strings.Contains(got, want) {
			t.Errorf("activity lacks %q: %s", want, got)
		}
	}
	if strings.Count(got, "Added a check") != 2 {
		t.Errorf("trying a check out is not a change: %s", got)
	}
}

// A design within every per-field limit saves, though it is larger than most changes may be.
func TestLargeStatusPageDesignSaves(t *testing.T) {
	m := newManaged(t)
	design, _ := json.Marshal(map[string]any{
		"title": "Status", "css": strings.Repeat("a{color:red}", 4000), "header_html": strings.Repeat("<b>hi</b>", 2000), "footer_html": strings.Repeat("<i>yo</i>", 2000),
	})
	if len(design) <= 64<<10 {
		t.Fatalf("the design is only %d bytes", len(design))
	}
	if code, body := m.send(t, http.MethodPut, "/api/statuspage", string(design)); code != http.StatusOK {
		t.Fatalf("save: %d %v", code, body)
	}
	huge := `{"css":"` + strings.Repeat("a", 3<<20) + `"}`
	if code, body := m.send(t, http.MethodPut, "/api/statuspage", huge); code != http.StatusRequestEntityTooLarge || !strings.Contains(fmt.Sprint(body["error"]), "larger") {
		t.Fatalf("too large: %d %v", code, body)
	}
}

// Behind a proxy that sends its own Host, the page's origin is the one the proxy says it was asked
// for; anyone else's origin is still refused.
func TestOriginCheckBehindAProxy(t *testing.T) {
	m := newManaged(t)
	put := func(remote string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/appearance", strings.NewReader(`{}`))
		req.Host, req.RemoteAddr = "127.0.0.1:8080", remote
		req.Header.Set("X-Auth-Token", "tok")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		m.srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := put("127.0.0.1:40000", map[string]string{"Origin": "https://monitor.example.com", "X-Forwarded-Host": "monitor.example.com"}); rec.Code == http.StatusForbidden {
		t.Fatalf("the proxy's own page was refused: %s", rec.Body)
	}
	if rec := put("127.0.0.1:40000", map[string]string{"Origin": "https://evil.example", "X-Forwarded-Host": "monitor.example.com"}); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "proxy") {
		t.Fatalf("another site: %d %s", rec.Code, rec.Body)
	}
	// Only a proxy on this machine is believed.
	if rec := put("203.0.113.5:40000", map[string]string{"Origin": "https://monitor.example.com", "X-Forwarded-Host": "monitor.example.com"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a forwarded host from elsewhere: %d", rec.Code)
	}
	// A plain proxy_pass passes on neither; the browser still says the page is the dashboard's own.
	if rec := put("127.0.0.1:40000", map[string]string{"Origin": "https://monitor.example.com", "Sec-Fetch-Site": "same-origin"}); rec.Code == http.StatusForbidden {
		t.Fatalf("a same-origin page behind a plain proxy was refused: %s", rec.Body)
	}
	if rec := put("127.0.0.1:40000", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a cross-site page: %d", rec.Code)
	}
}

// The dashboard's stats leave the scanner's cached processes, which alerts read, as they were.
func TestStatsLeaveTheProcessCacheAlone(t *testing.T) {
	s, ctx := newServer(t, "", false)
	if rec, _ := get(t, s, "/api/stats", nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	top := ctx.Processes.Overview(5).TopMemory
	if len(top) == 0 {
		t.Skip("no processes to look at")
	}
	if top[0].RSS == 0 {
		t.Fatalf("the stats cleared the cached processes' memory: %+v", top[0])
	}
}

// A program on this machine can send the dashboard a request with whichever header it likes, as if
// through the proxy. One such request cannot teach the dashboard to trust a header the real proxy
// passes on untouched, before the proxy's own traffic has shown its habits or after.
func TestOneLocalRequestCannotTeachTheProxysHabits(t *testing.T) {
	const owner, attacker = "198.51.100.4", "203.0.113.66"
	// The real proxy sets X-Forwarded-For and passes X-Real-IP on as the client sent it.
	plain := map[string]string{"X-Forwarded-For": owner}
	poison := map[string]string{"X-Real-IP": "127.0.0.9"}
	bad := map[string]string{"X-Real-IP": owner, "X-Forwarded-For": owner + ", " + attacker}
	for _, tc := range []struct {
		name   string
		before int // the proxy's requests before the poisoned one
		after  int // and after it
	}{
		{"poisoned first", 0, proxyEvidence},
		{"poisoned once the proxy is known", proxyEvidence, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cookie := signedIn(t)
			for range tc.before {
				serve(s, "GET", "/api/config", "", "127.0.0.1:5000", plain, cookie)
			}
			serve(s, "GET", "/api/config", "", "127.0.0.1:5000", poison, nil)
			for range tc.after {
				serve(s, "GET", "/api/config", "", "127.0.0.1:5000", plain, cookie)
			}
			for i := range 10 {
				if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"a wrong guess"}`, "127.0.0.1:5000", bad, nil); rec.Code != http.StatusUnauthorized {
					t.Fatalf("guess %d: %d", i, rec.Code)
				}
			}
			if rec := serve(s, "POST", "/api/auth/login", `{"username":"jay","password":"correct horse battery"}`, "127.0.0.1:5000", plain, nil); rec.Code != http.StatusOK {
				t.Fatalf("the owner is locked out by someone else's guesses: %d", rec.Code)
			}
		})
	}
}
