package plexstats_test

import (
	"strings"
	"testing"
	"time"

	"nightjar/internal/plex"
	"nightjar/internal/plexstats"
	"nightjar/internal/storage"
)

type source struct {
	streams []plex.Stream
	libs    []plex.Library
	viewed  []plex.Viewed
}

func (s *source) URL() string                            { return "http://pms" }
func (s *source) Server() (plex.Server, error)           { return plex.Server{Name: "nas"}, nil }
func (s *source) AvailableUpdate(string) string          { return "" }
func (s *source) Sessions() ([]plex.Stream, error)       { return s.streams, nil }
func (s *source) Libraries() ([]plex.Library, error)     { return s.libs, nil }
func (s *source) Terminate(string, string) error         { return nil }
func (s *source) History(float64) ([]plex.Viewed, error) { return s.viewed, nil }

func TestWeek(t *testing.T) {
	store := storage.Open(":memory:")
	defer store.Close()
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local)
	at := func(daysAgo, hour int) float64 {
		return float64(now.AddDate(0, 0, -daysAgo).Truncate(24 * time.Hour).Add(time.Duration(hour) * time.Hour).Unix())
	}
	hour := 3600.0
	for _, p := range []storage.PlexPlay{
		{Started: at(2, 20), Ended: at(2, 20) + 2*hour, User: "sam", Kind: "movie", Title: "Heat", Decision: plex.DirectPlay, Local: true, DurationMS: 10200000},
		{Started: at(2, 21), Ended: at(2, 21) + hour, User: "alex", Kind: "episode", Title: "Severance", Decision: plex.Transcode, DurationMS: 3300000},
		{Started: at(2, 21) + 600, Ended: at(2, 21) + 1800, User: "kim", Kind: "episode", Title: "Severance", Decision: plex.DirectPlay},
		// Left paused all night: counts no more than the episode's length.
		{Started: at(1, 22), Ended: at(1, 22) + 10*hour, User: "sam", Kind: "episode", Title: "Severance", Decision: plex.DirectPlay, Local: true, DurationMS: 3000000},
		// Too short to count.
		{Started: at(1, 12), Ended: at(1, 12) + 20, User: "sam", Title: "Trailer"},
		// Before the week.
		{Started: at(9, 20), Ended: at(9, 21), User: "sam", Title: "Old"},
	} {
		store.AddPlexPlay(p)
	}
	src := &source{
		libs:   []plex.Library{{Key: "1", Title: "TV", Type: "show", Count: 40, ChildCount: 812, ChildLabel: "episodes"}, {Key: "2", Title: "Movies", Type: "movie", Count: 352}},
		viewed: []plex.Viewed{{User: "sam", Title: "Heat", Kind: "movie"}, {User: "alex", Title: "The Pilot", Show: "Severance", Kind: "episode"}, {User: "kim", Title: "Hello", Show: "Severance", Kind: "episode"}},
	}
	// A week ago the libraries were smaller.
	plexstats.RecordLibraries(store, []plex.Library{{Key: "1", Title: "TV", Type: "show", Count: 40, ChildCount: 798, ChildLabel: "episodes"}, {Key: "2", Title: "Movies", Type: "movie", Count: 350}}, now.AddDate(0, 0, -7))
	m := plex.NewMonitor(src, 10, 1)
	m.Poll(float64(now.Unix()))
	for i := 0; i < 50 && len(m.Libraries()) == 0; i++ {
		time.Sleep(10 * time.Millisecond) // libraries are counted in the background
	}

	st := plexstats.Make(store, m, 7, now)
	if st.Plays != 4 || st.Peak != 3 || st.Users[0].Name != "sam" || st.Titles[0].Name != "Severance" {
		t.Fatalf("plays %d, peak %d, users %+v, titles %+v", st.Plays, st.Peak, st.Users, st.Titles)
	}
	// 2h of Heat, 55 and 20 minutes of Severance (no longer than the episode), and the paused one's 50.
	if h := st.Hours; h < 4.05 || h > 4.1 {
		t.Fatalf("hours %.2f", h)
	}
	if *st.Transcoded != 25 || *st.Remote != 50 {
		t.Fatalf("transcoded %v, remote %v", *st.Transcoded, *st.Remote)
	}
	if st.Watched != 3 || st.Finished[0].Name != "Severance" || st.Finished[0].N != 2 {
		t.Fatalf("history: %+v", st)
	}
	if len(st.Added) != 2 || st.Added[0].Items != 14 || st.Added[0].Label != "episodes" || st.Added[1].Items != 2 || st.Added[1].Label != "movies" {
		t.Fatalf("added: %+v", st.Added)
	}
	line := st.Line()
	for _, want := range []string{"4 streams, 4 hours", "mostly sam", "top: Severance", "3 at once on", "25% transcoded", "added 14 episodes, 2 movies"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
}
