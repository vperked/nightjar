package services_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"servermonitor/internal/services"
	"servermonitor/internal/storage"
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
