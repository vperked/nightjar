package plex_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"nightjar/internal/plex"
)

const sessionsJSON = `{"MediaContainer":{"size":3,"Metadata":[
 {"sessionKey":"1","type":"episode","title":"Pilot","grandparentTitle":"Severance","parentIndex":1,"index":1,
  "duration":3000000,"viewOffset":1500000,"User":{"title":"alex"},
  "Player":{"title":"Living Room","product":"Plex for Roku","platform":"Roku","state":"playing","local":true,"address":"192.168.1.20"},
  "Session":{"id":"a","bandwidth":12000,"location":"lan"},
  "Media":[{"videoResolution":"1080","bitrate":10000}]},
 {"sessionKey":"2","type":"movie","title":"Heat","year":1995,"duration":100,"viewOffset":10,"User":{"title":"sam"},
  "Player":{"title":"Phone","product":"Plex for iOS","state":"buffering","local":false},
  "Session":{"id":"b","bandwidth":"4000","location":"wan"},
  "TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy","throttled":"1","speed":"2.5","transcodeHwEncoding":"vaapi"},
  "Media":[{"videoResolution":"4k","bitrate":4000}]},
 {"sessionKey":"3","type":"track","title":"Song","grandparentTitle":"Artist","parentTitle":"Album","User":{"title":"kim"},
  "Player":{"title":"Web","state":"paused"},"Session":{"bandwidth":320},
  "TranscodeSession":{"videoDecision":"","audioDecision":"copy"}}
]}}`

func fakePMS(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.RawQuery, "secret") {
			t.Error("token must not be sent in the URL")
		}
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`{"MediaContainer":{"friendlyName":"nas","version":"1.40.0","platform":"Linux","platformVersion":"6.8"}}`))
		case "/updater/status":
			w.Write([]byte(`{"MediaContainer":{"Release":[{"version":"1.41.0","state":"available"}]}}`))
		case "/status/sessions":
			w.Write([]byte(sessionsJSON))
		case "/status/sessions/terminate":
			switch r.URL.Query().Get("sessionId") {
			case "a":
				if r.URL.Query().Get("reason") != "Bedtime" {
					t.Errorf("reason %q", r.URL.Query().Get("reason"))
				}
			case "b":
				w.WriteHeader(http.StatusUnauthorized)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case "/library/sections":
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"TV","type":"show","refreshing":false},{"key":"2","title":"Movies","type":"movie"}]}}`))
		case "/library/sections/1/all":
			if r.URL.Query().Get("type") == "4" {
				w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":812}}`))
			} else {
				w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":40}}`))
			}
		case "/status/sessions/history/all":
			if r.URL.Query().Get("sort") != "viewedAt:desc" {
				t.Errorf("history must come newest first: %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"MediaContainer":{"Metadata":[
 {"viewedAt":2000,"accountID":1,"type":"episode","title":"The Pilot","grandparentTitle":"Severance"},
 {"viewedAt":1500,"accountID":7,"type":"movie","title":"Heat"},
 {"viewedAt":900,"accountID":1,"type":"movie","title":"Too old"}]}}`))
		case "/accounts":
			w.Write([]byte(`{"MediaContainer":{"Account":[{"id":1,"name":"sam"},{"id":7,"name":"alex"}]}}`))
		case "/library/sections/2/all":
			w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":350}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestClient(t *testing.T) {
	srv := fakePMS(t)
	defer srv.Close()
	c, err := plex.New(srv.URL, "secret", true, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	info, err := c.Server()
	if err != nil || info.Name != "nas" || info.Version != "1.40.0" {
		t.Fatalf("server %+v %v", info, err)
	}
	if up := c.AvailableUpdate(info.Version); up != "1.41.0" {
		t.Fatalf("update %q", up)
	}

	streams, err := c.Sessions()
	if err != nil || len(streams) != 3 {
		t.Fatalf("sessions %v %v", streams, err)
	}
	ep, movie, track := streams[0], streams[1], streams[2]
	if ep.Title != "Severance" || ep.Subtitle != "S01E01 · Pilot" || ep.Decision != plex.DirectPlay || ep.Quality != "1080p" ||
		ep.Progress != 50 || !ep.Local || ep.BandwidthKbps != 12000 {
		t.Errorf("episode %+v", ep)
	}
	if movie.Decision != plex.Transcode || !movie.HWTranscode || !movie.Throttled || movie.Speed != 2.5 ||
		movie.Subtitle != "1995" || movie.Quality != "4K" || movie.Local || movie.BandwidthKbps != 4000 {
		t.Errorf("movie %+v", movie)
	}
	if track.Decision != plex.DirectStream || track.Subtitle != "Artist · Album" {
		t.Errorf("track %+v", track)
	}

	if ep.Address != "192.168.1.20" || ep.SessionID != "a" || movie.SessionID != "b" {
		t.Errorf("address and session ids %+v %+v", ep, movie)
	}
	if err := c.Terminate("a", "Bedtime"); err != nil {
		t.Errorf("terminate: %v", err)
	}
	if err := c.Terminate("b", "x"); !errors.Is(err, plex.ErrStopRefused) {
		t.Errorf("want refused, got %v", err)
	}
	if err := c.Terminate("gone", "x"); !errors.Is(err, plex.ErrNoStream) {
		t.Errorf("want no stream, got %v", err)
	}

	libs, err := c.Libraries()
	if err != nil || len(libs) != 2 {
		t.Fatalf("libraries %v %v", libs, err)
	}
	if libs[0].Count != 40 || libs[0].ChildCount != 812 || libs[0].ChildLabel != "episodes" || libs[1].Count != 350 {
		t.Errorf("libraries %+v", libs)
	}

	bad, _ := plex.New(srv.URL, "wrong", true, 2*time.Second)
	if _, err := bad.Sessions(); !errors.Is(err, plex.ErrUnauthorized) {
		t.Errorf("want unauthorized, got %v", err)
	}
}

// Plex sends an item's own id as "guid", a string, beside its "Guid" list; Go matches keys regardless of
// case, so the string must not fail the library (it did: "cannot unmarshal string into ... guid").
func TestItemsWithBothGuidKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections/2/all" || r.URL.Query().Get("includeGuids") != "1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"MediaContainer":{"Metadata":[
 {"ratingKey":"10","type":"movie","title":"Heat","year":1995,"guid":"plex://movie/5d776825","Guid":[{"id":"imdb://tt0113277"},{"id":"tmdb://949"}],
  "Media":[{"Part":[{"file":"/mnt/media/Heat.mkv","size":1000}]}]},
 {"ratingKey":"11","type":"movie","title":"Alien","Guid":[{"id":"tmdb://348"}],"guid":"plex://movie/5d77682a"},
 {"ratingKey":"12","type":"movie","title":"Local video","guid":"local://12"}]}}`))
	}))
	defer srv.Close()
	c, err := plex.New(srv.URL, "", true, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := c.Items(plex.Library{Key: "2", Title: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, it := range items {
		got = append(got, it.Title+"="+strings.Join(it.Guids, ","))
	}
	if want := "Heat=imdb://tt0113277,tmdb://949|Alien=tmdb://348|Local video="; strings.Join(got, "|") != want {
		t.Errorf("got %q, want %q", strings.Join(got, "|"), want)
	}
	if items[0].Size != 1000 || len(items[0].Files) != 1 {
		t.Errorf("files and size lost: %+v", items[0])
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "localhost:32400", "ftp://x"} {
		if _, err := plex.New(u, "", true, time.Second); err == nil {
			t.Errorf("%q should be rejected", u)
		}
	}
}

type fake struct {
	streams []plex.Stream
	err     error
	stopped []string
	stopErr error
}

func (f *fake) URL() string                   { return "http://pms" }
func (f *fake) Server() (plex.Server, error)  { return plex.Server{Name: "nas", Version: "1"}, nil }
func (f *fake) AvailableUpdate(string) string { return "" }
func (f *fake) Sessions() ([]plex.Stream, error) {
	return append([]plex.Stream(nil), f.streams...), f.err
}
func (f *fake) Libraries() ([]plex.Library, error) { return nil, nil }
func (f *fake) Terminate(id, reason string) error {
	f.stopped = append(f.stopped, id+": "+reason)
	return f.stopErr
}

func kinds(events []plex.Event) string {
	var k []string
	for _, e := range events {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestMonitorTransitions(t *testing.T) {
	f := &fake{streams: []plex.Stream{{ID: "1", Title: "A", Decision: plex.DirectPlay, BandwidthKbps: 1000, Local: true}}}
	m := plex.NewMonitor(f, 10, 1)

	if e := m.Poll(0); kinds(e) != "" {
		t.Fatalf("first poll should not announce streams already playing: %v", kinds(e))
	}
	if m.Poll(5) != nil {
		t.Fatal("poll before the interval elapsed should do nothing")
	}
	f.streams = append(f.streams, plex.Stream{ID: "2", Title: "B", Decision: plex.Transcode, BandwidthKbps: 3000})
	if e := m.Poll(10); kinds(e) != plex.EventStreamStart || e[0].Stream.Title != "B" {
		t.Fatalf("new stream: %v", kinds(e))
	}
	snap := m.Snapshot(false)
	if snap.Status != plex.StatusUp || snap.Direct != 1 || snap.Transcode != 1 || snap.BandwidthKbps != 4000 || snap.WANKbps != 3000 {
		t.Fatalf("snapshot %+v", snap)
	}
	if snap.Streams[0].StartedAt != 0 || snap.Streams[1].StartedAt != 10 {
		t.Fatalf("start times should persist across polls: %+v", snap.Streams)
	}

	f.streams = f.streams[1:]
	m.Poll(20)
	if r := m.Snapshot(false).Recent; len(r) != 1 || r[0].Title != "A" || r[0].Ended != 20 {
		t.Fatalf("finished stream should move to recent: %+v", r)
	}

	f.err = errors.New("connection refused")
	if e := m.Poll(30); kinds(e) != "" {
		t.Fatal("one failure is within retries")
	}
	if e := m.Poll(40); kinds(e) != plex.EventDown || e[0].Detail != "connection refused" {
		t.Fatalf("second failure should go down: %v", kinds(e))
	}
	if s := m.Snapshot(false); s.Status != plex.StatusDown || len(s.Streams) != 0 {
		t.Fatalf("down snapshot %+v", s)
	}
	if e := m.Poll(50); kinds(e) != "" {
		t.Fatal("already down should not re-announce")
	}

	f.err = nil
	e := m.Poll(60)
	if kinds(e) != plex.EventUp || e[0].Downtime != 20 {
		t.Fatalf("recovery should report downtime and not re-announce playing streams: %v %+v", kinds(e), e)
	}
	if len(m.Snapshot(false).Samples) != 4 {
		t.Fatalf("samples only on successful polls, got %d", len(m.Snapshot(false).Samples))
	}
}

func TestChartSamplesKeepPeaks(t *testing.T) {
	f := &fake{}
	m := plex.NewMonitor(f, 10, 0)
	for i := range 1000 {
		f.streams = nil
		switch i {
		case 501:
			for id := range 3 {
				f.streams = append(f.streams, plex.Stream{ID: string(rune('a' + id)), Decision: plex.Transcode})
			}
		case 777:
			f.streams = []plex.Stream{{ID: "x", Decision: plex.DirectPlay, BandwidthKbps: 9000}}
		}
		m.Poll(float64(i * 10))
		if i == 9 {
			if got := len(m.Snapshot(false).Samples); got != 10 {
				t.Fatalf("short histories are returned as is, got %d", got)
			}
		}
	}
	out := m.Snapshot(false).Samples
	if len(out) != 360 || out[0].TS != 0 {
		t.Fatalf("got %d samples starting at %v, want 360 from 0", len(out), out[0].TS)
	}
	peakTranscode, peakBandwidth := 0, 0
	for _, s := range out {
		peakTranscode, peakBandwidth = max(peakTranscode, s.Transcode), max(peakBandwidth, s.BandwidthKbps)
	}
	if peakTranscode != 3 || peakBandwidth != 9000 {
		t.Fatalf("bucket peaks should survive downsampling: transcode %d bandwidth %d", peakTranscode, peakBandwidth)
	}
}

func TestSummarySnapshotOmitsCharts(t *testing.T) {
	m := plex.NewMonitor(&fake{streams: []plex.Stream{{ID: "1"}}}, 10, 0)
	m.Poll(0)
	if s := m.Snapshot(true); len(s.Streams) != 1 || len(s.Samples) != 0 || s.Samples == nil {
		t.Fatalf("summary %+v", s)
	}
}

func TestMonitorStop(t *testing.T) {
	f := &fake{streams: []plex.Stream{
		{ID: "1", SessionID: "s1", Title: "A", Decision: plex.DirectPlay},
		{ID: "2", Title: "B", Decision: plex.Transcode},
	}}
	m := plex.NewMonitor(f, 10, 1)
	m.Poll(0)

	stopped, err := m.Stop("1", "Bedtime")
	if err != nil || stopped.Title != "A" || strings.Join(f.stopped, ",") != "s1: Bedtime" {
		t.Fatalf("stop: %+v %v %v", stopped, err, f.stopped)
	}
	if snap := m.Snapshot(false); len(snap.Streams) != 1 || snap.Streams[0].ID != "2" {
		t.Errorf("stopped stream should leave the list: %+v", snap.Streams)
	}
	// The next poll no longer sees it and records it as finished.
	f.streams = f.streams[1:]
	m.Poll(20)
	if snap := m.Snapshot(false); len(snap.Recent) != 1 || snap.Recent[0].Title != "A" {
		t.Errorf("recent %+v", snap.Recent)
	}

	if _, err := m.Stop("1", "x"); !errors.Is(err, plex.ErrNoStream) {
		t.Errorf("want no stream, got %v", err)
	}
	if _, err := m.Stop("2", "x"); err == nil {
		t.Error("a stream without a session id cannot be stopped")
	}
	f.streams = append(f.streams, plex.Stream{ID: "3", SessionID: "s3"})
	m.Poll(40)
	f.stopErr = plex.ErrStopRefused
	if _, err := m.Stop("3", "x"); !errors.Is(err, plex.ErrStopRefused) {
		t.Errorf("want refused, got %v", err)
	}
	if snap := m.Snapshot(false); len(snap.Streams) != 2 {
		t.Errorf("a refused stop keeps the stream: %+v", snap.Streams)
	}
}

func TestHistory(t *testing.T) {
	srv := fakePMS(t)
	defer srv.Close()
	c, _ := plex.New(srv.URL, "secret", true, 2*time.Second)
	got, err := c.History(1000)
	if err != nil || len(got) != 2 {
		t.Fatalf("history since 1000: %v %+v", err, got)
	}
	if got[0].User != "sam" || got[0].Show != "Severance" || got[1].User != "alex" || got[1].Kind != "movie" {
		t.Fatalf("entries with their accounts' names: %+v", got)
	}
	// A finished stream is announced, so it can be kept.
	f := &fake{streams: []plex.Stream{{ID: "1", Title: "A", Kind: "movie", User: "sam", Decision: plex.Transcode, DurationMS: 5400000}}}
	m := plex.NewMonitor(f, 10, 1)
	m.Poll(0)
	f.streams = nil
	e := m.Poll(20)
	if kinds(e) != plex.EventStreamEnd || e[0].Play.User != "sam" || e[0].Play.Started != 0 || e[0].Play.Ended != 20 || e[0].Play.DurationMS != 5400000 {
		t.Fatalf("stream end: %+v", e)
	}
}

func TestTranscodeReasons(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"MediaContainer":{"Metadata":[
 {"sessionKey":"1","type":"movie","title":"Subs","User":{"title":"a"},"Player":{"title":"TV","local":true},"Session":{"id":"s1"},
  "Media":[{"height":1080,"videoCodec":"h264"}],"TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy","subtitleDecision":"burn"}},
 {"sessionKey":"2","type":"movie","title":"Remote","User":{"title":"b"},"Player":{"title":"Phone","local":false},"Session":{"id":"s2"},
  "Media":[{"height":2160,"videoCodec":"hevc"}],"TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy","sourceVideoCodec":"hevc","videoCodec":"h264","height":720}},
 {"sessionKey":"3","type":"movie","title":"Codec","User":{"title":"c"},"Player":{"title":"Old TV","local":true},"Session":{"id":"s3"},
  "Media":[{"height":1080,"videoCodec":"hevc"}],"TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy","sourceVideoCodec":"hevc","videoCodec":"h264","height":1080}},
 {"sessionKey":"4","type":"movie","title":"Audio","User":{"title":"d"},"Player":{"title":"TV","local":true},"Session":{"id":"s4"},
  "Media":[{"height":1080,"audioCodec":"truehd"}],"TranscodeSession":{"videoDecision":"copy","audioDecision":"transcode"}}]}}`))
	}))
	defer srv.Close()
	c, _ := plex.New(srv.URL, "secret", true, 2*time.Second)
	streams, err := c.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Subs":   "Burning in subtitles",
		"Remote": "Lowering the quality to 720p",
		"Codec":  "The player cannot play HEVC video",
		"Audio":  "The player cannot play Dolby TrueHD audio",
	}
	for _, s := range streams {
		if s.Reason != want[s.Title] || s.Fix == "" {
			t.Errorf("%s: %q (%q), want %q", s.Title, s.Reason, s.Fix, want[s.Title])
		}
	}
	for _, s := range streams {
		if s.Title == "Remote" && !strings.Contains(s.Fix, "remote quality") {
			t.Errorf("remote fix: %q", s.Fix)
		}
	}
}

// A library too big for one answer is read in pages; one that changes while it is read is an
// error, not a list missing an item.
func TestItemsArePaged(t *testing.T) {
	const n = 1234
	total, grow := n, true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		if size == 0 || size > 1000 {
			t.Errorf("not paged: %q", r.URL.RawQuery)
		}
		var items []string
		for i := start; i < min(start+size, n); i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"M%d"}`, i, i))
		}
		fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, total, strings.Join(items, ","))
		if grow && start > 0 {
			total = n + 1 // an item added after the first page
		}
	}))
	defer srv.Close()
	c, _ := plex.New(srv.URL, "", true, 2*time.Second)
	if _, err := c.Items(plex.Library{Key: "1", Title: "Movies"}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a library that changed while read: %v", err)
	}
	total, grow = n, false
	items, err := c.Items(plex.Library{Key: "1", Title: "Movies"})
	if err != nil || len(items) != n || items[n-1].Key != fmt.Sprint(n-1) {
		t.Fatalf("all %d items: %v %d", n, err, len(items))
	}
}

// A server that ignores the paging and sends the whole library each time is read once, not
// asked again forever, whether or not it says how many items there are.
func TestItemsUnpagedServer(t *testing.T) {
	const n = 700
	for _, total := range []int{n, 0} {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			var items []string
			for i := range n {
				items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"M%d"}`, i, i))
			}
			fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, total, strings.Join(items, ","))
		}))
		c, _ := plex.New(srv.URL, "", true, 2*time.Second)
		items, err := c.Items(plex.Library{Key: "1", Title: "Movies"})
		srv.Close()
		if err != nil || len(items) != n || requests > 2 {
			t.Fatalf("totalSize %d: all %d items in a request or two: %v %d items, %d requests", total, n, err, len(items), requests)
		}
	}
}

// An answer over the limit is refused as too large, not parsed cut short.
func TestOversizedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"MediaContainer":{"Directory":[{"title":"`))
		w.Write(bytes.Repeat([]byte("x"), 9<<20))
		w.Write([]byte(`"}]}}`))
	}))
	defer srv.Close()
	c, _ := plex.New(srv.URL, "", true, 5*time.Second)
	if _, err := c.Sections(); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("too large: %v", err)
	}
}

// Watch history that could not be read in full is an error: the media cleanup would take what is
// missing as never watched.
func TestEverWatchedReadsAllOrFails(t *testing.T) {
	entries, total := 2500, 2500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
		size, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Size"))
		var rows []string
		for i := start; i < min(start+size, entries); i++ {
			rows = append(rows, fmt.Sprintf(`{"ratingKey":"%d"}`, i))
		}
		fmt.Fprintf(w, `{"MediaContainer":{"totalSize":%d,"Metadata":[%s]}}`, total, strings.Join(rows, ","))
	}))
	defer srv.Close()
	c, _ := plex.New(srv.URL, "", true, 2*time.Second)
	seen, err := c.EverWatched()
	if err != nil || len(seen) != entries || !seen["2499"] {
		t.Fatalf("the whole history: %v %d", err, len(seen))
	}
	entries = 1800 // deleted while it was read
	if _, err := c.EverWatched(); err == nil {
		t.Fatal("history read short should fail")
	}
}
