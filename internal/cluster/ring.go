package cluster

import (
	"log/slog"
	"strings"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/hash"
	"github.com/norlis/hydra/internal/topology"
)

// virtualIDSep joins a node id and an interface name into the ring entry
// id ("node-a::eth0"). "::" cannot appear in a memberlist node name.
const virtualIDSep = "::"

// Endpoint is the resolved target returned by the ring for a given
// entityID: the peer that owns that entity plus a flag telling whether
// that peer is this node.
type Endpoint struct {
	NodeID    string // virtual id: "<node>::<iface>"
	Iface     string
	ProxyAddr string // host:port reachable by peers
	IsSelf    bool
}

// Ring is a gossip-driven consistent hash ring. Every network interface
// of every member is a separate entry, so a request is pinned to one NIC
// of one node. It subscribes to the event bus at construction time and
// rebalances itself as nodes join, leave or update.
type Ring struct {
	hash   *hash.ConsistentHashRing
	selfID string
	log    *slog.Logger

	// bus and events are kept only so Stop can end the run loop.
	bus    bus.EventBus
	events <-chan bus.ClusterEvent
}

// NewRing builds an empty ring and starts the subscription loop.
func NewRing(cfg *hydra.Config, eventBus bus.EventBus, log *slog.Logger) *Ring {
	r := &Ring{
		hash:   hash.NewConsistentHash(hash.DefaultReplicas),
		selfID: cfg.GossIPNodeName,
		log:    log,
		bus:    eventBus,
		events: eventBus.Subscribe(),
	}
	go r.run(r.events)
	return r
}

// Stop unsubscribes from the bus, which closes the event channel and ends
// the run loop. The ring keeps serving its last known topology.
func (r *Ring) Stop() {
	r.bus.Unsubscribe(r.events)
}

// run consumes cluster events and mutates the ring until the channel closes.
func (r *Ring) run(ch <-chan bus.ClusterEvent) {
	for ev := range ch {
		switch ev.Type {
		case bus.NodeJoined, bus.NodeUpdated:
			r.sync(ev.Node)
		case bus.NodeLeft:
			// Remove by node id rather than by the interfaces the event
			// carries: a peer whose metadata could not be decoded arrives
			// with a stub interface that never matches what was registered.
			r.removeNode(ev.Node.ID, nil)
		}
	}
}

// sync makes the ring hold exactly the interfaces the event lists. A node
// that restarts with fewer interfaces may never be seen leaving, only
// joining again, so entries it no longer serves must go here.
func (r *Ring) sync(node topology.Node) {
	served := make(map[string]struct{}, len(node.Interfaces))
	for _, iface := range node.Interfaces {
		if iface.PrivateIP == "" || iface.ServicePort == 0 {
			continue
		}
		id := VirtualID(node.ID, iface.Name)
		served[id] = struct{}{}
		addr := node.ProxyAddress(iface)
		if r.hash.AddNode(id, addr) {
			r.log.Debug("ring: node added", slog.String("node", id), slog.String("addr", addr))
		}
	}
	// No servable interface means undecodable metadata, not an empty node:
	// keeping the known entries is safer than dropping a live peer.
	if len(served) > 0 {
		r.removeNode(node.ID, served)
	}
}

// removeNode drops every entry of nodeID except those in keep.
func (r *Ring) removeNode(nodeID string, keep map[string]struct{}) {
	prefix := nodeID + virtualIDSep
	for _, id := range r.hash.NodeIDs() {
		if _, kept := keep[id]; strings.HasPrefix(id, prefix) && !kept {
			r.hash.RemoveNode(id)
			r.log.Debug("ring: node removed", slog.String("node", id))
		}
	}
}

// EndpointFor returns the entry that owns entityID, or nil when the ring
// is empty.
func (r *Ring) EndpointFor(entityID string) *Endpoint {
	id, addr, ok := r.hash.GetTargetNode(entityID)
	if !ok {
		return nil
	}
	node, iface, _ := strings.Cut(id, virtualIDSep)
	return &Endpoint{NodeID: id, Iface: iface, ProxyAddr: addr, IsSelf: node == r.selfID}
}

// Len returns the number of entries (node × interface) currently in the ring.
func (r *Ring) Len() int {
	return r.hash.Len()
}

// VirtualID builds the ring entry id for one interface of a node.
func VirtualID(nodeID, ifaceName string) string {
	return nodeID + virtualIDSep + ifaceName
}

// LogicalNodeID returns the node name from a ring entry id, dropping the
// "::iface" suffix. Ids without the separator are returned unchanged.
func LogicalNodeID(virtualID string) string {
	if node, _, found := strings.Cut(virtualID, virtualIDSep); found {
		return node
	}
	return virtualID
}
