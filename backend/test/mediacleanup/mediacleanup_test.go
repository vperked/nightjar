package mediacleanup_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/arr"
	"nightjar/internal/config"
	"nightjar/internal/discord"
	"nightjar/internal/mediacleanup"
	"nightjar/internal/plex"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/storage"
)

const day = 86400

// world is a fake Plex, qBittorrent and Radarr, and what was asked of them.
type world struct {
	mu      sync.Mutex
	now     time.Time
	ratio   float64 // of the never-watched movie's torrent
	calls   []string
	history string
}

func (w *world) log(s string) {
	w.mu.Lock()
	w.calls = append(w.calls, s)
	w.mu.Unlock()
}

func (w *world) plex(t *testing.T) *httptest.Server {
	added := func(daysAgo int) int64 { return w.now.Unix() - int64(daysAgo)*day }
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			w.log("plex delete " + r.URL.Path)
		case r.URL.Path == "/status/sessions/history/all":
			if w.history == "fail" {
				rw.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Someone watched an episode of show 30, so the show stays.
			fmt.Fprint(rw, `{"MediaContainer":{"Metadata":[{"ratingKey":"31","parentRatingKey":"301","grandparentRatingKey":"30"}]}}`)
		case r.URL.Path == "/library/sections":
			fmt.Fprint(rw, `{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"},{"key":"2","title":"TV","type":"show"},{"key":"3","title":"Music","type":"artist"}]}}`)
		case r.URL.Path == "/library/sections/1/all":
			fmt.Fprintf(rw, `{"MediaContainer":{"Metadata":[
 {"ratingKey":"10","type":"movie","title":"Heat","year":1995,"addedAt":%d,"Guid":[{"id":"tmdb://949"}],"Media":[{"Part":[{"file":"/media/movies/Heat (1995)/Heat.mkv","size":12000000000}]}]},
 {"ratingKey":"11","type":"movie","title":"Seen It","year":2001,"addedAt":%d,"viewCount":1},
 {"ratingKey":"12","type":"movie","title":"New Arrival","year":2024,"addedAt":%d},
 {"ratingKey":"13","type":"movie","title":"Plex Only","year":1980,"addedAt":%d,"Media":[{"Part":[{"file":"/media/other/x.mkv","size":1000}]}]}]}}`,
				added(90), added(90), added(10), added(90))
		case r.URL.Path == "/library/sections/2/all":
			fmt.Fprintf(rw, `{"MediaContainer":{"Metadata":[{"ratingKey":"30","type":"show","title":"Watched Show","addedAt":%d}]}}`, added(200))
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (w *world) qb(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(rw, &http.Cookie{Name: "SID", Value: "ok", Path: "/"})
			fmt.Fprint(rw, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(rw, "v5.0.0")
		case "/api/v2/torrents/info":
			w.mu.Lock()
			ratio := w.ratio
			w.mu.Unlock()
			fmt.Fprintf(rw, `[{"hash":"aaaa000000000000000000000000000000000000","name":"Heat.1995.1080p.BluRay","ratio":%g,"size":12000000000},
 {"hash":"bbbb000000000000000000000000000000000000","name":"Plex.Only.1980.DVDRip","ratio":3.1,"size":1000}]`, ratio)
		case "/api/v2/torrents/delete":
			r.ParseForm()
			w.log("qb delete " + r.Form.Get("hashes")[:4] + " files=" + r.Form.Get("deleteFiles"))
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (w *world) radarr(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "key" {
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			fmt.Fprint(rw, `[{"id":7,"title":"Heat","tmdbId":949,"path":"/media/movies/Heat (1995)","sizeOnDisk":12000000000}]`)
		case r.URL.Path == "/api/v3/history/movie":
			fmt.Fprint(rw, `[{"downloadId":"AAAA000000000000000000000000000000000000","eventType":"downloadFolderImported"}]`)
		case strings.HasPrefix(r.URL.Path, "/api/v3/movie/") && r.Method == http.MethodDelete:
			w.log("radarr delete " + r.URL.Path + "?" + r.URL.RawQuery)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
}

type sent struct {
	mu     sync.Mutex
	titles []string
}

func (s *sent) SendEmbed(title, _ string, _ discord.Severity, _ []discord.Field, _ string) bool {
	s.mu.Lock()
	s.titles = append(s.titles, title)
	s.mu.Unlock()
	return true
}

func TestCleanup(t *testing.T) {
	w := &world{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local), ratio: 0.4}
	px, qs, rs := w.plex(t), w.qb(t), w.radarr(t)
	defer px.Close()
	defer qs.Close()
	defer rs.Close()
	store := storage.Open(":memory:")
	defer store.Close()
	cfg := config.Settings{PlexMonitor: true, MediaCleanup: false, MediaCleanupDays: 60, MediaCleanupNoticeDays: 7, MediaCleanupRatio: 1.0, MediaCleanupRatioDays: 30}
	discordSent := &sent{}
	m := &mediacleanup.Manager{Store: store, Config: func() config.Settings { return cfg }, Send: discordSent, Now: func() time.Time { return w.now },
		Connect: func(config.Settings) mediacleanup.Sources {
			p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
			q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
			return mediacleanup.Sources{Plex: p, QB: q, Radarr: arr.New("radarr", rs.URL, "key")}
		}}
	ctx := context.Background()
	status := func(title string) string {
		for _, it := range m.View().Items {
			if it.Title == title {
				return it.Status
			}
		}
		return ""
	}

	// Off: a preview of what qualifies, and nothing is removed or announced.
	if err := m.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	v := m.View()
	if len(v.Items) != 2 || status("Heat") != mediacleanup.Preview || status("Plex Only") != mediacleanup.Preview {
		t.Fatalf("only the old, never-watched movies qualify (not one watched, one new, a show with a watched episode): %+v", v.Items)
	}
	heat := v.Items[slices(v.Items, "Heat")]
	if heat.Manager != "radarr" || len(heat.Torrents) != 1 || !strings.HasPrefix(heat.Torrents[0].Hash, "aaaa") {
		t.Fatalf("Heat is Radarr's, from the torrent Radarr imported: %+v", heat)
	}
	if plexOnly := v.Items[slices(v.Items, "Plex Only")]; plexOnly.Manager != "plex" || len(plexOnly.Torrents) != 1 {
		t.Fatalf("not Radarr's: Plex removes it, and its torrent is found by name and year: %+v", plexOnly)
	}

	// On: both are listed, to leave in a week, and Discord is told once.
	cfg.MediaCleanup = true
	m.Scan(ctx)
	if status("Heat") != mediacleanup.Leaving || len(discordSent.titles) != 1 || !strings.Contains(discordSent.titles[0], "Leaving Plex on") {
		t.Fatalf("listed: %v %v", status("Heat"), discordSent.titles)
	}
	m.Scan(ctx)
	if len(discordSent.titles) != 1 {
		t.Fatalf("told once: %v", discordSent.titles)
	}
	// The owner keeps Plex Only.
	if err := m.Keep(m.View().Items[slices(m.View().Items, "Plex Only")].Key, true); err != nil {
		t.Fatal(err)
	}

	// A week on, Heat's torrent has not seeded to 1.0: it waits.
	w.now = w.now.Add(8 * day * time.Second)
	m.Scan(ctx)
	if status("Heat") != mediacleanup.Waiting || len(w.calls) != 0 {
		t.Fatalf("waiting for the ratio: %v %v", status("Heat"), w.calls)
	}
	if status("Plex Only") != "" || len(m.View().Kept) != 1 {
		t.Fatalf("kept is off the list: %+v", m.View())
	}

	// It reaches 1.2: the torrent goes with its files, then Radarr deletes the movie and excludes it.
	w.ratio = 1.2
	m.Scan(ctx)
	if status("Heat") != mediacleanup.Removed {
		t.Fatalf("removed: %+v", m.View().Items)
	}
	want := []string{"qb delete aaaa files=true", "radarr delete /api/v3/movie/7?deleteFiles=true&addImportExclusion=true"}
	if strings.Join(w.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v, want %v", w.calls, want)
	}
	if v := m.View(); v.Freed != 12000000000 || !strings.Contains(discordSent.titles[len(discordSent.titles)-1], "Removed from Plex: 1 title") {
		t.Fatalf("freed %d, %v", v.Freed, discordSent.titles)
	}

	// Without the watch history, nothing is listed or removed.
	w.history = "fail"
	if err := m.Scan(ctx); err == nil || !strings.Contains(err.Error(), "watch history") {
		t.Fatalf("no history: %v", err)
	}
}

func TestGiveUpOnRatio(t *testing.T) {
	w := &world{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local), ratio: 0.2}
	px, qs := w.plex(t), w.qb(t)
	defer px.Close()
	defer qs.Close()
	store := storage.Open(":memory:")
	defer store.Close()
	cfg := config.Settings{PlexMonitor: true, MediaCleanup: true, MediaCleanupDays: 60, MediaCleanupNoticeDays: 7, MediaCleanupRatio: 1.0, MediaCleanupRatioDays: 30, MediaCleanupLibraries: []string{"movies"}}
	m := &mediacleanup.Manager{Store: store, Config: func() config.Settings { return cfg }, Now: func() time.Time { return w.now },
		Connect: func(config.Settings) mediacleanup.Sources {
			p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
			q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
			return mediacleanup.Sources{Plex: p, QB: q}
		}}
	m.Scan(context.Background())
	w.now = w.now.Add(20 * day * time.Second)
	m.Scan(context.Background())
	if got := strings.Join(w.calls, "|"); got != "qb delete bbbb files=true|plex delete /library/metadata/13" {
		t.Fatalf("Plex Only seeded past 1.0 goes; Heat waits: %v", w.calls)
	}
	// 30 days after it was due, Heat goes anyway, through Plex without Radarr.
	w.now = w.now.Add(20 * day * time.Second)
	m.Scan(context.Background())
	if got := strings.Join(w.calls, "|"); !strings.Contains(got, "qb delete aaaa files=true|plex delete /library/metadata/10") {
		t.Fatalf("gave up waiting: %v", w.calls)
	}
}

func slices(items []mediacleanup.Item, title string) int {
	for i, it := range items {
		if it.Title == title {
			return i
		}
	}
	return -1
}

func TestHurryWhenTheDiskIsFull(t *testing.T) {
	w := &world{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local), ratio: 5}
	px, qs := w.plex(t), w.qb(t)
	defer px.Close()
	defer qs.Close()
	store := storage.Open(":memory:")
	defer store.Close()
	old := mediacleanup.DiskUsed
	mediacleanup.DiskUsed = func(path string) (float64, bool) { return 96, strings.HasPrefix(path, "/media/movies") }
	defer func() { mediacleanup.DiskUsed = old }()
	cfg := config.Settings{PlexMonitor: true, MediaCleanup: true, MediaCleanupDays: 60, MediaCleanupNoticeDays: 7, MediaCleanupRatio: 1, MediaCleanupRatioDays: 30, MediaCleanupDiskFull: 90}
	m := &mediacleanup.Manager{Store: store, Config: func() config.Settings { return cfg }, Now: func() time.Time { return w.now },
		Connect: func(config.Settings) mediacleanup.Sources {
			p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
			q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
			return mediacleanup.Sources{Plex: p, QB: q}
		}}
	m.Scan(context.Background())
	var heat mediacleanup.Item
	for _, it := range m.View().Items {
		if it.Title == "Heat" {
			heat = it
		}
	}
	if heat.LeavingAt != float64(w.now.Unix())+day || !strings.Contains(heat.Reason, "96% full") {
		t.Fatalf("a full disk hurries it: %+v", heat)
	}
	// A day later it goes; the movie on the other disk keeps its week.
	w.now = w.now.Add(25 * time.Hour)
	m.Scan(context.Background())
	if got := strings.Join(w.calls, "|"); !strings.Contains(got, "plex delete /library/metadata/10") || strings.Contains(got, "metadata/13") {
		t.Fatalf("calls %v", w.calls)
	}
}
