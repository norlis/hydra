package cluster

import (
	"context"
	"testing"

	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/topology"
)

// stubDiscovery implements Discovery with a fixed local node.
type stubDiscovery struct {
	local topology.Node
}

func (s *stubDiscovery) Start() error                       { return nil }
func (s *stubDiscovery) Stop() error                        { return nil }
func (s *stubDiscovery) GetActiveNodes() []topology.Node    { return nil }
func (s *stubDiscovery) GetLocalNode() topology.Node        { return s.local }
func (s *stubDiscovery) Subscribe() <-chan bus.ClusterEvent { return nil }

func TestCheckNilDiscovery(t *testing.T) {
	t.Parallel()
	c := &NodeReadinessChecker{}
	if err := c.Check(context.Background()); err == nil {
		t.Error("want error when discovery is not initialized")
	}
}

func TestCheckUnhealthyNode(t *testing.T) {
	t.Parallel()
	c := NewNodeReadinessChecker(&stubDiscovery{local: topology.Node{Healthy: false}})
	if err := c.Check(context.Background()); err == nil {
		t.Error("want error when local node reports unhealthy")
	}
}

func TestCheckNoInterfaces(t *testing.T) {
	t.Parallel()
	c := NewNodeReadinessChecker(&stubDiscovery{local: topology.Node{Healthy: true}})
	if err := c.Check(context.Background()); err == nil {
		t.Error("want error when no proxy interfaces are bound")
	}
}

func TestCheckReady(t *testing.T) {
	t.Parallel()
	c := NewNodeReadinessChecker(&stubDiscovery{local: topology.Node{
		Healthy:    true,
		Interfaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1"}},
	}})
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("want ready, got %v", err)
	}
}
