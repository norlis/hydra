// Package limiter caps how many concurrent CONNECT tunnels the proxy
// will hold open. The check is purely numeric — no per-client identity,
// no token bucket — and is the cheapest possible defense against
// FD exhaustion (a single misbehaving client opening thousands of
// tunnels). For per-client fairness see a future rate_limiter.
//
// Adapted from Stripe's smokescreen (pkg/smokescreen/tunnel_limiter.go).
package limiter

import "sync/atomic"

// Tunnel is a soft limit on simultaneously-open tunnels. Zero max
// disables the cap entirely; the limiter degenerates into a no-op.
type Tunnel struct {
	max    int64
	active atomic.Int64
}

// New builds a Tunnel limiter with the given cap. max <= 0 means
// "no cap" — Acquire/Release become free atomic-skip paths so callers
// don't need to special-case it.
func New(max int) *Tunnel {
	return &Tunnel{max: int64(max)}
}

// Acquire reserves a slot. Returns true on success and false when the
// cap was already reached; on false the counter is rolled back so the
// limiter remains exact under contention.
func (t *Tunnel) Acquire() bool {
	if t.max <= 0 {
		return true
	}
	if t.active.Add(1) > t.max {
		t.active.Add(-1)
		return false
	}
	return true
}

// Release frees a slot. Safe to call exactly once per successful
// Acquire; calling it without a prior Acquire would underflow the
// counter, so callers must pair them via defer.
func (t *Tunnel) Release() {
	if t.max <= 0 {
		return
	}
	t.active.Add(-1)
}

// Active returns a live snapshot of the in-flight count.
func (t *Tunnel) Active() int64 { return t.active.Load() }

// Limit returns the configured cap (0 == unlimited). Useful for
// exposing the value as a constant gauge or in /healthz.
func (t *Tunnel) Limit() int64 { return t.max }
