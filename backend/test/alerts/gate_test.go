package alerts_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"nightjar/internal/alerts"
)

func TestFirstAlertIsImmediate(t *testing.T) {
	if !alerts.NewGate(300, 3600, 2, 300).ShouldAlert(0) {
		t.Fatal("first alert should be immediate")
	}
}

func TestRepeatsBackOffAndCap(t *testing.T) {
	g := alerts.NewGate(100, 300, 2, 1000)
	g.ShouldAlert(0)
	g.MarkSent(0)
	if g.ShouldAlert(50) {
		t.Fatal("repeat before base cooldown")
	}
	if !g.ShouldAlert(100) {
		t.Fatal("repeat after base cooldown")
	}
	g.MarkSent(100)
	if g.ShouldAlert(250) || !g.ShouldAlert(300) {
		t.Fatal("second repeat should wait 200s")
	}
	g.MarkSent(300)
	g.MarkSent(300)
	if got := g.CurrentCooldown(); got != 300 {
		t.Fatalf("cooldown should cap at 300, got %v", got)
	}
}

func TestQuietPeriodStartsNewEpisode(t *testing.T) {
	g := alerts.NewGate(100, 1000, 2, 50)
	g.ShouldAlert(0)
	g.MarkSent(0)
	if !g.ShouldAlert(60) {
		t.Fatal("condition absent for longer than recovery should start a new episode")
	}
}

func TestShortDipDoesNotResetEpisode(t *testing.T) {
	g := alerts.NewGate(100, 1000, 2, 50)
	g.ShouldAlert(0)
	g.MarkSent(0)
	g.ShouldAlert(40)
	if g.ShouldAlert(80) {
		t.Fatal("a reappearance within recovery is the same episode")
	}
}

func TestZeroCooldownDisablesThrottling(t *testing.T) {
	g := alerts.NewGate(0, 0, 2, 0)
	g.ShouldAlert(0)
	g.MarkSent(0)
	if !g.ShouldAlert(0.1) {
		t.Fatal("zero cooldown should always alert")
	}
}

func TestGatesAreIndependent(t *testing.T) {
	s := alerts.NewGates(100, 100, 1, 10)
	a, b := s.Get("a", 0), s.Get("b", 0)
	a.ShouldAlert(0)
	a.MarkSent(0)
	if !b.ShouldAlert(1) {
		t.Fatal("gate b should not be affected by a")
	}
	if s.Get("a", 0) != a {
		t.Fatal("same key should return same gate")
	}
}

func TestAllowLetsOneOfManyThrough(t *testing.T) {
	g := alerts.NewGates(300, 3600, 2, 300).Get("service:api", 0)
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Allow(10) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("%d reports of the same condition at once alerted, want 1", allowed.Load())
	}
}

func TestPruneIsSafeWhileGatesAreInUse(t *testing.T) {
	s := alerts.NewGates(300, 3600, 2, 10)
	g := s.Get("busy", 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 2000 {
			g.Allow(float64(i))
		}
	}()
	for i := range 2000 {
		s.Get(fmt.Sprintf("proc:cpu_%d", i), float64(i))
	}
	<-done
}

func TestSlowConditionStaysOneEpisode(t *testing.T) {
	// Looked at every 600s, past the 300s recovery: each look must not start a new episode.
	s := alerts.NewGates(300, 3600, 2, 300)
	var sent []float64
	for now := 0.0; now <= 3000; now += 600 {
		if s.GetEvery("service:backup", now, 600).Allow(now) {
			sent = append(sent, now)
		}
	}
	if want := []float64{0, 600, 1200, 2400}; fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Fatalf("alerted at %v, want the backoff's %v", sent, want)
	}
	// A condition looked at every cycle still ends its episode after the recovery period.
	if !s.Get("cpu", 0).Allow(0) || s.Get("cpu", 100).Allow(100) || !s.Get("cpu", 500).Allow(500) {
		t.Fatal("a per-cycle gate ends its episode after 300s without a report")
	}
}

func TestSetRecoveryFollowsTheCheckInterval(t *testing.T) {
	s := alerts.NewGates(600, 3600, 2, 300)
	s.Get("cpu", 0).Allow(0)
	// The check interval was raised to 400s: cycles are now further apart than the old recovery.
	s.SetRecovery(1200)
	if s.Get("cpu", 400).Allow(400) {
		t.Fatal("the next cycle's report is the same episode, still in its cooldown")
	}
	if s.Get("new", 0).Allow(0); s.Get("new", 400).Allow(400) {
		t.Fatal("gates made after the change use the new recovery too")
	}
}

func TestClearEndsTheEpisode(t *testing.T) {
	s := alerts.NewGates(300, 3600, 2, 300)
	for now := 0.0; now <= 3000; now += 600 {
		s.GetEvery("service:api", now, 600).Allow(now)
	}
	// Recovered, then down again well inside three intervals of the last report.
	s.Clear("service:api")
	if !s.GetEvery("service:api", 3300, 600).Allow(3300) {
		t.Fatal("a new outage after a recovery alerts at once")
	}
	if s.GetEvery("service:api", 3400, 600).Allow(3400) {
		t.Fatal("the new outage still backs off")
	}
	s.Clear("never-seen") // a no-op
}
