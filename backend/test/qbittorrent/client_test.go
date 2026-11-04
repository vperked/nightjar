package qbittorrent_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"nightjar/internal/qbittorrent"
)

// fakeQB is a minimal qBittorrent Web API: cookie login, torrent list and torrent actions.
type fakeQB struct {
	*httptest.Server
	mu       sync.Mutex
	list     string
	v5       bool // qBittorrent 5 has /start but no /resume
	actions  []string
	failList bool
	logins   int
}

func newFakeQB(t *testing.T) *fakeQB {
	f := &fakeQB{list: `[]`}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeQB) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/v2/auth/login" {
		f.logins++
		r.ParseForm()
		if r.Form.Get("password") != "secret" {
			w.Write([]byte("Fails."))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "ok", Path: "/"})
		w.Write([]byte("Ok."))
		return
	}
	if c, err := r.Cookie("SID"); err != nil || c.Value != "ok" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch path := r.URL.Path; {
	case path == "/api/v2/app/version":
		w.Write([]byte("v5.0.0"))
	case path == "/api/v2/torrents/info":
		if f.failList {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(f.list))
	case path == "/api/v2/torrents/resume" && f.v5:
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(path, "/api/v2/torrents/"):
		r.ParseForm()
		f.actions = append(f.actions, strings.TrimPrefix(path, "/api/v2/torrents/")+":"+r.Form.Get("hashes")+":"+r.Form.Get("deleteFiles"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeQB) loginAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeQB) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func TestNewValidatesCredentialsAndURL(t *testing.T) {
	if _, err := qbittorrent.New("localhost", 8080, "", "x"); err == nil {
		t.Fatal("empty username should fail")
	}
	if _, err := qbittorrent.New("localhost", 8080, "admin", ""); err == nil {
		t.Fatal("empty password should fail")
	}
	qb := newFakeQB(t)
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(qb.URL, "http://"))
	p, _ := strconv.Atoi(port)
	if c, err := qbittorrent.New(host, p, "admin", "secret"); err != nil || !c.Available() {
		t.Fatalf("a bare host should get http:// and the configured port (err %v)", err)
	}
	if c, _ := qbittorrent.New(qb.URL, 1, "admin", "secret"); !c.Available() {
		t.Fatal("a URL's own scheme and port should win over QBITTORRENT_PORT")
	}
}

func TestLoginAndList(t *testing.T) {
	qb := newFakeQB(t)
	qb.list = `[{"hash":"a","name":"A","state":"stalledDL","progress":0.5},{"hash":"b","name":"B","state":"uploading","progress":1,"ratio":2.5}]`
	c, _ := qbittorrent.New(qb.URL, 0, "admin", "secret")
	if !c.Available() {
		t.Fatalf("should connect: %s", c.LastError())
	}
	ts, err := c.List()
	if err != nil || len(ts) != 2 || ts[0].State != "stalleddl" {
		t.Fatalf("torrents %+v", ts)
	}
	if !qbittorrent.IsDownloadStalled(ts[0]) || qbittorrent.RemoveReason(ts[1], 2) != "ratio" || qbittorrent.RemoveReason(ts[1], 0) != "" {
		t.Fatal("policy helpers disagree with the listed torrents")
	}
	if s := qbittorrent.Summarize(ts); s.Total != 2 || s.Downloading != 1 || s.Seeding != 1 {
		t.Fatalf("summary %+v", s)
	}

	qb.failList = true
	if ts, err := c.List(); ts != nil || err == nil {
		t.Fatal("a failed fetch must be distinguishable from an empty list")
	}
}

func TestBadPasswordIsReported(t *testing.T) {
	qb := newFakeQB(t)
	c, _ := qbittorrent.New(qb.URL, 0, "admin", "wrong")
	if c.Available() || !strings.Contains(c.LastError(), "login failed") {
		t.Fatalf("available with a bad password? err=%q", c.LastError())
	}
}

func TestActionsFallBackForQBittorrent5(t *testing.T) {
	qb := newFakeQB(t)
	qb.v5 = true
	c, _ := qbittorrent.New(qb.URL, 0, "admin", "secret")
	if got := c.Recover("h1"); strings.Join(got, ",") != "reannounce,resume" {
		t.Fatalf("recover actions %v", got)
	}
	if got := c.Delete("h2", true); len(got) != 1 || got[0] != "delete_files" {
		t.Fatalf("delete actions %v", got)
	}
	want := []string{"reannounce:h1:", "start:h1:", "delete:h2:true"}
	if got := qb.recorded(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("server saw %v, want %v", got, want)
	}
	if c.Delete("", false) != nil {
		t.Fatal("an empty hash must never reach the API")
	}
}

func TestFailingLoginOpensBreaker(t *testing.T) {
	qb := newFakeQB(t)
	c, _ := qbittorrent.New(qb.URL, 0, "admin", "wrong")
	for range 8 {
		c.Available()
	}
	if got := qb.loginAttempts(); got != 5 {
		t.Fatalf("the breaker should stop after 5 failed logins, server saw %d", got)
	}
}
