package storage_test

import (
	"math"
	"path/filepath"
	"testing"

	"nightjar/internal/storage"
)

func f(v float64) *float64 { return &v }

// Hours roll up once finished, keep their peaks and minute counts, and outlive the minutes they came from.
func TestRollupSamplesOutlivesMinutes(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	const day0 = 1_700_000_000 / 86400 * 86400 // midnight
	var rows []storage.Sample
	// Two full hours of minutes for "nas": CPU 10 then 30, a 90% spike in the second, half the checks
	// missed in the first hour.
	for m := 0; m < 120; m++ {
		cpu, up := 10.0, 1.0
		if m >= 60 {
			cpu = 30
		} else if m%2 == 0 {
			up = 0
		}
		r := storage.Sample{Host: "nas", TS: day0 + float64(m)*60, Up: up, CPU: f(cpu), CPUMax: f(cpu)}
		if m == 90 {
			r.CPUMax = f(90)
		}
		rows = append(rows, r)
	}
	// One minute in the hour still going: not rolled up yet.
	rows = append(rows, storage.Sample{Host: "nas", TS: day0 + 2*3600 + 60, Up: 1, CPU: f(50), CPUMax: f(50)})
	s.AddSamples(rows)

	if n := s.RollupSamples(day0 + 2*3600 + 120); n != 2 {
		t.Fatalf("rolled up %d hours, want 2", n)
	}
	if n := s.RollupSamples(day0 + 2*3600 + 180); n != 1 {
		t.Fatalf("second run redid %d hours, want only the last one rolled up (1)", n)
	}

	// Drop every minute; the hourly rows still answer a long range, plus the minute after them.
	s.PruneSamples(day0 + 3*3600)
	b := s.SampleBuckets("nas", day0, 86400)
	if len(b) != 1 {
		t.Fatalf("got %d day buckets, want 1: %+v", len(b), b)
	}
	day := b[0]
	if day.Minutes != 120 {
		t.Errorf("minutes = %d, want the 120 rolled-up minutes (the later one was pruned)", day.Minutes)
	}
	if day.CPU == nil || math.Abs(*day.CPU-20) > 1e-9 {
		t.Errorf("cpu = %v, want 20", day.CPU)
	}
	if day.CPUMax == nil || *day.CPUMax != 90 {
		t.Errorf("cpu_max = %v, want 90", day.CPUMax)
	}
	if math.Abs(day.Up-0.75) > 1e-9 {
		t.Errorf("up = %v, want 0.75", day.Up)
	}

	// Minute-sized buckets still read minutes only.
	if got := s.SampleBuckets("nas", day0, 60); len(got) != 0 {
		t.Errorf("minute buckets came from hourly rows: %+v", got)
	}

	s.PruneHourlySamples(day0 + 3600)
	if b := s.SampleBuckets("nas", day0, 86400); len(b) != 1 || b[0].Minutes != 60 {
		t.Errorf("after pruning the first hour: %+v", b)
	}
	s.DeleteSamples("nas")
	if b := s.SampleBuckets("nas", day0, 86400); len(b) != 0 {
		t.Errorf("DeleteSamples left hourly rows: %+v", b)
	}
}

// The minutes after the last rolled-up hour fill in the long ranges, so nothing recent is missing.
func TestLongBucketsIncludeMinutesAfterRollup(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	const t0 = 1_700_000_000 / 3600 * 3600
	s.AddSamples([]storage.Sample{
		{Host: "a", TS: t0, Up: 1, CPU: f(10)},
		{Host: "a", TS: t0 + 3600, Up: 1, CPU: f(40)},
	})
	s.RollupSamples(t0 + 3600 + 30) // rolls up the first hour only
	b := s.SampleBuckets("a", t0, 7200)
	if len(b) != 1 || b[0].Minutes != 2 || b[0].CPU == nil || *b[0].CPU != 25 {
		t.Fatalf("got %+v, want one bucket of 2 minutes averaging 25", b)
	}
}

// Outage minutes outlive the minutes they came from, so a year's outages can still be listed.
func TestDownMinutesOutliveMinutes(t *testing.T) {
	s := storage.Open(filepath.Join(t.TempDir(), "h.db"))
	defer s.Close()
	const t0 = 1_700_000_000 / 3600 * 3600
	var rows []storage.Sample
	for m := 0; m < 60; m++ {
		up := 1.0
		if m >= 10 && m < 13 { // a three-minute outage
			up = 0
		}
		rows = append(rows, storage.Sample{Host: "nas", TS: t0 + float64(m)*60, Up: up})
	}
	rows = append(rows, storage.Sample{Host: "nas", TS: t0 + 3600 + 60, Up: 0}) // still in its minute table
	s.AddSamples(rows)
	s.RollupSamples(t0 + 3600 + 120)
	s.PruneSamples(t0 + 3600) // the first hour's minutes are gone

	got := s.DownMinutes("nas", 0)
	want := []float64{t0 + 600, t0 + 660, t0 + 720, t0 + 3660}
	if len(got) != len(want) {
		t.Fatalf("down minutes %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("down minutes %v, want %v", got, want)
		}
	}
	// Rolling up again does not count a minute twice.
	s.RollupSamples(t0 + 2*3600 + 30)
	if n := len(s.DownMinutes("nas", 0)); n != 4 {
		t.Errorf("after a second rollup: %d down minutes, want 4", n)
	}
	s.PruneHourlySamples(t0 + 3600)
	if got := s.DownMinutes("nas", 0); len(got) != 1 || got[0] != t0+3660 {
		t.Errorf("after pruning the first hour: %v", got)
	}
	s.DeleteSamples("nas")
	if got := s.DownMinutes("nas", 0); len(got) != 0 {
		t.Errorf("DeleteSamples left %v", got)
	}
}
