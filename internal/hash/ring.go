package hash

// RingStats is a snapshot of the current ring state, ideal for
// observability and debug endpoints.
type RingStats struct {
	TotalNodes      int               `json:"total_nodes"`
	TotalVNodes     int               `json:"total_vnodes"`
	ReplicasPerNode int               `json:"replicas_per_node"`
	ActiveNodes     map[string]string `json:"active_nodes"`
}

// Ring defines the behavior of a consistent hash ring.
type Ring interface {
	// AddNode adds a new node to the hash ring.
	AddNode(nodeID, address string)

	// RemoveNode removes a failed or departed node from the cluster.
	RemoveNode(nodeID string)

	// GetTargetNode takes an Entity-ID (e.g. "org-123") and returns the
	// address (IP:Port) of the node that owns that request.
	GetTargetNode(entityID string) string

	// SetNodes resets the ring with an entirely new set of nodes.
	SetNodes(nodes map[string]string)

	// GetStats returns the internal state of the ring for monitoring.
	GetStats() RingStats
}
