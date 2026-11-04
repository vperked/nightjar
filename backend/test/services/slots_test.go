package services_test

import (
	"math"
	"testing"
	"time"

	"nightjar/internal/services"
	"nightjar/internal/storage"
)

// slotSeconds is the in-memory history resolution; ranges of a slot or more per point are served from memory.
const slotSeconds = 300

func TestMemoryHistoryMatchesStore(t *testing.T) {
	store := storage.Open(":memory:") // one connection, so the in-memory database persists across calls
	defer store.Close()
	m := services.NewMonitor([]services.Spec{{Name: "svc", Interval: 120}}, store)
	// Slot-aligned range, checks every 2 minutes for 3 days: hourly buckets line up with slots.
	until := float64(time.Now().Unix() / slotSeconds * slotSeconds)
	since := until - 3*86400
	for i := 3*720 - 1; i >= 0; i-- {
		ts := until - 1 - float64(i)*120
		lat := 40 + float64(i%17)
		r := services.Result{Up: i%97 != 0, LatencyMS: &lat, Detail: "HTTP 502"}
		if !r.Up {
			r.LatencyMS = nil
		}
		m.Record("svc", r, ts)
	}
	got, want := m.History("svc", since, until, 72), store.Buckets("svc", since, until, 72)
	if len(got) != len(want) {
		t.Fatalf("got %d buckets, store %d", len(got), len(want))
	}
	totalFails := 0
	for i := range got {
		g, w := got[i], want[i]
		totalFails += g.Fails
		if g.TS > w.TS || g.Fails != w.Fails || g.Up != w.Up || math.Abs(*g.LatencyMS-*w.LatencyMS) > 1e-9 {
			t.Fatalf("bucket %d: memory %+v store %+v", i, g, w)
		}
	}
	if totalFails == 0 {
		t.Fatal("test data should include failures")
	}
	if u := m.Uptime("svc", since); u == nil || math.Abs(*u-100*96.0/97) > 0.1 {
		t.Fatalf("uptime %v", u)
	}
}

func TestMemoryHistoryReusesOldSlots(t *testing.T) {
	m := services.NewMonitor([]services.Spec{{Name: "svc", Interval: 60}}, nil)
	m.Record("svc", services.Result{Up: false, Detail: "old"}, 1000)
	m.Record("svc", services.Result{Up: true}, 1000+30*86400) // one lap of the 30-day ring later
	if u := m.Uptime("svc", 0); u == nil || *u != 100 {
		t.Fatalf("a slot from the previous lap must be reset, uptime %v", u)
	}
}

func BenchmarkHistory30d(b *testing.B) {
	store := storage.Open(":memory:")
	defer store.Close()
	m := services.NewMonitor([]services.Spec{{Name: "svc", Interval: 60}}, store)
	now := float64(time.Now().Unix())
	lat := 42.0
	for i := 0; i < 30*1440; i++ {
		m.Record("svc", services.Result{Up: i%97 != 0, LatencyMS: &lat}, now-float64(i)*60)
	}
	b.Run("memory", func(b *testing.B) {
		for range b.N {
			m.History("svc", now-30*86400, now, 720)
		}
	})
	b.Run("sqlite", func(b *testing.B) {
		for range b.N {
			store.Buckets("svc", now-30*86400, now, 720)
		}
	})
}
