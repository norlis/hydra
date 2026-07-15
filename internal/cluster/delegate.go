package cluster

import (
	"encoding/json"
	"log/slog"

	"github.com/hashicorp/memberlist"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/pkg/logger"
)

// NodeDelegate listens to HashiCorp memberlist events and translates
// them into ClusterEvents on our internal bus.
type NodeDelegate struct {
	eventBus bus.EventBus
	// Returns the current local node snapshot (injected from MemberlistDiscovery).
	getLocalNode func() topology.Node
	log          *slog.Logger
}

// NodeMeta is invoked when memberlist needs to share this node's metadata.
// The JSON payload is zlib-compressed so it fits in memberlist's 512-byte
// NodeMeta budget even as topology.Node gains fields.
func (d *NodeDelegate) NodeMeta(limit int) []byte {
	node := d.getLocalNode()
	raw, err := json.Marshal(node)
	if err != nil {
		d.log.Error("failed to serialize node", logger.Err(err))
		return nil
	}

	data := encodeMeta(raw)
	if len(data) > limit {
		d.log.Warn("metadata too large even after compression",
			slog.Int("raw", len(raw)),
			slog.Int("compressed", len(data)),
			slog.Int("limit", limit))
		return nil
	}
	return data
}

// NotifyMsg is invoked when we receive a message from another node.
func (d *NodeDelegate) NotifyMsg(msg []byte) {
	// Hook for node-to-node control messages.
}

// GetBroadcasts is used to send messages across the network (not needed
// for state sharing alone).
func (d *NodeDelegate) GetBroadcasts(overhead, limit int) [][]byte {
	return nil
}

// LocalState is used to sync full state between nodes.
func (d *NodeDelegate) LocalState(join bool) []byte {
	// Allow a larger limit for initial sync.
	return d.NodeMeta(1024 * 10)
}

// MergeRemoteState is invoked when we receive another node's full state.
func (d *NodeDelegate) MergeRemoteState(buf []byte, join bool) {
	raw, err := decodeMeta(buf)
	if err != nil {
		d.log.Error("failed to decompress remote state", logger.Err(err))
		return
	}
	var node topology.Node
	if err := json.Unmarshal(raw, &node); err != nil {
		d.log.Error("failed to deserialize remote state", logger.Err(err))
		return
	}
	// Hook to update a global node cache.
	_ = node
}

// NotifyJoin fires when a healthy node joins the network.
func (d *NodeDelegate) NotifyJoin(node *memberlist.Node) {
	d.log.Info("node joined the cluster", slog.String("node_id", node.Name), slog.String("address", node.Addr.String()))
	d.eventBus.Publish(bus.ClusterEvent{
		Type: bus.NodeJoined,
		Node: decodeMember(node, d.log),
	})
}

// NotifyLeave fires when a node fails or gracefully leaves.
func (d *NodeDelegate) NotifyLeave(node *memberlist.Node) {
	d.log.Info("node left the cluster", slog.String("node_id", node.Name), slog.String("address", node.Addr.String()))
	d.eventBus.Publish(bus.ClusterEvent{
		Type: bus.NodeLeft,
		Node: decodeMember(node, d.log),
	})
}

// NotifyUpdate fires when a node's metadata changes at runtime.
func (d *NodeDelegate) NotifyUpdate(node *memberlist.Node) {
	d.log.Debug("node updated", slog.String("node_id", node.Name), slog.String("address", node.Addr.String()))
	d.eventBus.Publish(bus.ClusterEvent{
		Type: bus.NodeUpdated,
		Node: decodeMember(node, d.log),
	})
}
