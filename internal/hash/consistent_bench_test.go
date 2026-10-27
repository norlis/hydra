package hash_test

import (
	"fmt"
	"testing"

	"github.com/norlis/hydra/internal/hash"
)

func ringWith(n int) *hash.ConsistentHashRing {
	r := hash.NewConsistentHash(50)
	for i := range n {
		r.AddNode(fmt.Sprintf("node-%d", i), fmt.Sprintf("10.0.0.%d:3128", i))
	}
	return r
}

func BenchmarkHashRing(b *testing.B) {
	b.Run("AddNode con 20 nodos", func(b *testing.B) {
		r := ringWith(20)
		b.ReportAllocs()
		for b.Loop() {
			r.AddNode("extra", "10.0.1.1:3128")
			r.RemoveNode("extra")
		}
	})
	b.Run("GetTargetNode 20 nodos", func(b *testing.B) {
		r := ringWith(20)
		keys := make([]string, 1000)
		for i := range keys {
			keys[i] = fmt.Sprintf("tenant-%d", i)
		}
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			_, _, _ = r.GetTargetNode(keys[i%len(keys)])
			i++
		}
	})
}
