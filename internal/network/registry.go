package network

import (
	"sync"
	"time"

	"github.com/norlis/hydra/internal/topology"
	"go.uber.org/zap"
)

// Registry periodically discovers interfaces through a Provider and
// keeps a concurrency-safe cache.
type Registry struct {
	provider Provider
	log      *zap.Logger

	mu     sync.RWMutex
	ifaces []topology.NetworkInterface

	stopCh chan struct{}
}

// NewRegistry initializes the registry, performs an initial discovery
// and launches a background loop that refreshes the cache.
func NewRegistry(provider Provider, log *zap.Logger) *Registry {
	r := &Registry{
		provider: provider,
		log:      log,
		stopCh:   make(chan struct{}),
	}

	r.Refresh()

	go r.loop()

	return r
}

// loop runs the refresh cycle every 60s until Stop() is called.
func (r *Registry) loop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			r.Refresh()
		case <-r.stopCh:
			r.log.Info("network registry discovery loop stopped")
			return
		}
	}
}

// Refresh forces a Provider read and updates the local cache.
func (r *Registry) Refresh() {
	ifaces, err := r.provider.Discover()
	if err != nil {
		r.log.Error("network discovery failed", zap.Error(err))
		return
	}

	r.mu.Lock()
	r.ifaces = ifaces
	r.mu.Unlock()

	r.log.Debug("interfaces discovered and cached", zap.Int("count", len(ifaces)))
}

// All returns a safe copy of every known interface.
func (r *Registry) All() []topology.NetworkInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Copy to prevent callers from mutating our cache.
	cp := make([]topology.NetworkInterface, len(r.ifaces))
	copy(cp, r.ifaces)

	return cp
}

// Stop halts the background discovery loop.
func (r *Registry) Stop() {
	close(r.stopCh)
}
