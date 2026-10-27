package hash

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// DefaultReplicas is the number of virtual nodes per real node when the
// caller passes a non-positive count.
const DefaultReplicas = 50

// ConsistentHashRing maps keys to nodes with consistent hashing: each node
// owns `replicas` points on a 64-bit ring and a key belongs to the first
// node clockwise from its hash. Safe for concurrent use.
type ConsistentHashRing struct {
	mu sync.RWMutex

	// vnodes is the sorted set of every point on the ring.
	vnodes []uint64

	// owners maps a point to the node that owns it.
	owners map[uint64]string

	// nodeMap tracks the real nodes (nodeID -> address).
	nodeMap map[string]string

	replicas int
}

// NewConsistentHash creates an empty ring. replicas <= 0 uses DefaultReplicas.
func NewConsistentHash(replicas int) *ConsistentHashRing {
	if replicas <= 0 {
		replicas = DefaultReplicas
	}
	return &ConsistentHashRing{
		vnodes:   make([]uint64, 0),
		owners:   make(map[uint64]string),
		nodeMap:  make(map[string]string),
		replicas: replicas,
	}
}

// AddNode registers nodeID at address, creating its virtual points. A node
// already present with the same address is left untouched; one present
// with a different address is re-registered. Returns whether the ring
// changed.
func (c *ConsistentHashRing) AddNode(nodeID, address string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if old, exists := c.nodeMap[nodeID]; exists {
		if old == address {
			return false
		}
		c.removeLocked(nodeID)
	}
	c.nodeMap[nodeID] = address
	for i := range c.replicas {
		point := c.vnodeHash(nodeID, i)
		c.vnodes = append(c.vnodes, point)
		c.owners[point] = nodeID
	}
	slices.Sort(c.vnodes)
	return true
}

// RemoveNode removes a node and all its virtual points. Unknown ids are a
// no-op.
func (c *ConsistentHashRing) RemoveNode(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(nodeID)
}

func (c *ConsistentHashRing) removeLocked(nodeID string) {
	if _, exists := c.nodeMap[nodeID]; !exists {
		return
	}
	delete(c.nodeMap, nodeID)
	for i := range c.replicas {
		delete(c.owners, c.vnodeHash(nodeID, i))
	}
	c.vnodes = slices.DeleteFunc(c.vnodes, func(point uint64) bool {
		_, kept := c.owners[point]
		return !kept
	})
}

// GetTargetNode returns the node that owns key and its address. ok is
// false when the ring is empty. The same key always maps to the same node
// while the membership is unchanged.
func (c *ConsistentHashRing) GetTargetNode(key string) (nodeID, address string, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.vnodes) == 0 {
		return "", "", false
	}
	h := xxhash.Sum64String(key)
	idx := sort.Search(len(c.vnodes), func(i int) bool { return c.vnodes[i] >= h })
	if idx == len(c.vnodes) {
		idx = 0
	}
	nodeID = c.owners[c.vnodes[idx]]
	return nodeID, c.nodeMap[nodeID], true
}

// Len returns the number of real nodes on the ring.
func (c *ConsistentHashRing) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.nodeMap)
}

// NodeIDs returns a snapshot of the registered node ids, in no particular
// order.
func (c *ConsistentHashRing) NodeIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.nodeMap))
	for id := range c.nodeMap {
		ids = append(ids, id)
	}
	return ids
}

func (c *ConsistentHashRing) vnodeHash(nodeID string, replica int) uint64 {
	return xxhash.Sum64String(fmt.Sprintf("%s-%d", nodeID, replica))
}
