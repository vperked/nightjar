package mediacleanup

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
	"nightjar/internal/plex"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/storage"
)

// A torrent is matched by name only when its name has the title as whole words and, for a movie,
// the year. That decides which torrents go with an item, so near misses must not match.
func TestSameTitleDecisions(t *testing.T) {
	cases := []struct {
		name, title string
		year        int
		want        bool
	}{
		{"Alien.1979.1080p.BluRay.x264-AMIABLE", "Alien", 1979, true},
		{"Alien (1979) Director's Cut [1080p]", "Alien", 1979, true},
		// A collection with the title and year in its name matches by name; holdsOnly is what keeps
		// its files (TestHoldsOnlyDecisions).
		{"Alien Quadrilogy 1979-1997 1080p", "Alien", 1979, true},
		{"Aliens.1986.Special.Edition.1080p", "Alien", 1979, false}, // another word
		{"Alien.Romulus.2024.2160p", "Alien", 1979, false},          // another year
		{"Alien.1979.1080p", "Alien", 0, true},                      // a show: no year to check
		{"Heat.1995.1080p", "Heat", 1995, true},
		{"Heatwave.2022.1080p", "Heat", 2022, false}, // not the whole word
		{"Up.2009.1080p", "Up", 2009, false},         // too short to trust
		{"Her.2013.1080p", "Her", 2013, false},
		{"Blade.Runner.2049.2017.2160p", "Blade Runner", 1982, false},
		{"Blade.Runner.2049.2017.2160p", "Blade Runner 2049", 2017, true},
		{"1917.2019.1080p", "1917", 2019, true},
		{"2012.1080p.BluRay", "2012", 2009, false},
		{"The.Office.US.S01-S09.1080p", "The Office (US)", 0, true},
		{"Star.Wars.1977.1080p", "Star Wars: Episode IV - A New Hope", 1977, false},
		{"", "Alien", 1979, false},
		{"Alien.1979", "", 0, false},
	}
	for _, c := range cases {
		if got := sameTitle(c.name, c.title, c.year); got != c.want {
			t.Errorf("sameTitle(%q, %q, %d) = %v, want %v", c.name, c.title, c.year, got, c.want)
		}
	}
}

// holdsOnly decides whether a torrent found by name is removed WITH its files. It must be true only
// when the torrent's content is the item's file or a folder holding all of it, and not much bigger.
func TestHoldsOnlyDecisions(t *testing.T) {
	const gb = int64(1_000_000_000)
	movie := &Item{Size: 10 * gb, files: []string{"/media/movies/Alien (1979)/Alien.mkv"}}
	show := &Item{Size: 20 * gb, files: []string{"/media/tv/Show/Season 1/e1.mkv", "/media/tv/Show/Season 2/e1.mkv"}}
	cases := []struct {
		why     string
		it      *Item
		content string
		size    int64
		want    bool
	}{
		{"its folder", movie, "/media/movies/Alien (1979)", 10 * gb, true},
		{"its folder with a trailing slash", movie, "/media/movies/Alien (1979)/", 10 * gb, true},
		{"its single file", movie, "/media/movies/Alien (1979)/Alien.mkv", 10 * gb, true},
		{"10% bigger (extras)", movie, "/media/movies/Alien (1979)", 11 * gb, true},
		{"much bigger: a pack", movie, "/media/movies/Alien (1979)", 40 * gb, false},
		{"a sibling folder sharing a prefix", movie, "/media/movies/Alien", 10 * gb, false},
		{"the whole library folder", movie, "/media/movies", 10 * gb, true}, // size is the guard here
		{"somewhere else (the download dir)", movie, "/downloads/Alien.1979.1080p", 10 * gb, false},
		{"no content path (qBittorrent < 4.3.2)", movie, "", 10 * gb, false},
		{"content path of only a slash", movie, "/", 10 * gb, false},
		{"a show's whole folder", show, "/media/tv/Show", 20 * gb, true},
		{"one season of a show", show, "/media/tv/Show/Season 1", 10 * gb, false},
		{"an item of unknown size", &Item{files: movie.files}, "/media/movies/Alien (1979)", 1, false},
		{"an item without files", &Item{Size: 10 * gb}, "/media/movies/Alien (1979)", 10 * gb, false},
	}
	for _, c := range cases {
		tr := qbittorrent.Torrent{ContentPath: c.content, Size: c.size}
		if got := holdsOnly(tr, c.it); got != c.want {
			t.Errorf("%s: holdsOnly = %v, want %v", c.why, got, c.want)
		}
	}
}

// sortedDue: only Leaving, Waiting and Failed items whose time has come, soonest first; biggest
// first when a full disk is hurrying them.
func TestSortedDue(t *testing.T) {
	const now = 1_000_000.0
	items := map[string]*Item{
		"a": {Key: "a", Status: Leaving, LeavingAt: now - 10},
		"b": {Key: "b", Status: Waiting, LeavingAt: now - 100},
		"c": {Key: "c", Status: Failed, LeavingAt: now},
		"d": {Key: "d", Status: Preview, LeavingAt: now - 500},
		"e": {Key: "e", Status: Removed, LeavingAt: now - 500},
		"f": {Key: "f", Status: Leaving, LeavingAt: now + 1},
		"g": {Key: "g", Status: Leaving, LeavingAt: 0},
	}
	var got []string
	for _, it := range sortedDue(items, now) {
		got = append(got, it.Key)
	}
	if strings.Join(got, ",") != "b,a,c" {
		t.Errorf("sortedDue = %v, want b,a,c", got)
	}
	full := map[string]*Item{
		"small": {Key: "small", Status: Leaving, LeavingAt: now - 100, Size: 1, Reason: "the disk it is on is 97% full, so it leaves sooner"},
		"big":   {Key: "big", Status: Leaving, LeavingAt: now - 1, Size: 9, Reason: "the disk it is on is 97% full, so it leaves sooner"},
	}
	got = nil
	for _, it := range sortedDue(full, now) {
		got = append(got, it.Key)
	}
	if strings.Join(got, ",") != "big,small" {
		t.Errorf("sortedDue on a full disk = %v, want big,small", got)
	}
}

// fakes is a Plex, qBittorrent and Radarr that log what is asked of them.
type fakes struct {
	mu          sync.Mutex
	calls       []string
	now         time.Time
	torrents    string // qBittorrent's torrent list, JSON
	qbFail      string // a hash prefix qBittorrent refuses to delete
	history     string // Radarr's download history, JSON; "fail" for an error
	radarrMovie string // Radarr's movie list, JSON
}

func (f *fakes) log(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

// set changes the fakes under their lock, as their servers read them from other goroutines.
func (f *fakes) set(change func(f *fakes)) {
	f.mu.Lock()
	change(f)
	f.mu.Unlock()
}

func (f *fakes) addedAt() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now.Unix() - 90*86400
}

func (f *fakes) failing() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.qbFail
}

func (f *fakes) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "|")
}

func (f *fakes) servers(t *testing.T) (px, qb, rd *httptest.Server) {
	px = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			f.log("plex delete " + r.URL.Path)
		case r.URL.Path == "/status/sessions/history/all":
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[]}}`)
		case r.URL.Path == "/library/sections":
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`)
		case r.URL.Path == "/library/sections/1/all":
			fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[
 {"ratingKey":"20","type":"movie","title":"Alien","year":1979,"addedAt":%d,"Guid":[{"id":"tmdb://348"}],"Media":[{"Part":[{"file":"/media/movies/Alien (1979)/Alien.mkv","size":10000000000}]}]}]}}`,
				f.addedAt())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	qb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "ok", Path: "/"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/app/version":
			fmt.Fprint(w, "v5.0.0")
		case "/api/v2/torrents/info":
			fmt.Fprint(w, f.torrents)
		case "/api/v2/torrents/delete":
			r.ParseForm()
			h := r.Form.Get("hashes")
			if fail := f.failing(); fail != "" && strings.HasPrefix(h, fail) {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.log("qb delete " + h[:4] + " files=" + r.Form.Get("deleteFiles"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	rd = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodGet:
			fmt.Fprint(w, f.radarrMovie)
		case r.URL.Path == "/api/v3/history/movie":
			if f.history == "fail" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			fmt.Fprint(w, f.history)
		case strings.HasPrefix(r.URL.Path, "/api/v3/movie/") && r.Method == http.MethodDelete:
			f.log("radarr delete " + r.URL.Path)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(px.Close)
	t.Cleanup(qb.Close)
	t.Cleanup(rd.Close)
	return px, qb, rd
}

const alienTorrents = `[
 {"hash":"aaaa000000000000000000000000000000000000","name":"Alien.1979.Directors.Cut.1080p.BluRay","ratio":2,"size":10000000000,"content_path":"/downloads/Alien.1979.Directors.Cut.1080p.BluRay"},
 {"hash":"bbbb000000000000000000000000000000000000","name":"Alien Quadrilogy 1979-1997 1080p","ratio":2,"size":40000000000,"content_path":"/downloads/Alien Quadrilogy"},
 {"hash":"cccc000000000000000000000000000000000000","name":"Aliens.1986.Special.Edition.1080p","ratio":2,"size":9000000000,"content_path":"/downloads/Aliens.1986"},
 {"hash":"dddd000000000000000000000000000000000000","name":"Alien.Romulus.2024.2160p","ratio":2,"size":20000000000,"content_path":"/downloads/Alien.Romulus.2024"}]`

// runScans lists the item on one pass and, eight days later, removes what is due on another.
func runScans(t *testing.T, f *fakes, withRadarr bool) *Manager {
	t.Helper()
	px, qs, rs := f.servers(t)
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	cfg := config.Settings{PlexMonitor: true, MediaCleanup: true, MediaCleanupDays: 60, MediaCleanupNoticeDays: 7, MediaCleanupRatio: 1, MediaCleanupRatioDays: 30}
	m := &Manager{Store: store, Config: func() config.Settings { return cfg }, Now: func() time.Time { return f.now }}
	src := func() Sources {
		p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
		q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
		s := Sources{Plex: p, QB: q}
		if withRadarr {
			s.Radarr = arr.New("radarr", rs.URL, "key")
		}
		return s
	}
	ctx := context.Background()
	if err := m.scan(ctx, cfg, src(), float64(f.now.Unix())); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := f.joined(); got != "" {
		t.Fatalf("the first pass removed something: %s", got)
	}
	var later time.Time
	f.set(func(f *fakes) { f.now = f.now.Add(8 * 24 * time.Hour); later = f.now })
	if err := m.scan(ctx, cfg, src(), float64(later.Unix())); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	return m
}

// With Radarr's download history naming the torrent, only that one goes (with its files), and a
// collection that merely matches by name is not touched at all.
func TestExactMatchLeavesNameMatchesAlone(t *testing.T) {
	f := &fakes{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), torrents: alienTorrents,
		radarrMovie: `[{"id":7,"title":"Alien","tmdbId":348,"path":"/media/movies/Alien (1979)","sizeOnDisk":10000000000}]`,
		history:     `[{"downloadId":"AAAA000000000000000000000000000000000000","eventType":"downloadFolderImported"}]`}
	runScans(t, f, true)
	got := f.joined()
	if got != "qb delete aaaa files=true|radarr delete /api/v3/movie/7" {
		t.Fatalf("calls = %s", got)
	}
}

// Without Radarr, torrents are matched by name: the movie's own and the collection go, both with
// their files kept since neither is plainly the item alone; other films' torrents stay.
func TestNameMatchKeepsFilesOfPacksAndOthers(t *testing.T) {
	f := &fakes{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), torrents: alienTorrents}
	runScans(t, f, false)
	got := f.joined()
	for _, want := range []string{"qb delete aaaa files=false", "qb delete bbbb files=false", "plex delete /library/metadata/20"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in %s", want, got)
		}
	}
	if strings.Contains(got, "files=true") || strings.Contains(got, "cccc") || strings.Contains(got, "dddd") {
		t.Fatalf("deleted files, or another film's torrent: %s", got)
	}
}

// When the *arr download history cannot be read, the item is "unsure": nothing of it is removed,
// not even by a name guess, and the reason says why.
func TestUnsureWhenHistoryFails(t *testing.T) {
	f := &fakes{now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), torrents: alienTorrents, history: "fail",
		radarrMovie: `[{"id":7,"title":"Alien","tmdbId":348,"path":"/media/movies/Alien (1979)","sizeOnDisk":10000000000}]`}
	m := runScans(t, f, true)
	if got := f.joined(); got != "" {
		t.Fatalf("removed while unsure: %s", got)
	}
	st, _ := m.load()
	it := st.Items["20"]
	if it == nil || it.Status != Leaving || !strings.Contains(it.Reason, "download history") || len(it.Torrents) != 0 {
		t.Fatalf("item = %+v", it)
	}
}

// remove deletes every torrent before the media, each with or without its files as decided, and
// stops (leaving the media) as soon as a torrent cannot be removed or there is no qBittorrent.
func TestRemoveOrderAndStops(t *testing.T) {
	f := &fakes{}
	px, qs, _ := f.servers(t)
	p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
	q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
	it := &Item{Key: "20", Title: "Alien", Manager: "plex", Torrents: []Torrent{
		{Hash: "aaaa000000000000000000000000000000000000", Name: "own", withFiles: true},
		{Hash: "bbbb000000000000000000000000000000000000", Name: "pack", withFiles: false},
	}}
	m := &Manager{}
	ctx := context.Background()

	if err := m.remove(ctx, Sources{Plex: p}, it, nil); err == nil || f.joined() != "" {
		t.Fatalf("without qBittorrent: err %v, calls %s", err, f.joined())
	}
	f.set(func(f *fakes) { f.qbFail = "bbbb" })
	if err := m.remove(ctx, Sources{Plex: p, QB: q}, it, nil); err == nil || f.joined() != "qb delete aaaa files=true" {
		t.Fatalf("a torrent qBittorrent refused: err %v, calls %s", err, f.joined())
	}
	f.set(func(f *fakes) { f.qbFail, f.calls = "", nil })
	if err := m.remove(ctx, Sources{Plex: p, QB: q}, it, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.joined(); got != "qb delete aaaa files=true|qb delete bbbb files=false|plex delete /library/metadata/20" {
		t.Fatalf("calls = %s", got)
	}

	// Managed by Radarr, but Radarr no longer has it: an error, and Plex is not asked instead.
	f.set(func(f *fakes) { f.calls = nil })
	gone := &Item{Key: "21", Title: "Gone", Manager: "radarr", guids: []string{"tmdb://1"}, Torrents: []Torrent{}}
	err := m.remove(ctx, Sources{Plex: p, QB: q, Radarr: arr.New("radarr", "http://127.0.0.1:1", "key")}, gone, map[string][]arr.Entry{"radarr": {{ID: 9, TMDB: 2}}})
	if err == nil || !strings.Contains(err.Error(), "no longer has it") || strings.Contains(f.joined(), "plex delete") {
		t.Fatalf("err %v, calls %s", err, f.joined())
	}
}

// Who removes the media, and that they still have it, is settled before any torrent goes: a movie
// Radarr no longer has, or one whose Radarr is not connected, keeps its torrents and files.
func TestNothingGoesWhenTheMediaCannotBeRemoved(t *testing.T) {
	f := &fakes{}
	px, qs, _ := f.servers(t)
	p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
	q, _ := qbittorrent.New(qs.URL, 0, "admin", "secret")
	m := &Manager{}
	ctx := context.Background()
	it := &Item{Key: "21", Title: "Gone", Manager: "radarr", guids: []string{"tmdb://1"}, Torrents: []Torrent{
		{Hash: "aaaa000000000000000000000000000000000000", Name: "own", withFiles: true},
	}}
	err := m.remove(ctx, Sources{Plex: p, QB: q, Radarr: arr.New("radarr", "http://127.0.0.1:1", "key")}, it, map[string][]arr.Entry{"radarr": {{ID: 9, TMDB: 2}}})
	if err == nil || !strings.Contains(err.Error(), "no longer has it") || f.joined() != "" {
		t.Fatalf("Radarr without it: err %v, calls %s", err, f.joined())
	}
	err = m.remove(ctx, Sources{Plex: p, QB: q}, it, map[string][]arr.Entry{"radarr": {{ID: 9, TMDB: 1}}})
	if err == nil || f.joined() != "" {
		t.Fatalf("no Radarr: err %v, calls %s", err, f.joined())
	}
	plexOnly := &Item{Key: "22", Title: "Plex only", Manager: "plex", Torrents: it.Torrents}
	if err := m.remove(ctx, Sources{QB: q}, plexOnly, nil); err == nil || f.joined() != "" {
		t.Fatalf("no Plex: err %v, calls %s", err, f.joined())
	}
}

// manyMovies is a Plex with n never-watched movies whose deletion fails, and a count of the tries.
func manyMovies(t *testing.T, n int, now time.Time) (*httptest.Server, *int) {
	var mu sync.Mutex
	deletes := 0
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			mu.Lock()
			deletes++
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/status/sessions/history/all":
			fmt.Fprint(w, `{"MediaContainer":{"Metadata":[]}}`)
		case r.URL.Path == "/library/sections":
			fmt.Fprint(w, `{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"}]}}`)
		case r.URL.Path == "/library/sections/1/all":
			var items []string
			for i := range n {
				items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie number %d","year":2001,"addedAt":%d}`, 100+i, i, now.Unix()-90*86400))
			}
			fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[%s]}}`, strings.Join(items, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(px.Close)
	return px, &deletes
}

func cleanupManager(t *testing.T, px *httptest.Server, now *time.Time) (*Manager, *storage.Store) {
	store := storage.Open(":memory:")
	t.Cleanup(store.Close)
	cfg := config.Settings{PlexMonitor: true, MediaCleanup: true, MediaCleanupDays: 60, MediaCleanupNoticeDays: 7, MediaCleanupRatio: 1, MediaCleanupRatioDays: 30}
	m := &Manager{Store: store, Config: func() config.Settings { return cfg }, Now: func() time.Time { return *now },
		Connect: func(config.Settings) Sources {
			p, _ := plex.New(px.URL, "tok", true, 5*time.Second)
			return Sources{Plex: p}
		}}
	return m, store
}

// A pass tries at most maxRemovals removals, failed ones included.
func TestFailedRemovalsCountTowardsThePassLimit(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	px, deletes := manyMovies(t, maxRemovals+5, now)
	m, _ := cleanupManager(t, px, &now)
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *deletes != maxRemovals {
		t.Fatalf("%d removals tried in one pass, want %d", *deletes, maxRemovals)
	}
}

// A list that cannot be read stops the pass: nothing is removed, and nothing is saved over it, so
// what the owner kept stays kept.
func TestAnUnreadableListIsNeverSavedOver(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	px, deletes := manyMovies(t, 3, now)
	m, store := cleanupManager(t, px, &now)
	const broken = `{"kept":{"100":{"key":"100","title":"Movie number 0"}},"items":"oops"`
	store.SetMeta(stateKey, broken)
	if err := m.Scan(context.Background()); err == nil {
		t.Fatal("the pass went ahead without its list")
	}
	if err := m.Keep("100", false); err == nil {
		t.Fatal("unkept with no list")
	}
	if raw, _ := store.MetaValue(stateKey); raw != broken {
		t.Fatalf("the list was saved over: %s", raw)
	}
	if *deletes != 0 {
		t.Fatalf("%d removals", *deletes)
	}
	if v := m.View(); !strings.Contains(v.Error, "could not be read") {
		t.Fatalf("the page does not say so: %+v", v.Error)
	}
	if m.FirstWait() != 2*time.Minute {
		t.Fatal("an unreadable list should be retried soon")
	}
}

// A pass told to stop removes nothing more, but saves what it listed.
func TestAStoppedPassRemovesNothingAndSaves(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	px, deletes := manyMovies(t, 3, now)
	m, _ := cleanupManager(t, px, &now)
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Scan(ctx)
	if *deletes != 0 {
		t.Fatalf("%d removals after the stop", *deletes)
	}
	st, err := m.load()
	if err != nil || len(st.Items) != 3 || st.Scanned != float64(now.Unix()) {
		t.Fatalf("state %+v, %v", st, err)
	}
	for _, it := range st.Items {
		if it.Status != Leaving {
			t.Fatalf("item %+v", it)
		}
	}
}
