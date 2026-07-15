package cluster

import (
	"fmt"
	"log/slog"
	"sync"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/hash"
	"github.com/norlis/hydra/internal/topology"
)

// Endpoint is the resolved target returned by the ring for a given
// entityID: the peer that owns that entity plus a flag telling whether
// that peer is this node.
type Endpoint struct {
	NodeID    string
	ProxyAddr string // host:port reachable by peers
	IsSelf    bool
}

// Ring is a gossip-driven consistent hash ring. It subscribes to the
// internal event bus at construction time and rebalances itself as
// nodes join, leave or update.
type Ring struct {
	hash   *hash.ConsistentHashRing
	selfID string
	log    *slog.Logger

	mu    sync.RWMutex
	nodes map[string]string // nodeID -> proxyAddr
}

// NewRing builds an empty ring and starts the subscription loop.
func NewRing(cfg *hydra.Config, eventBus bus.EventBus, log *slog.Logger) *Ring {
	r := &Ring{
		hash:   hash.NewConsistentHash(50),
		selfID: cfg.GossIPNodeName,
		log:    log,
		nodes:  make(map[string]string),
	}
	go r.run(eventBus.Subscribe())
	return r
}

// run consumes cluster events and mutates the ring. It exits when the
// application terminates and the event channel is abandoned.
func (r *Ring) run(ch <-chan bus.ClusterEvent) {
	for ev := range ch {
		switch ev.Type {
		case bus.NodeJoined, bus.NodeUpdated:
			// Register each network interface as a separate target in the ring
			for _, iface := range ev.Node.Interfaces {
				if iface.PrivateIP == "" || iface.ServicePort == 0 {
					continue
				}
				addr := fmt.Sprintf("%s:%d", iface.PrivateIP, iface.ServicePort)

				// Use a safe separator "::" to avoid conflicts if Node.ID contains hyphens
				virtualID := fmt.Sprintf("%s::%s", ev.Node.ID, iface.Name)
				r.replace(virtualID, addr)
			}
		case bus.NodeLeft:
			// When a node leaves, remove all its interfaces from the ring
			for _, iface := range ev.Node.Interfaces {
				virtualID := fmt.Sprintf("%s::%s", ev.Node.ID, iface.Name)
				r.remove(virtualID)
			}
		}
	}
}

// replace removes any previous entry for nodeID and adds the new one.
// This handles NodeUpdated cleanly when a peer's address changes.
func (r *Ring) replace(nodeID, addr string) {
	r.mu.Lock()
	if old, ok := r.nodes[nodeID]; ok && old == addr {
		r.mu.Unlock()
		return
	}
	r.nodes[nodeID] = addr
	r.mu.Unlock()

	r.hash.RemoveNode(nodeID)
	r.hash.AddNode(nodeID, addr)
	r.log.Debug("ring: node added", slog.String("node", nodeID), slog.String("addr", addr))
}

func (r *Ring) remove(nodeID string) {
	r.mu.Lock()
	_, existed := r.nodes[nodeID]
	delete(r.nodes, nodeID)
	r.mu.Unlock()
	if existed {
		r.hash.RemoveNode(nodeID)
		r.log.Debug("ring: node removed", slog.String("node", nodeID))
	}
}

// EndpointFor returns the node that owns entityID, or nil when the
// ring has not yet been populated.
func (r *Ring) EndpointFor(entityID string) *Endpoint {
	addr := r.hash.GetTargetNode(entityID)
	if addr == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, a := range r.nodes {
		if a == addr {
			return &Endpoint{
				NodeID:    id,
				ProxyAddr: a,
				IsSelf:    id == r.selfID,
			}
		}
	}
	return nil
}

// Len returns the number of real nodes currently in the ring.
func (r *Ring) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// proxyAddr derives the canonical proxy address from a node: the
// PrivateIP + ServicePort of its first interface. Returns empty if
// the node has no usable interface yet (e.g. a stub published before
// its NodeMeta arrived).
func proxyAddr(n topology.Node) string {
	if len(n.Interfaces) == 0 {
		return ""
	}
	i := n.Interfaces[0]
	if i.PrivateIP == "" || i.ServicePort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", i.PrivateIP, i.ServicePort)
}
