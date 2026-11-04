package services_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

func spec(kind, target string) services.Spec {
	return services.Spec{Name: "svc", Type: kind, Target: target, Retries: 1, Timeout: 2, Interval: 60, VerifyTLS: true}
}

func TestParseSkipsInvalidAndDuplicates(t *testing.T) {
	var warnings []string
	specs, err := services.Parse([]byte(`{"services":[
		{"name":"a","type":"tcp","target":"host:22"},
		{"name":"a","type":"tcp","target":"host:23"},
		{"name":"b","type":"bogus","target":"x"},
		{"name":"c","type":"http","target":"ftp://nope"},
		{"name":"d","type":"ping","target":"bad host; rm -rf /"},
		{"target":"missing-name"}
	]}`), func(w string) { warnings = append(warnings, w) })
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "a" {
		t.Fatalf("got %+v", specs)
	}
	if len(warnings) != 5 {
		t.Fatalf("expected 5 warnings, got %v", warnings)
	}
}

func TestParseClampsAndDefaults(t *testing.T) {
	specs, _ := services.Parse([]byte(`[{"name":"a","type":"tcp","target":"h:1","interval":0,"timeout":999}]`), func(string) {})
	s := specs[0]
	if s.Interval != 5 || s.Timeout != 60 || s.Retries != 2 || !s.VerifyTLS || s.TLSWarnDays != 14 {
		t.Fatalf("got %+v", s)
	}
	if s.RetryInterval != 5 || s.PingCount != 3 || s.PingInterval != 0.2 {
		t.Fatalf("retry and ping defaults: got %+v", s)
	}
	specs, _ = services.Parse([]byte(`[{"name":"a","type":"ping","target":"h","interval":120,"retry_interval":1,"ping_count":50,"ping_interval":0.01}]`), func(string) {})
	s = specs[0]
	if s.RetryInterval != 5 || s.PingCount != 10 || s.PingInterval != 0.2 {
		t.Fatalf("retry and ping limits: got %+v", s)
	}
}

func TestHTTPCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
		}
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()
	cases := []struct {
		target, keyword string
		expected        []int
		up              bool
	}{
		{srv.URL, "", nil, true},
		{srv.URL + "/missing", "", nil, false},
		{srv.URL, "hello", nil, true},
		{srv.URL, "absent", nil, false},
		{srv.URL + "/missing", "", []int{404}, true},
	}
	for _, c := range cases {
		s := spec("http", c.target)
		s.Keyword, s.ExpectedStatus = c.keyword, c.expected
		if r := services.Run(s); r.Up != c.up {
			t.Errorf("%s keyword=%q: up=%v detail=%s", c.target, c.keyword, r.Up, r.Detail)
		}
	}
}

func TestHTTPCheckSeesRedirectOnlyWhenExpected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.Write([]byte("new page"))
	}))
	defer srv.Close()
	s := spec("http", srv.URL+"/old")
	if r := services.Run(s); !r.Up || r.Detail != "HTTP 200" {
		t.Fatalf("a redirect is followed by default: up=%v %s", r.Up, r.Detail)
	}
	s.ExpectedStatus = []int{301}
	if r := services.Run(s); !r.Up || r.Detail != "HTTP 301" {
		t.Fatalf("an expected redirect is checked itself: up=%v %s", r.Up, r.Detail)
	}
	s.Target, s.ExpectedStatus = srv.URL+"/new", []int{204}
	if r := services.Run(s); r.Up || r.Detail != "HTTP 200, expected 204" {
		t.Fatalf("got up=%v %s", r.Up, r.Detail)
	}
}

func TestHTTPCheckBypassesCachesAndConnectionReuse(t *testing.T) {
	var conns []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns = append(conns, r.RemoteAddr)
		if r.Header.Get("Cache-Control") != "no-cache" || !strings.Contains(r.UserAgent(), "Nightjar") {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	for range 2 {
		if r := services.Run(spec("http", srv.URL)); !r.Up {
			t.Fatalf("headers not sent: %s", r.Detail)
		}
	}
	if conns[0] == conns[1] {
		t.Fatal("each attempt should open its own connection")
	}
}

func TestHTTPCheckFailsWhenTheBodyBreaksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer srv.Close()
	if r := services.Run(spec("http", srv.URL)); r.Up || !strings.Contains(r.Detail, "broke off") {
		t.Fatalf("got up=%v %s", r.Up, r.Detail)
	}
}

func TestTLSCheckAcceptsURLTargets(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	// The test server's certificate is untrusted, so reaching the handshake shows the URL was understood.
	if r := services.Run(spec("tls", srv.URL+"/some/path")); r.Up || !strings.HasPrefix(r.Detail, "TLS error") {
		t.Fatalf("got up=%v %s", r.Up, r.Detail)
	}
}

func TestPingCheck(t *testing.T) {
	if _, err := exec.LookPath("ping"); err != nil {
		t.Skip("no ping command")
	}
	r := services.Run(spec("ping", "127.0.0.1"))
	if !r.Up {
		t.Skipf("ping not permitted here: %s", r.Detail)
	}
	if r.LatencyMS == nil || *r.LatencyMS > 100 {
		t.Fatalf("latency should be the round trip, got %v", r.LatencyMS)
	}
	s := spec("ping", "127.0.0.1")
	s.PingCount, s.PingInterval = 3, 0.5
	start := time.Now()
	if r := services.Run(s); !r.Up || time.Since(start) < time.Second {
		t.Fatalf("3 pings 0.5s apart should take at least 1s, took %s (%s)", time.Since(start), r.Detail)
	}
	if r := services.Run(spec("ping", "nightjar-test.invalid")); r.Up || !strings.HasPrefix(r.Detail, "nightjar-test.invalid:") {
		t.Fatalf("an unknown host should say so: up=%v %s", r.Up, r.Detail)
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if r := services.Run(spec("tcp", addr)); !r.Up {
		t.Fatalf("expected up: %s", r.Detail)
	}
	ln.Close()
	if r := services.Run(spec("tcp", addr)); r.Up {
		t.Fatal("expected down after close")
	}
	if r := services.Run(spec("tcp", "no-port")); r.Up || r.Detail == "" {
		t.Fatal("missing port should be down with a detail")
	}
}

func TestDownOnlyAfterRetries(t *testing.T) {
	s := spec("tcp", "h:1")
	s.Retries = 2
	m := services.NewMonitor([]services.Spec{s}, nil)
	fail, ok := services.Result{Detail: "x"}, services.Result{Up: true}
	for i, want := range []string{"", "", services.StatusDown, ""} {
		if got := m.Record("svc", fail, float64(i)); got != want {
			t.Fatalf("failure %d: got %q want %q", i, got, want)
		}
	}
	if got := m.Record("svc", ok, 10); got != services.StatusUp {
		t.Fatalf("recovery: got %q", got)
	}
}

func TestFirstSuccessIsNotRecovery(t *testing.T) {
	m := services.NewMonitor([]services.Spec{spec("tcp", "h:1")}, nil)
	if got := m.Record("svc", services.Result{Up: true}, 1); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestClaimRespectsInterval(t *testing.T) {
	m := services.NewMonitor([]services.Spec{spec("tcp", "h:1")}, nil)
	if len(m.Claim(100)) != 1 {
		t.Fatal("should be due initially")
	}
	if len(m.Claim(101)) != 0 {
		t.Fatal("a running check must not be claimed twice")
	}
	m.Record("svc", services.Result{Up: true}, 100)
	if len(m.Claim(120)) != 0 || len(m.Claim(160)) != 1 {
		t.Fatal("interval not respected")
	}
}

func TestFailedCheckIsRetriedSooner(t *testing.T) {
	s := spec("tcp", "h:1")
	s.Interval, s.Retries, s.RetryInterval = 300, 1, 30
	m := services.NewMonitor([]services.Spec{s}, nil)
	m.Claim(0)
	m.Record("svc", services.Result{Detail: "x"}, 0)
	if len(m.Claim(29)) != 0 || len(m.Claim(30)) != 1 {
		t.Fatal("a failure within retries should be re-checked after the retry interval")
	}
	m.Record("svc", services.Result{Detail: "x"}, 30)
	if m.State("svc").Status != services.StatusDown {
		t.Fatal("expected down")
	}
	if len(m.Claim(60)) != 0 {
		t.Fatal("a down check goes back to its normal interval")
	}
}

func TestHistoryPersistsAndRestoresStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	s := spec("tcp", "h:1")
	s.Retries = 0
	store := storage.Open(path)
	first := services.NewMonitor([]services.Spec{s}, store)
	base := float64(time.Now().Unix()) - 1000
	first.Record("svc", services.Result{Detail: "boom"}, base+100)
	store.Close()

	store = storage.Open(path)
	defer store.Close()
	second := services.NewMonitor([]services.Spec{s}, store)
	if st := second.State("svc"); st.Status != services.StatusDown {
		t.Fatalf("status not restored: %s", st.Status)
	}
	if got := second.Record("svc", services.Result{Detail: "boom"}, base+200); got != "" {
		t.Fatalf("still down should not re-alert, got %q", got)
	}
	if got := second.Record("svc", services.Result{Up: true}, base+300); got != services.StatusUp {
		t.Fatalf("got %q", got)
	}
	row := second.Summaries(base + 400)[0]
	if row.Uptime30d == nil || *row.Uptime30d < 33 || *row.Uptime30d > 34 {
		t.Fatalf("uptime %v", row.Uptime30d)
	}
	if len(row.Recent) != 3 {
		t.Fatalf("recent beats %d", len(row.Recent))
	}
}
