package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"servermonitor/internal/api"
	"servermonitor/internal/config"
	"servermonitor/internal/monitor"
	"servermonitor/internal/qbittorrent"
)

const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// fakeQBittorrent is a qBittorrent 4.x (no start/stop endpoints) that records each POST.
type fakeQBittorrent struct {
	mu    sync.Mutex
	posts []string
	state string
}

func (f *fakeQBittorrent) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		w.Write([]byte("Ok."))
	case "/api/v2/app/version":
		w.Write([]byte("v4.6.7"))
	case "/api/v2/torrents/info":
		w.Write([]byte(`[{"hash":"` + hashA + `","name":"a.iso","state":"` + f.state + `"},{"hash":"` + hashB + `","name":"b.iso","state":"uploading"}]`))
	case "/api/v2/torrents/categories":
		w.Write([]byte(`{"tv":{"name":"tv"},"movies":{"name":"movies"}}`))
	case "/api/v2/torrents/start", "/api/v2/torrents/stop":
		http.NotFound(w, r) // 4.x calls these resume and pause
	default:
		r.ParseForm()
		f.posts = append(f.posts, strings.TrimPrefix(r.URL.Path, "/api/v2/torrents/")+"?"+r.Form.Encode())
		if r.URL.Path == "/api/v2/torrents/pause" {
			f.state = "pauseddl"
		}
		w.Write([]byte("Ok."))
	}
}

func (f *fakeQBittorrent) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posts...)
}

func qbServer(t *testing.T) (http.Handler, *fakeQBittorrent) {
	fake := &fakeQBittorrent{state: "downloading"}
	upstream := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(upstream.Close)
	qb, _ := qbittorrent.New(upstream.URL, 0, "admin", "x")
	ctx := &monitor.Context{Cfg: config.LoadFrom(config.MapLookup(nil)), QBittorrent: qb, QBittorrentBoard: &qbittorrent.Board{}}
	srv := api.New(api.Config{Host: "127.0.0.1", Port: "0", AllowedOrigin: "null", FrontendDir: t.TempDir()}, ctx)
	return srv.Handler(), fake
}

func TestQBittorrentActions(t *testing.T) {
	h, fake := qbServer(t)
	code, body := call(t, h, http.MethodPost, "/api/qbittorrent/action", `{"action":"stop","hashes":["`+hashA+`","`+hashB+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("stop: %d %v", code, body)
	}
	// Falls back to 4.x's pause, sends both hashes, and returns the refreshed list.
	if got := fake.recorded(); len(got) != 1 || got[0] != "pause?hashes="+hashA+"%7C"+hashB {
		t.Fatalf("posts %v", got)
	}
	torrents := body["qbittorrent"].(map[string]any)["torrents"].([]any)
	if len(torrents) != 2 || torrents[0].(map[string]any)["state"] != "pauseddl" {
		t.Fatalf("the list is refreshed after an action: %v", torrents)
	}

	call(t, h, http.MethodPost, "/api/qbittorrent/action", `{"action":"delete","hashes":["`+hashB+`"],"delete_files":true}`)
	if got := fake.recorded(); got[1] != "delete?deleteFiles=true&hashes="+hashB {
		t.Fatalf("delete with files: %v", got)
	}
	for _, bad := range []string{`{"action":"delete","hashes":["all"]}`, `{"action":"delete","hashes":[]}`, `{"action":"format","hashes":["` + hashA + `"]}`} {
		if code, _ := call(t, h, http.MethodPost, "/api/qbittorrent/action", bad); code == http.StatusOK {
			t.Fatalf("accepted %s", bad)
		}
	}
	if len(fake.recorded()) != 2 {
		t.Fatalf("rejected actions must not reach qBittorrent: %v", fake.recorded())
	}
}

func TestQBittorrentAddAndCategories(t *testing.T) {
	h, fake := qbServer(t)
	code, body := call(t, h, http.MethodGet, "/api/qbittorrent/categories", "")
	if code != http.StatusOK || len(body["categories"].([]any)) != 2 || body["categories"].([]any)[0] != "movies" {
		t.Fatalf("categories: %d %v", code, body)
	}
	if code, body := call(t, h, http.MethodPost, "/api/qbittorrent/add", `{"urls":"magnet:?xt=urn:btih:`+hashA+`\n\n https://example.com/x.torrent ","category":"tv","stopped":true}`); code != http.StatusCreated || body["added"] != 2.0 {
		t.Fatalf("add: %d %v", code, body)
	}
	got := fake.recorded()
	if len(got) != 1 || !strings.Contains(got[0], "category=tv") || !strings.Contains(got[0], "stopped=true") || !strings.Contains(got[0], "%0Ahttps") {
		t.Fatalf("add posted %v", got)
	}
	if code, _ := call(t, h, http.MethodPost, "/api/qbittorrent/add", `{"urls":"file:///etc/passwd"}`); code != http.StatusBadRequest {
		t.Fatalf("only magnet links and http(s) URLs: %d", code)
	}
}

func TestQBittorrentRoutesWhenOff(t *testing.T) {
	h := updatesServer(t, "tok")
	if code, _ := call(t, h, http.MethodPost, "/api/qbittorrent/action", `{"action":"stop","hashes":["`+hashA+`"]}`); code != http.StatusServiceUnavailable {
		t.Fatalf("off: %d", code)
	}
}
