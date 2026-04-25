package cluster

import (
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/topology"
)

// Discovery describes how the application interacts with the gossip
// network.
type Discovery interface {
	// Start initializes the gossip protocol and joins the cluster if
	// there are seeds available.
	Start() error

	// Stop closes the gossip channel gracefully.
	Stop() error

	// GetActiveNodes returns the list of nodes currently considered alive.
	GetActiveNodes() []topology.Node

	// GetLocalNode returns information about this instance.
	GetLocalNode() topology.Node

	// Subscribe registers a channel that receives real-time notifications
	// when a node joins or leaves the cluster.
	Subscribe() <-chan bus.ClusterEvent
}
