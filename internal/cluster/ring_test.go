package cluster_test

import (
	"log/slog"
	"strconv"
	"testing"
	"testing/synctest"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/topology"
)

const selfID = "self"

// newRing builds a ring inside the synctest bubble so Ring.run lives in it
// and synctest.Wait can observe when an event has been consumed.
func newRing(t *testing.T) (*cluster.Ring, bus.EventBus) {
	t.Helper()
	b := bus.NewEventBus(slog.New(slog.DiscardHandler))
	r := cluster.NewRing(&hydra.Config{GossIPNodeName: selfID}, b, slog.New(slog.DiscardHandler))
	t.Cleanup(r.Stop)
	return r, b
}

func iface(name, ip string, port int) topology.NetworkInterface {
	return topology.NetworkInterface{Name: name, PrivateIP: ip, ServicePort: port}
}

func publish(b bus.EventBus, et bus.EventType, id string, ifaces ...topology.NetworkInterface) {
	b.Publish(bus.ClusterEvent{Type: et, Node: topology.Node{ID: id, Interfaces: ifaces}})
	synctest.Wait()
}

func TestRing(t *testing.T) {
	t.Parallel()
	t.Run("a peer is relayed through its node address on each interface port", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			// utun32 carries an address only this host can reach (a VPN
			// tunnel); peers must use the node address with that NIC's port.
			b.Publish(bus.ClusterEvent{Type: bus.NodeJoined, Node: topology.Node{
				ID: "n2", Addr: "198.18.60.115",
				Interfaces: []topology.NetworkInterface{iface("utun32", "100.114.144.54", 3130)},
			}})
			synctest.Wait()
			ep := r.EndpointFor("tenant-1")
			if ep == nil || ep.ProxyAddr != "198.18.60.115:3130" {
				t.Errorf("endpoint = %+v, want ProxyAddr 198.18.60.115:3130", ep)
			}
		})
	})

	t.Run("two nodes sharing an interface IP keep distinct relay addresses", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			for _, n := range []topology.Node{
				{ID: "n1", Addr: "198.18.60.26", Interfaces: []topology.NetworkInterface{iface("bridge100", "192.168.139.3", 3129)}},
				{ID: "n2", Addr: "198.18.60.115", Interfaces: []topology.NetworkInterface{iface("bridge100", "192.168.139.3", 3129)}},
			} {
				b.Publish(bus.ClusterEvent{Type: bus.NodeJoined, Node: n})
			}
			synctest.Wait()
			want := map[string]string{"n1": "198.18.60.26:3129", "n2": "198.18.60.115:3129"}
			seen := map[string]bool{}
			for i := range 200 {
				ep := r.EndpointFor("entity-" + strconv.Itoa(i))
				node := cluster.LogicalNodeID(ep.NodeID)
				if ep.ProxyAddr != want[node] {
					t.Fatalf("%s resolves to %s, want %s", node, ep.ProxyAddr, want[node])
				}
				seen[node] = true
			}
			if len(seen) != 2 {
				t.Errorf("only %v owned entities, want both nodes", seen)
			}
		})
	})

	t.Run("a rejoin with fewer interfaces drops the ones no longer served", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			// A node restarted with fewer interfaces: peers may never see it
			// leave, only the new incarnation join.
			publish(b, bus.NodeJoined, "n2", iface("en0", "10.0.0.2", 3128), iface("bridge100", "10.0.9.9", 3129))
			publish(b, bus.NodeJoined, "n2", iface("en0", "10.0.0.2", 3128))
			if r.Len() != 1 {
				t.Fatalf("Len = %d, want 1 (stale interface kept)", r.Len())
			}
			for i := range 100 {
				if ep := r.EndpointFor("entity-" + strconv.Itoa(i)); ep.ProxyAddr != "10.0.0.2:3128" {
					t.Fatalf("resolves to %s, want only the served interface", ep.ProxyAddr)
				}
			}
		})
	})

	t.Run("an update without an interface drops it and keeps the other nodes", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "10.0.0.1", 3128))
			publish(b, bus.NodeJoined, "n2", iface("eth0", "10.0.0.2", 3128), iface("eth1", "10.0.1.2", 3129))
			publish(b, bus.NodeUpdated, "n2", iface("eth1", "10.0.1.2", 3129))
			if r.Len() != 2 {
				t.Errorf("Len = %d, want 2 (n1::eth0 and n2::eth1)", r.Len())
			}
		})
	})

	t.Run("an update with no servable interface keeps the node's entries", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n2", iface("en0", "10.0.0.2", 3128))
			// What decodeMember yields for metadata it cannot parse.
			publish(b, bus.NodeUpdated, "n2", iface("default", "10.0.0.2", 0))
			if r.Len() != 1 {
				t.Errorf("Len = %d, want 1 (a live peer must not vanish on bad metadata)", r.Len())
			}
		})
	})

	t.Run("NodeJoined registers one entry per interface", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "10.0.0.1", 3128), iface("eth1", "10.0.1.1", 3129))
			if r.Len() != 2 {
				t.Errorf("Len = %d, want 2", r.Len())
			}
		})
	})

	t.Run("ignores interfaces without IP or port", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "", 3128), iface("eth1", "10.0.1.1", 0))
			if r.Len() != 0 {
				t.Errorf("Len = %d, want 0", r.Len())
			}
		})
	})

	t.Run("the same entity always resolves to the same endpoint", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "10.0.0.1", 3128))
			publish(b, bus.NodeJoined, "n2", iface("eth0", "10.0.0.2", 3128))
			first := r.EndpointFor("tenant-42")
			if first == nil {
				t.Fatal("EndpointFor = nil")
			}
			for range 100 {
				if got := r.EndpointFor("tenant-42"); *got != *first {
					t.Fatalf("EndpointFor changed: %+v vs %+v", got, first)
				}
			}
		})
	})

	t.Run("NodeUpdated with a new address replaces the previous entry", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "10.0.0.1", 3128))
			publish(b, bus.NodeUpdated, "n1", iface("eth0", "10.0.0.9", 3128))
			if r.Len() != 1 {
				t.Errorf("Len = %d, want 1", r.Len())
			}
			if ep := r.EndpointFor("x"); ep == nil || ep.ProxyAddr != "10.0.0.9:3128" {
				t.Errorf("EndpointFor = %+v, want new address", ep)
			}
		})
	})

	t.Run("NodeLeft removes every interface of the node", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			ifaces := []topology.NetworkInterface{iface("eth0", "10.0.0.1", 3128), iface("eth1", "10.0.1.1", 3129)}
			publish(b, bus.NodeJoined, "n1", ifaces...)
			publish(b, bus.NodeLeft, "n1", ifaces...)
			if r.Len() != 0 || r.EndpointFor("x") != nil {
				t.Errorf("Len=%d EndpointFor=%+v, want empty", r.Len(), r.EndpointFor("x"))
			}
		})
	})

	t.Run("empty ring resolves to nil", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, _ := newRing(t)
			if r.EndpointFor("x") != nil || r.Len() != 0 {
				t.Error("empty ring resolved an endpoint")
			}
		})
	})

	t.Run("IsSelf is true for own interfaces and false for peers", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, selfID, iface("eth0", "10.0.0.1", 3128))
			ep := r.EndpointFor("x")
			if ep == nil {
				t.Fatal("EndpointFor = nil")
			}
			if ep.NodeID != selfID+"::eth0" || !ep.IsSelf {
				t.Errorf("endpoint = %+v, want own virtual id with IsSelf=true", ep)
			}
			publish(b, bus.NodeLeft, selfID, iface("eth0", "10.0.0.1", 3128))
			publish(b, bus.NodeJoined, "other", iface("eth0", "10.0.0.2", 3128))
			if ep := r.EndpointFor("x"); ep == nil || ep.IsSelf {
				t.Errorf("endpoint = %+v, want IsSelf=false for a peer", ep)
			}
		})
	})

	t.Run("NodeLeft removes the node even when the event interfaces differ from the registered ones", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			r, b := newRing(t)
			publish(b, bus.NodeJoined, "n1", iface("eth0", "10.0.0.1", 3128), iface("eth1", "10.0.1.1", 3129))
			publish(b, bus.NodeJoined, "n10", iface("eth0", "10.0.0.10", 3128))
			publish(b, bus.NodeLeft, "n1", topology.NetworkInterface{Name: "default", PrivateIP: "10.0.0.1"})
			if r.Len() != 1 {
				t.Errorf("Len = %d, want 1 (only n10 remains; n1 removed by node id, n10 untouched)", r.Len())
			}
			if ep := r.EndpointFor("x"); ep == nil || ep.NodeID != "n10::eth0" {
				t.Errorf("endpoint = %+v, want n10", ep)
			}
		})
	})
}
