package analytics_test

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"nightjar/internal/analytics"
	"nightjar/internal/storage"
)

func f(v float64) *float64 { return &v }

func openDB(t *testing.T) *storage.Store {
	db := storage.Open(filepath.Join(t.TempDir(), "a.db"))
	t.Cleanup(db.Close)
	return db
}

// clock is a time the test moves.
type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) add(d time.Duration)  { c.t = c.t.Add(d) }
func near(a *float64, b float64) bool { return a != nil && math.Abs(*a-b) < 0.01 }

func TestReadingsFoldIntoMinutes(t *testing.T) {
	db := openDB(t)
	c := &clock{time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	r := analytics.NewRecorder(db)
	r.Now = c.now
	// Six readings in one minute: CPU 10..60, one hot spike, one missed check.
	for i := range 6 {
		up := i != 3
		x := analytics.Reading{Up: up}
		if up {
			x.CPU, x.Mem, x.Temp, x.Rx = f(float64(10*(i+1))), f(50), f(60+float64(i)), f(1000)
		}
		r.Add("nas", x)
		c.add(10 * time.Second)
	}
	r.Add("nas", analytics.Reading{Up: true, CPU: f(5)}) // the next minute stores the first
	h := analytics.Read(db, "nas", analytics.RangeNamed("1h"), c.now(), true)
	if len(h.Points) != 1 {
		t.Fatalf("one finished minute: %+v", h.Points)
	}
	p := h.Points[0]
	if !near(p.CPU, 34) || !near(p.CPUMax, 60) || !near(p.TempMax, 65) || math.Abs(p.Up-5.0/6) > 0.001 || p.Minutes != 1 {
		t.Fatalf("the minute: cpu %v max %v temp max %v up %v", *p.CPU, *p.CPUMax, *p.TempMax, p.Up)
	}
	if !near(h.Summary.Uptime, 100*5.0/6) || math.Abs(h.Summary.DowntimeS-10) > 0.01 || h.Summary.RxBytes != 60000 {
		t.Fatalf("summary: %+v", h.Summary)
	}

	r.Flush()
	if h := analytics.Read(db, "nas", analytics.RangeNamed("1h"), c.now(), true); len(h.Points) != 2 {
		t.Fatalf("flush stores the minute in progress: %d", len(h.Points))
	}
	r.Forget("nas")
	if h := analytics.Read(db, "nas", analytics.RangeNamed("1h"), c.now(), true); len(h.Points) != 0 {
		t.Fatal("a server no longer watched leaves no history")
	}
	var nilRecorder *analytics.Recorder
	nilRecorder.Add("x", analytics.Reading{}) // records nothing, without panicking
}

func TestOutagesAndTheDiskForecast(t *testing.T) {
	db := openDB(t)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var rows []storage.Sample
	// Two days, a minute at a time: the disk fills a point a day, and the server is down twice.
	for m := 0; m < 2*24*60; m++ {
		ts := start.Add(time.Duration(m) * time.Minute)
		up := 1.0
		if (m >= 600 && m < 615) || (m >= 2000 && m < 2003) {
			up = 0
		}
		rows = append(rows, storage.Sample{Host: "nas", TS: float64(ts.Unix()), Up: up, Disk: f(80 + float64(m)/1440), CPU: f(20)})
	}
	db.AddSamples(rows)
	now := start.Add(2 * 24 * time.Hour)
	h := analytics.Read(db, "nas", analytics.RangeNamed("7d"), now, true)
	s := h.Summary
	if len(h.Incidents) != 2 || h.Incidents[0].End-h.Incidents[0].Start != 180 || h.Incidents[1].End-h.Incidents[1].Start != 900 || s.Incidents != 2 {
		t.Fatalf("two outages, newest first: %+v", h.Incidents)
	}
	if h.Incidents[0].Ongoing {
		t.Fatal("an outage that ended long ago is not ongoing")
	}
	if math.Abs(s.DowntimeS-18*60) > 1 || !near(s.Uptime, 100*(1-18.0/2880)) {
		t.Fatalf("downtime %v uptime %v", s.DowntimeS, *s.Uptime)
	}
	if s.DiskFullDays == nil || math.Abs(*s.DiskFullDays-18) > 0.5 || math.Abs(*s.DiskChange-2) > 0.05 {
		t.Fatalf("a disk at 82%% filling a point a day is full in 18 days: %v %v", *s.DiskFullDays, *s.DiskChange)
	}
	if math.Abs(s.Coverage-2.0/7) > 0.01 {
		t.Fatalf("two days of a seven-day range: %v", s.Coverage)
	}

	// The monitor's own machine has no uptime to speak of: it only records while it runs.
	local := analytics.Read(db, "nas", analytics.RangeNamed("7d"), now, false)
	if local.Summary.Uptime != nil || len(local.Incidents) != 0 {
		t.Fatalf("no uptime for the local machine: %+v", local.Summary)
	}
	// Too little data says nothing about the disk.
	short := analytics.Read(db, "nas", analytics.RangeNamed("1h"), now, true)
	if short.Summary.DiskFullDays != nil {
		t.Fatal("an hour is too short to forecast a disk")
	}
}

// A spike shows as the peak however wide the range's buckets are.
func TestPeaksAreNotSmoothedAway(t *testing.T) {
	db := openDB(t)
	c := &clock{time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}
	r := analytics.NewRecorder(db)
	r.Now = c.now
	for i := range 120 {
		mem, lat := 50.0, 10.0
		if i%60 == 30 {
			mem, lat = 98, 900
		}
		r.Add("nas", analytics.Reading{Up: true, Mem: f(mem), Latency: f(lat)})
		c.add(30 * time.Second)
	}
	r.Flush()
	for _, rng := range []string{"1h", "24h", "7d", "30d"} {
		s := analytics.Read(db, "nas", analytics.RangeNamed(rng), c.now(), true).Summary
		if !near(s.MemPeak, 98) || !near(s.LatencyPeak, 900) {
			t.Errorf("%s: memory peak %v, latency peak %v", rng, *s.MemPeak, *s.LatencyPeak)
		}
	}
}
