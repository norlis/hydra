package bus

import (
	"fmt"

	"github.com/norlis/hydra/internal/topology"
)

// EventType identifies what happened in the cluster.
type EventType int

const (
	// NodeJoined signals that a new node entered the P2P network.
	// The consistent hash ring should add it.
	NodeJoined EventType = iota

	// NodeLeft signals that a node failed or gracefully left.
	// The consistent hash ring should remove it.
	NodeLeft

	// NodeUpdated is optional; useful when hot-changing metadata
	// (e.g. role or capacity).
	NodeUpdated
)

func (e EventType) String() string {
	switch e {
	case NodeJoined:
		return "node.joined"
	case NodeLeft:
		return "node.left"
	case NodeUpdated:
		return "node.updated"
	default:
		return fmt.Sprintf("node.unknown_event_%d", int(e))
	}
}

// ClusterEvent describes a topology change.
type ClusterEvent struct {
	ID   string
	Type EventType
	Node topology.Node
}

type EventBus interface {
	// Subscribe registers a new listener and returns a read-only channel
	// that will receive cluster events.
	Subscribe() <-chan ClusterEvent

	Unsubscribe(ch <-chan ClusterEvent)

	// Publish fans the event out to every subscriber concurrently.
	Publish(event ClusterEvent)
}
