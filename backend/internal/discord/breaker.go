package discord

import (
	"errors"
	"sync"
	"time"
)

// ErrBreakerOpen is returned while the breaker refuses calls.
var ErrBreakerOpen = errors.New("circuit breaker is open")

type breakerState int

const (
	closed breakerState = iota
	open
	halfOpen
)

// CircuitBreaker stops calling a failing service for a cooldown, then probes it.
type CircuitBreaker struct {
	// Service names what it guards in its log lines ("qBittorrent", "Discord"), since several share
	// this one.
	Service          string
	mu               sync.Mutex
	failureThreshold int
	successThreshold int
	timeout          time.Duration
	failures         int
	successes        int
	lastFailure      time.Time
	state            breakerState
}

func NewCircuitBreaker(failureThreshold int, timeout time.Duration, successThreshold int) *CircuitBreaker {
	return &CircuitBreaker{failureThreshold: failureThreshold, timeout: timeout, successThreshold: successThreshold}
}

// Named sets the service the breaker guards, for its log lines.
func (b *CircuitBreaker) Named(service string) *CircuitBreaker {
	b.Service = service
	return b
}

// Call runs fn unless the breaker is open, and updates the breaker from its result.
func (b *CircuitBreaker) Call(fn func() error) error {
	b.mu.Lock()
	if b.state == open {
		if !b.lastFailure.IsZero() && time.Since(b.lastFailure) > b.timeout {
			log.Info("Circuit breaker half-open after timeout", "service", b.Service, "timeout", b.timeout)
			b.state, b.successes = halfOpen, 0
		} else {
			b.mu.Unlock()
			return ErrBreakerOpen
		}
	}
	b.mu.Unlock()

	err := fn()

	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.failures++
		b.lastFailure = time.Now()
		switch {
		case b.state == halfOpen:
			log.Warn("Circuit breaker re-opened: service still failing", "service", b.Service)
			b.state, b.successes = open, 0
		case b.state == closed && b.failures >= b.failureThreshold:
			log.Warn("Circuit breaker opening", "service", b.Service, "failures", b.failures, "timeout", b.timeout)
			b.state = open
		}
		return err
	}
	switch b.state {
	case halfOpen:
		b.successes++
		if b.successes >= b.successThreshold {
			log.Info("Circuit breaker closed: service recovered", "service", b.Service)
			b.state, b.failures, b.successes = closed, 0, 0
		}
	case closed:
		b.failures = 0
	}
	return nil
}

// IsOpen reports whether a call made now would be refused. Once the timeout has passed
// the breaker counts as closed here, so the next Call can probe the service.
func (b *CircuitBreaker) IsOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == open && time.Since(b.lastFailure) <= b.timeout
}
