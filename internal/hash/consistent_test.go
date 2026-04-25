package hash

import (
	"fmt"
	"sync"
	"testing"
)

func TestNewConsistentHash(t *testing.T) {
	t.Parallel()

	t.Run("default vnodes", func(t *testing.T) {
		t.Parallel()

		ring := NewConsistentHash(0)
		if ring.replicas != 50 {
			t.Errorf("expected 50 default replicas, got %d", ring.replicas)
		}
	})

	t.Run("custom vnodes", func(t *testing.T) {
		t.Parallel()

		ring := NewConsistentHash(100)
		if ring.replicas != 100 {
			t.Errorf("expected 100 custom replicas, got %d", ring.replicas)
		}
	})
}

func TestConsistentHashRing_AddRemoveNode(t *testing.T) {
	t.Parallel()

	// 10 replicas keeps the math simple for this test.
	ring := NewConsistentHash(10)

	// Add a node.
	ring.AddNode("nodeA", "10.0.0.1:8080")
	if len(ring.nodeMap) != 1 {
		t.Errorf("expected 1 real node, got %d", len(ring.nodeMap))
	}
	if len(ring.vnodes) != 10 {
		t.Errorf("expected 10 virtual vnodes, got %d", len(ring.vnodes))
	}

	// Idempotency on add: re-adding the same node must not duplicate load.
	ring.AddNode("nodeA", "10.0.0.1:8080")
	if len(ring.vnodes) != 10 {
		t.Errorf("expected 10 vnodes after duplicate add, got %d", len(ring.vnodes))
	}

	// Add a second node.
	ring.AddNode("nodeB", "10.0.0.2:8080")
	if len(ring.vnodes) != 20 {
		t.Errorf("expected 20 total vnodes, got %d", len(ring.vnodes))
	}

	// Remove a node.
	ring.RemoveNode("nodeA")
	if len(ring.nodeMap) != 1 {
		t.Errorf("expected 1 remaining node, got %d", len(ring.nodeMap))
	}
	if len(ring.vnodes) != 10 {
		t.Errorf("expected 10 vnodes after removal, got %d", len(ring.vnodes))
	}

	// Idempotency on remove: removing an absent node must not break anything.
	ring.RemoveNode("nodeA")
	if len(ring.vnodes) != 10 {
		t.Errorf("expected 10 vnodes after absent-node removal, got %d", len(ring.vnodes))
	}
}

func TestConsistentHashRing_GetTargetNode(t *testing.T) {
	t.Parallel()

	ring := NewConsistentHash(50)

	t.Run("empty ring", func(t *testing.T) {
		t.Parallel()
		target := ring.GetTargetNode("org-123")
		if target != "" {
			t.Errorf("expected empty string, got %s", target)
		}
	})

	t.Run("deterministic routing (immutability)", func(t *testing.T) {
		t.Parallel()

		ring.AddNode("nodeA", "10.0.0.1")
		ring.AddNode("nodeB", "10.0.0.2")
		ring.AddNode("nodeC", "10.0.0.3")

		// Route 100 different IDs and store the results.
		routes := make(map[string]string)
		for i := range 100 {
			entityID := fmt.Sprintf("org-%d", i)
			routes[entityID] = ring.GetTargetNode(entityID)
		}

		// Second pass: immutability guarantees the same results.
		for i := range 100 {
			entityID := fmt.Sprintf("org-%d", i)
			target := ring.GetTargetNode(entityID)

			if target != routes[entityID] {
				t.Fatalf("immutability failure: entity %s first routed to %s, now to %s",
					entityID, routes[entityID], target)
			}
		}
	})
}

func TestConsistentHashRing_ConcurrencySafe(t *testing.T) {
	t.Parallel()

	ring := NewConsistentHash(50)
	ring.AddNode("node-seed", "192.168.1.1")

	var wg sync.WaitGroup
	workers := 200

	// 200 goroutines performing reads, adds and deletes concurrently.
	// If sync.RWMutex is misused the race detector or a panic fails the test.

	wg.Add(workers * 2)

	// Simulate 200 HTTP requests reading which node to route to.
	for i := range workers {
		go func(id int) {
			defer wg.Done()
			entityID := fmt.Sprintf("tenant-%d", id)
			_ = ring.GetTargetNode(entityID)
		}(i)
	}

	// Simulate the gossip event bus adding and removing nodes aggressively.
	for i := range workers {
		go func(id int) {
			defer wg.Done()
			nodeID := fmt.Sprintf("node-%d", id)
			ring.AddNode(nodeID, "localhost:8080")

			// Remove half of the nodes immediately to create stress.
			if id%2 == 0 {
				ring.RemoveNode(nodeID)
			}
		}(i)
	}

	wg.Wait()
	// Reaching this point without a race-detector panic means the ring
	// is safe for production-level concurrency.
}
