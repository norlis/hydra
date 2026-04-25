package hash

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// ConsistentHashRing is a deterministic, immutable consistent hash ring.
type ConsistentHashRing struct {
	mu sync.RWMutex

	// vnodes is a sorted slice of every hash on the ring.
	vnodes []uint64

	// ring maps a virtual-node hash to its real address (IP:Port).
	ring map[uint64]string

	// nodeMap tracks real active nodes (NodeID -> Address).
	nodeMap map[string]string

	// replicas is the number of VNodes per real node for this instance.
	replicas int
}

// NewConsistentHash creates a new ring. replicas <= 0 falls back to 50.
func NewConsistentHash(replicas int) *ConsistentHashRing {
	if replicas <= 0 {
		replicas = 50
	}

	return &ConsistentHashRing{
		vnodes:   make([]uint64, 0),
		ring:     make(map[uint64]string),
		nodeMap:  make(map[string]string),
		replicas: replicas,
	}
}

// AddNode adds a node to the ring, creating its virtual replicas.
func (c *ConsistentHashRing) AddNode(nodeID, address string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.nodeMap[nodeID]; exists {
		return
	}
	c.nodeMap[nodeID] = address

	for i := range c.replicas {
		vnodeKey := fmt.Sprintf("%s-%d", nodeID, i)
		hash := xxhash.Sum64([]byte(vnodeKey))

		c.vnodes = append(c.vnodes, hash)
		c.ring[hash] = address
	}

	slices.Sort(c.vnodes)
}

// RemoveNode removes a node and all its VNodes from the ring.
func (c *ConsistentHashRing) RemoveNode(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.nodeMap[nodeID]; !exists {
		return
	}
	delete(c.nodeMap, nodeID)

	for i := range c.replicas {
		vnodeKey := fmt.Sprintf("%s-%d", nodeID, i)
		hash := xxhash.Sum64([]byte(vnodeKey))

		delete(c.ring, hash)
	}

	var newVnodes []uint64
	for _, vnodeHash := range c.vnodes {
		if _, exists := c.ring[vnodeHash]; exists {
			newVnodes = append(newVnodes, vnodeHash)
		}
	}
	c.vnodes = newVnodes
}

// GetTargetNode always returns the same address for a given entityID
// (the routing guarantee of consistent hashing).
func (c *ConsistentHashRing) GetTargetNode(entityID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.vnodes) == 0 {
		return ""
	}

	hash := xxhash.Sum64String(entityID)

	idx := sort.Search(len(c.vnodes), func(i int) bool {
		return c.vnodes[i] >= hash
	})

	if idx == len(c.vnodes) {
		idx = 0
	}

	return c.ring[c.vnodes[idx]]
}

// GetStats returns a safe snapshot of the ring state.
func (c *ConsistentHashRing) GetStats() RingStats {
	// Read-lock to avoid blocking the routing hot path.
	c.mu.RLock()
	defer c.mu.RUnlock()

	nodesCopy := make(map[string]string, len(c.nodeMap))
	maps.Copy(nodesCopy, c.nodeMap)

	return RingStats{
		TotalNodes:      len(c.nodeMap),
		TotalVNodes:     len(c.vnodes),
		ReplicasPerNode: c.replicas,
		ActiveNodes:     nodesCopy,
	}
}
