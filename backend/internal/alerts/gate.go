// Package alerts throttles repeated alerts with escalating cooldowns and flap suppression.
package alerts

import (
	"math"
	"sync"
)

// Gate decides whether an ongoing condition may alert again.
//
// The first alert in an episode is sent immediately. While the condition keeps being reported,
// repeats wait base, then base*backoff, and so on up to max. An episode only ends once the
// condition has not been reported for recovery seconds, so a metric hovering around its
// threshold stays one episode instead of re-alerting every time it dips and rises.
type Gate struct {
	base, maxCooldown, backoff, recovery float64
	lastSeen, lastSent                   float64
	seen, sent                           bool
	sentCount                            int
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
	if g.sentCount <= 1 {
		return g.base
	}
	return math.Min(g.maxCooldown, g.base*math.Pow(g.backoff, float64(g.sentCount-1)))
}

// ShouldAlert reports that the condition is currently true and asks whether to alert now.
func (g *Gate) ShouldAlert(now float64) bool {
	if g.seen && now-g.lastSeen > g.recovery {
		g.sent = false
		g.sentCount = 0
	}
	g.lastSeen, g.seen = now, true
	if g.base <= 0 || !g.sent {
		return true
	}
	return now-g.lastSent >= g.CurrentCooldown()
}

// MarkSent records that an alert went out (or was logged).
func (g *Gate) MarkSent(now float64) {
	g.lastSent, g.sent = now, true
	g.sentCount++
}

// SecondsUntilNext is the remaining wait before the next repeat, for debug logging.
func (g *Gate) SecondsUntilNext(now float64) float64 {
	if !g.sent {
		return 0
	}
	return math.Max(0, g.CurrentCooldown()-(now-g.lastSent))
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.gates[key]; ok {
		return g
	}
	if len(s.gates) >= pruneAbove {
		for k, g := range s.gates {
			if !g.seen || now-g.lastSeen > s.recovery {
				delete(s.gates, k)
			}
		}
	}
	g := NewGate(s.base, s.maxCooldown, s.backoff, s.recovery)
	s.gates[key] = g
	return g
}
