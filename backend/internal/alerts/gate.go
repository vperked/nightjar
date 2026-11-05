// Package alerts throttles repeated alerts with escalating cooldowns and flap suppression.
package alerts

import (
	"math"
	"sync"
)

// Gate decides whether an ongoing condition may alert again. Its methods are safe for
// concurrent use.
//
// The first alert in an episode is sent immediately. While the condition keeps being reported,
// repeats wait base, then base*backoff, and so on up to max. An episode only ends once the
// condition has not been reported for recovery seconds, so a metric hovering around its
// threshold stays one episode instead of re-alerting every time it dips and rises.
type Gate struct {
	mu                                   sync.Mutex
	base, maxCooldown, backoff, recovery float64
	// floor keeps recovery at least this long whatever the gate set's recovery is: three of the
	// caller's own intervals, for a condition looked at less often than every cycle.
	floor              float64
	lastSeen, lastSent float64
	seen, sent         bool
	sentCount          int
}

// NewGate builds a gate, normalizing out-of-range arguments.
func NewGate(base, maxCooldown, backoff, recovery float64) *Gate {
	base = math.Max(0, base)
	return &Gate{
		base:        base,
		maxCooldown: math.Max(base, maxCooldown),
		backoff:     math.Max(1, backoff),
		recovery:    math.Max(0, recovery),
	}
}

// CurrentCooldown is the wait before the next repeat in this episode.
func (g *Gate) CurrentCooldown() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.currentCooldown()
}

func (g *Gate) currentCooldown() float64 {
	if g.sentCount <= 1 {
		return g.base
	}
	return math.Min(g.maxCooldown, g.base*math.Pow(g.backoff, float64(g.sentCount-1)))
}

// ShouldAlert reports that the condition is currently true and asks whether to alert now.
func (g *Gate) ShouldAlert(now float64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.shouldAlert(now)
}

func (g *Gate) shouldAlert(now float64) bool {
	if g.seen && now-g.lastSeen > g.recovery {
		g.sent = false
		g.sentCount = 0
	}
	g.lastSeen, g.seen = now, true
	if g.base <= 0 || !g.sent {
		return true
	}
	return now-g.lastSent >= g.currentCooldown()
}

// MarkSent records that an alert went out (or was logged).
func (g *Gate) MarkSent(now float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markSent(now)
}

func (g *Gate) markSent(now float64) {
	g.lastSent, g.sent = now, true
	g.sentCount++
}

// Allow is ShouldAlert and, when it says yes, MarkSent, in one step: of two callers reporting the
// same condition at once, only one is let through.
func (g *Gate) Allow(now float64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.shouldAlert(now) {
		return false
	}
	g.markSent(now)
	return true
}

// SecondsUntilNext is the remaining wait before the next repeat, for debug logging.
func (g *Gate) SecondsUntilNext(now float64) float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sent {
		return 0
	}
	return math.Max(0, g.currentCooldown()-(now-g.lastSent))
}

// tune sets the gate's floor, and its recovery from the set's and that floor.
func (g *Gate) tune(recovery, floor float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.floor = math.Max(0, floor)
	g.recovery = math.Max(math.Max(0, recovery), g.floor)
}

// setRecovery is tune keeping the gate's floor.
func (g *Gate) setRecovery(recovery float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recovery = math.Max(math.Max(0, recovery), g.floor)
}

func (g *Gate) idle(now float64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.seen || now-g.lastSeen > g.recovery
}

// Gates lazily creates one Gate per alert key and prunes idle ones.
type Gates struct {
	mu                                   sync.Mutex
	base, maxCooldown, backoff, recovery float64
	gates                                map[string]*Gate
}

const pruneAbove = 256

// NewGates builds a gate set whose gates share these parameters.
func NewGates(base, maxCooldown, backoff, recovery float64) *Gates {
	return &Gates{base: base, maxCooldown: maxCooldown, backoff: backoff, recovery: recovery, gates: map[string]*Gate{}}
}

// Get returns the gate for key, creating it on first use.
func (s *Gates) Get(key string, now float64) *Gate {
	return s.GetEvery(key, now, 0)
}

// GetEvery is Get for a condition looked at only every interval seconds (0 for every cycle):
// its episode lasts at least three intervals without a report, so a slow check still backs off
// instead of starting a new episode, and alerting at once, on every look.
func (s *Gates) GetEvery(key string, now, interval float64) *Gate {
	s.mu.Lock()
	defer s.mu.Unlock()
	floor := 3 * math.Max(0, interval)
	if g, ok := s.gates[key]; ok {
		if interval > 0 {
			g.tune(s.recovery, floor)
		}
		return g
	}
	if len(s.gates) >= pruneAbove {
		for k, g := range s.gates {
			if g.idle(now) {
				delete(s.gates, k)
			}
		}
	}
	g := NewGate(s.base, s.maxCooldown, s.backoff, s.recovery)
	g.tune(s.recovery, floor)
	s.gates[key] = g
	return g
}

// Clear ends key's episode, for a condition seen to recover: its next report alerts at once
// instead of waiting out the cooldown it had backed off to.
func (s *Gates) Clear(key string) {
	s.mu.Lock()
	delete(s.gates, key)
	s.mu.Unlock()
}

// SetRecovery changes how long a condition must go unreported to end its episode, for every gate
// (each keeping its own floor); for a check interval changed while running.
func (s *Gates) SetRecovery(recovery float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recovery = recovery
	for _, g := range s.gates {
		g.setRecovery(recovery)
	}
}
