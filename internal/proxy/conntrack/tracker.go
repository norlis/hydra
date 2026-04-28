package conntrack

import (
	"sync"
	"sync/atomic"
)

// Tracker keeps a registry of active InstrumentedConns. Useful for
// graceful shutdown (force-close every active tunnel) and metrics.
type Tracker struct {
	mu     sync.Mutex
	active map[*InstrumentedConn]struct{}
	count  atomic.Int64
}

func NewTracker() *Tracker {
	return &Tracker{active: make(map[*InstrumentedConn]struct{})}
}

// Add registers a new conn and wires its onClose so the tracker
// automatically removes it when the conn closes. The original onClose
// supplied to Wrap (if any) still runs first.
func (t *Tracker) Add(c *InstrumentedConn) {
	prev := c.onClose
	c.onClose = func(s Stats) {
		if prev != nil {
			prev(s)
		}
		t.remove(c)
	}
	t.mu.Lock()
	t.active[c] = struct{}{}
	t.mu.Unlock()
	t.count.Add(1)
}

func (t *Tracker) remove(c *InstrumentedConn) {
	t.mu.Lock()
	if _, ok := t.active[c]; ok {
		delete(t.active, c)
		t.count.Add(-1)
	}
	t.mu.Unlock()
}

// Count returns the number of currently active conns.
func (t *Tracker) Count() int64 { return t.count.Load() }

// CloseAll force-closes every tracked conn. Returns the number of
// conns it tried to close.
func (t *Tracker) CloseAll() int {
	t.mu.Lock()
	conns := make([]*InstrumentedConn, 0, len(t.active))
	for c := range t.active {
		conns = append(conns, c)
	}
	t.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}
