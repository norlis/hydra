// Package conntrack provides instrumented net.Conn wrappers that
// track bytes transferred, last-activity timestamps, and lifecycle
// events. Adapted from Stripe's smokescreen (pkg/smokescreen/conntrack).
package conntrack

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// InstrumentedConn wraps a net.Conn and counts bytes flowing through it.
// LastActivity exposes a monotonic snapshot useful for idle-timeout
// enforcement; OnClose fires exactly once when Close completes.
//
// When idleTimeout > 0, an internal time.AfterFunc force-expires the
// underlying conn (via SetDeadline(now)) if no Read/Write touches it
// inside that window. The expiry side-effect is intentionally one-sided
// — we only set the deadline on the wrapped conn — because that's
// enough to unblock the splice goroutine reading or writing through it,
// which then closes both ends of the tunnel.
type InstrumentedConn struct {
	net.Conn

	bytesIn  atomic.Int64
	bytesOut atomic.Int64

	mu           sync.Mutex
	lastActivity time.Time
	createdAt    time.Time
	closed       bool
	onClose      func(stats Stats)

	idleTimeout time.Duration
	idleTimer   *time.Timer
}

// Stats captures the final counters of an InstrumentedConn at close.
type Stats struct {
	BytesIn   int64
	BytesOut  int64
	Duration  time.Duration
	CreatedAt time.Time
}

// Wrap turns an existing net.Conn into an InstrumentedConn with no idle
// watchdog. Use WrapWithIdle when an idle timeout is required.
func Wrap(c net.Conn, onClose func(Stats)) *InstrumentedConn {
	return WrapWithIdle(c, 0, onClose)
}

// WrapWithIdle wraps c and arms an idle watchdog. idle <= 0 disables
// the watchdog (equivalent to Wrap). When the timer fires, the
// underlying conn's deadline is set to now so any pending or future
// Read/Write fails immediately and the splice goroutine unwinds.
func WrapWithIdle(c net.Conn, idle time.Duration, onClose func(Stats)) *InstrumentedConn {
	now := time.Now()
	ic := &InstrumentedConn{
		Conn:         c,
		lastActivity: now,
		createdAt:    now,
		onClose:      onClose,
		idleTimeout:  idle,
	}
	if idle > 0 {
		// AfterFunc captures ic; the closure runs on its own goroutine
		// when the timer expires. Setting the deadline races benignly
		// with Close (a closed conn just returns ErrClosed which we
		// ignore).
		ic.idleTimer = time.AfterFunc(idle, ic.expire)
	}
	return ic
}

func (c *InstrumentedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.bytesIn.Add(int64(n))
		c.touch()
	}
	return n, err
}

func (c *InstrumentedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.bytesOut.Add(int64(n))
		c.touch()
	}
	return n, err
}

// Close idempotently closes the underlying conn and fires onClose with
// the final counters.
func (c *InstrumentedConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cb := c.onClose
	timer := c.idleTimer
	stats := Stats{
		BytesIn:   c.bytesIn.Load(),
		BytesOut:  c.bytesOut.Load(),
		Duration:  time.Since(c.createdAt),
		CreatedAt: c.createdAt,
	}
	c.mu.Unlock()

	// Stop is best-effort: it returns false if the timer already fired,
	// in which case expire's SetDeadline call is harmless on a closed
	// conn.
	if timer != nil {
		timer.Stop()
	}
	err := c.Conn.Close()
	if cb != nil {
		cb(stats)
	}
	return err
}

// LastActivity returns the most recent Read/Write timestamp.
func (c *InstrumentedConn) LastActivity() time.Time {
	c.mu.Lock()
	t := c.lastActivity
	c.mu.Unlock()
	return t
}

// BytesIn / BytesOut return live counters (safe to read concurrently).
func (c *InstrumentedConn) BytesIn() int64  { return c.bytesIn.Load() }
func (c *InstrumentedConn) BytesOut() int64 { return c.bytesOut.Load() }

func (c *InstrumentedConn) touch() {
	c.mu.Lock()
	c.lastActivity = time.Now()
	if c.idleTimer != nil {
		// Reset is safe on an active timer; on a fired-but-not-yet-run
		// timer it may race with the AfterFunc goroutine, but the
		// effect (deadline already set or about to be set on a still-
		// open conn that's actively reading) is harmless: the next
		// Read/Write succeeds, the touch arrives, and we're back to
		// armed.
		c.idleTimer.Reset(c.idleTimeout)
	}
	c.mu.Unlock()
}

// expire is the AfterFunc payload: nudge the underlying conn's
// deadline into the past so any blocked or upcoming syscall returns
// immediately. The error propagates through io.Copy in splice and
// causes both halves of the tunnel to close.
func (c *InstrumentedConn) expire() {
	_ = c.Conn.SetDeadline(time.Now())
}
