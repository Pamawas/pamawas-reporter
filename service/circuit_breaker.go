package service

import (
	"sync"
	"time"
)

// CircuitState is the current state of a circuit breaker.
type CircuitState string

const (
	CircuitStateClosed     CircuitState = "closed"
	CircuitStateOpen       CircuitState = "open"
	CircuitStateHalfOpen   CircuitState = "half-open"
)

// CircuitBreaker is a simple failure-threshold circuit breaker.
// It starts closed (requests allowed). After maxFailures consecutive failures
// it trips open (requests rejected immediately) for the given timeout, then
// allows a single trial request (half-open); success closes it again, failure
// reopens it.
type CircuitBreaker struct {
	mu            sync.Mutex
	state         CircuitState
	failures      int
	maxFailures   int
	timeout       time.Duration
	lastFailure   time.Time
	halfOpenCalls int // number of calls allowed in half-open state (1)
}

// NewCircuitBreaker creates a circuit breaker that trips open after
// maxFailures consecutive failures and stays open for timeout before
// allowing a single trial call.
func NewCircuitBreaker(maxFailures int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:      CircuitStateClosed,
		maxFailures: maxFailures,
		timeout:    timeout,
	}
}

// Allow returns true if a call is permitted right now.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitStateClosed:
		return true
	case CircuitStateOpen:
		if time.Since(cb.lastFailure) >= cb.timeout {
			cb.state = CircuitStateHalfOpen
			cb.halfOpenCalls = 1
			return true
		}
		return false
	case CircuitStateHalfOpen:
		if cb.halfOpenCalls > 0 {
			cb.halfOpenCalls--
			return true
		}
		return false
	}
	return false
}

// OnSuccess records a successful call, closing the circuit.
func (cb *CircuitBreaker) OnSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.state = CircuitStateClosed
	cb.halfOpenCalls = 0
}

// OnFailure records a failed call, potentially tripping the circuit open.
func (cb *CircuitBreaker) OnFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.lastFailure = time.Now()
	if cb.failures >= cb.maxFailures {
		cb.state = CircuitStateOpen
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Failures returns the current consecutive failure count.
func (cb *CircuitBreaker) Failures() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.failures
}

// StateSnapshot returns a snapshot of the circuit breaker state for observability.
type StateSnapshot struct {
	State   CircuitState `json:"state"`
	Failures int         `json:"failures"`
}

// Snapshot returns a copy of the current state for observability.
func (cb *CircuitBreaker) Snapshot() StateSnapshot {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return StateSnapshot{
		State:    cb.state,
		Failures: cb.failures,
	}
}
