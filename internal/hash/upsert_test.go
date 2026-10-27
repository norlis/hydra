package hash_test

import (
	"slices"
	"testing"

	"github.com/norlis/hydra/internal/hash"
)

func TestConsistentHashRing_AddNodeUpsert(t *testing.T) {
	t.Parallel()
	r := hash.NewConsistentHash(10)

	if !r.AddNode("a", "10.0.0.1:1") {
		t.Fatal("first AddNode = false, want true")
	}
	if r.AddNode("a", "10.0.0.1:1") {
		t.Error("same address again = true, want false (no-op)")
	}
	if !r.AddNode("a", "10.0.0.9:1") {
		t.Error("new address = false, want true (replaced)")
	}
	if _, addr, ok := r.GetTargetNode("k"); !ok || addr != "10.0.0.9:1" {
		t.Errorf("GetTargetNode = %q %v, want new address", addr, ok)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1 after replace", r.Len())
	}

	r.AddNode("b", "10.0.0.2:1")
	ids := r.NodeIDs()
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("NodeIDs = %v", ids)
	}
	r.RemoveNode("missing")
	if r.Len() != 2 {
		t.Errorf("Len = %d after removing an unknown id, want 2", r.Len())
	}
}
