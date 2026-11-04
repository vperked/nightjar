package arr_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	arr.ConfigPaths = func(app string) []string {
		return []string{filepath.Join(filepath.Dir(filepath.Dir(dir)), ".config", app, "config.xml")}
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
