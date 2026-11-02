package plex_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"servermonitor/internal/plex"
)

const sessionsJSON = `{"MediaContainer":{"size":3,"Metadata":[
 {"sessionKey":"1","type":"episode","title":"Pilot","grandparentTitle":"Severance","parentIndex":1,"index":1,
  "duration":3000000,"viewOffset":1500000,"User":{"title":"alex"},
  "Player":{"title":"Living Room","product":"Plex for Roku","platform":"Roku","state":"playing","local":true},
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
		case "/library/sections":
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"TV","type":"show","refreshing":false},{"key":"2","title":"Movies","type":"movie"}]}}`))
		case "/library/sections/1/all":
			if r.URL.Query().Get("type") == "4" {
				w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":812}}`))
			} else {
				w.Write([]byte(`{"MediaContainer":{"size":0,"totalSize":40}}`))
			}
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
}

func (f *fake) URL() string                   { return "http://pms" }
func (f *fake) Server() (plex.Server, error)  { return plex.Server{Name: "nas", Version: "1"}, nil }
func (f *fake) AvailableUpdate(string) string { return "" }
func (f *fake) Sessions() ([]plex.Stream, error) {
	return append([]plex.Stream(nil), f.streams...), f.err
}
func (f *fake) Libraries() ([]plex.Library, error) { return nil, nil }

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
