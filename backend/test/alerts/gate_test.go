package alerts_test

import (
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
