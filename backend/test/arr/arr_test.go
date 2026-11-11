package arr_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"nightjar/internal/arr"
)

func TestDiscoverAndReplace(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "abc123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodGet && r.URL.Path == "/sonarr/api/v3/queue" {
			fmt.Fprint(w, `{"records":[{"id":5,"title":"Show.S01E01","downloadId":"ABCDEF0000000000000000000000000000000000"}]}`)
		}
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	dir := filepath.Join(t.TempDir(), ".config", "Sonarr")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.xml"), []byte("<Config><Port>"+port+"</Port><UrlBase>/sonarr</UrlBase><ApiKey>abc123</ApiKey></Config>"), 0o600)
	arr.ConfigPaths = func(app string) []arr.Candidate {
		return []arr.Candidate{{Path: filepath.Join(filepath.Dir(filepath.Dir(dir)), ".config", app, "config.xml")}}
	}

	f, ok := arr.Discover("sonarr")
	if !ok || f.URL != srv.URL+"/sonarr" {
		t.Fatalf("found %+v %v", f, ok)
	}
	if _, ok := arr.Discover("radarr"); ok {
		t.Fatal("no Radarr here")
	}
	c := arr.Connect("sonarr", "", "")
	q, ok, err := c.Queued(context.Background(), "abcdef0000000000000000000000000000000000")
	if err != nil || !ok || q.ID != 5 {
		t.Fatalf("queued %+v %v %v", q, ok, err)
	}
	if err := c.Replace(context.Background(), q.ID); err != nil {
		t.Fatal(err)
	}
	if last := calls[len(calls)-1]; last != "DELETE /sonarr/api/v3/queue/5?removeFromClient=true&blocklist=true&skipRedownload=false" {
		t.Fatalf("replace: %s", last)
	}
	// Settings win over what is found.
	if c := arr.Connect("sonarr", "http://elsewhere:8989", "k"); c == nil {
		t.Fatal("set by hand")
	}
}

// Only a plain config.xml belonging to root, Nightjar's account or the app's own account is
// believed: one someone else wrote could point the cleanup at a fake Sonarr. A FIFO in its place
// does not hang the search, and a symlink is not followed.
func TestDiscoverTrustsOnlyTheAppsOwnConfig(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give files to other accounts")
	}
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody account")
	}
	uid, _ := strconv.Atoi(nobody.Uid)
	dir := t.TempDir()
	conf := []byte("<Config><Port>8989</Port><ApiKey>k</ApiKey></Config>")
	foreign := filepath.Join(dir, "foreign.xml")
	os.WriteFile(foreign, conf, 0o644)
	os.Chown(foreign, uid, -1)
	fifo := filepath.Join(dir, "fifo.xml")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real.xml")
	os.WriteFile(real, conf, 0o600)
	link := filepath.Join(dir, "link.xml")
	os.Symlink(real, link)
	defer func(old func(string) []arr.Candidate) { arr.ConfigPaths = old }(arr.ConfigPaths)

	for _, c := range []arr.Candidate{{Path: foreign}, {Path: foreign, User: "root"}, {Path: fifo}, {Path: link}} {
		arr.ConfigPaths = func(string) []arr.Candidate { return []arr.Candidate{c} }
		done := make(chan bool, 1)
		go func() { _, ok := arr.Discover("sonarr"); done <- ok }()
		select {
		case ok := <-done:
			if ok {
				t.Errorf("believed %+v", c)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("hung on %+v", c)
		}
	}
	// The app's own account may own it.
	arr.ConfigPaths = func(string) []arr.Candidate { return []arr.Candidate{{Path: foreign, User: "nobody"}} }
	if f, ok := arr.Discover("sonarr"); !ok || f.From != foreign {
		t.Fatalf("the app's own config: %+v %v", f, ok)
	}
}

// An answer bigger than the most read says so, rather than failing as broken JSON.
func TestTooLargeAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":1,"title":"`+strings.Repeat("x", 2000)+`"}]`)
	}))
	defer srv.Close()
	defer func(old int64) { arr.MaxAnswer = old }(arr.MaxAnswer)
	arr.MaxAnswer = 1000
	_, err := arr.New("radarr", srv.URL, "k").List(context.Background())
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	arr.MaxAnswer = 1 << 20
	if list, err := arr.New("radarr", srv.URL, "k").List(context.Background()); err != nil || len(list) != 1 {
		t.Fatalf("list %v, err %v", list, err)
	}
}
