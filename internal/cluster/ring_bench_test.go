package cluster_test

import (
	"fmt"
	"log/slog"
	"runtime"
	"testing"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
)

// populatedRing publishes nodes×nics interfaces and waits until the ring
// has consumed them all (observed through Len), without synctest.
func populatedRing(b *testing.B, nodes, nics int) *cluster.Ring {
	b.Helper()
	eb := bus.NewEventBus(slog.New(slog.DiscardHandler))
	r := cluster.NewRing(&hydra.Config{GossIPNodeName: "self"}, eb, slog.New(slog.DiscardHandler))
	b.Cleanup(r.Stop)
	for n := range nodes {
		ifaces := make([]topology.NetworkInterface, 0, nics)
		for i := range nics {
			ifaces = append(ifaces, topology.NetworkInterface{Name: fmt.Sprintf("eth%d", i), PrivateIP: fmt.Sprintf("10.0.%d.%d", n, i), ServicePort: 3128 + i})
		}
		eb.Publish(bus.ClusterEvent{Type: bus.NodeJoined, Node: topology.Node{ID: fmt.Sprintf("n%d", n), Interfaces: ifaces}})
		for r.Len() < (n+1)*nics { // bus buffer is 10: wait for each node before the next
			runtime.Gosched()
		}
	}
	return r
}

func BenchmarkRingEndpointFor(b *testing.B) {
	entities := make([]string, 1000)
	for i := range entities {
		entities[i] = fmt.Sprintf("tenant-%d", i)
	}
	for _, tc := range []struct {
		name        string
		nodes, nics int
	}{{"1 nodo × 1 NIC", 1, 1}, {"10 nodos × 2 NICs", 10, 2}} {
		b.Run(tc.name, func(b *testing.B) {
			r := populatedRing(b, tc.nodes, tc.nics)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if r.EndpointFor(entities[i%len(entities)]) == nil {
					b.Fatal("nil endpoint")
				}
				i++
			}
		})
	}
}
