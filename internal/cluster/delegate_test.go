package cluster

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/testlog"
	"github.com/norlis/hydra/internal/topology"
)

// awsNode builds a node with n fully populated ENI-like interfaces.
func awsNode(n int) topology.Node {
	now := time.Now().UTC()
	nd := topology.Node{ID: "hydra-prod-us-east-1a-01", LastSeen: now, StartedAt: &now, Healthy: true, Version: "v1.4.2"}
	for i := range n {
		nd.Interfaces = append(nd.Interfaces, topology.NetworkInterface{
			Name: fmt.Sprintf("ens%d", 5+i), MAC: fmt.Sprintf("0a:ff:c3:1b:%02x:%02x", i, 7*i),
			PrivateIP: fmt.Sprintf("10.42.%d.117", i), PublicIP: fmt.Sprintf("54.210.%d.%d", 100+i, 33+i),
			SubnetCIDR: fmt.Sprintf("10.42.%d.0/24", i), ServicePort: 3128 + i, PhysicalID: fmt.Sprintf("eni-0%015x", 123456789+i*777),
		})
	}
	return nd
}

func TestNodeMeta_FitsInMetaMaxSize(t *testing.T) {
	t.Parallel()
	d := &NodeDelegate{getLocalNode: func() topology.Node { return awsNode(6) }, log: slog.New(slog.DiscardHandler)}
	meta := d.NodeMeta(memberlist.MetaMaxSize)
	if len(meta) == 0 || len(meta) > memberlist.MetaMaxSize {
		t.Fatalf("NodeMeta size = %d, want 1..%d", len(meta), memberlist.MetaMaxSize)
	}
}

func recvEvent(t *testing.T, ch <-chan bus.ClusterEvent) bus.ClusterEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-t.Context().Done():
		t.Fatal("no event")
	}
	return bus.ClusterEvent{}
}

func TestNodeDelegate_Events(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(topology.Node{ID: "ignored", Interfaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1", ServicePort: 3128}}})
	if err != nil {
		t.Fatal(err)
	}
	member := &memberlist.Node{Name: "n1", Addr: net.IPv4(10, 0, 0, 1), Meta: encodeMeta(raw)}

	t.Run("NotifyJoin publishes NodeJoined with decoded meta and the memberlist name", func(t *testing.T) {
		t.Parallel()
		b := bus.NewEventBus(slog.New(slog.DiscardHandler))
		ch := b.Subscribe()
		d := &NodeDelegate{eventBus: b, log: slog.New(slog.DiscardHandler)}
		d.NotifyJoin(member)
		ev := recvEvent(t, ch)
		if ev.Type != bus.NodeJoined || ev.Node.ID != "n1" || len(ev.Node.Interfaces) != 1 || ev.Node.Interfaces[0].Name != "eth0" {
			t.Errorf("event = %+v", ev)
		}
	})

	t.Run("NotifyUpdate publishes NodeUpdated", func(t *testing.T) {
		t.Parallel()
		b := bus.NewEventBus(slog.New(slog.DiscardHandler))
		ch := b.Subscribe()
		d := &NodeDelegate{eventBus: b, log: slog.New(slog.DiscardHandler)}
		d.NotifyUpdate(member)
		if ev := recvEvent(t, ch); ev.Type != bus.NodeUpdated {
			t.Errorf("type = %v, want NodeUpdated", ev.Type)
		}
	})

	t.Run("NotifyLeave with unreadable meta publishes the default stub and warns once", func(t *testing.T) {
		t.Parallel()
		b := bus.NewEventBus(slog.New(slog.DiscardHandler))
		ch := b.Subscribe()
		rec := testlog.New()
		d := &NodeDelegate{eventBus: b, log: rec.Logger()}
		d.NotifyLeave(&memberlist.Node{Name: "n1", Addr: net.IPv4(10, 0, 0, 1), Meta: []byte("garbage")})
		ev := recvEvent(t, ch)
		if ev.Type != bus.NodeLeft || len(ev.Node.Interfaces) != 1 || ev.Node.Interfaces[0].Name != "default" || ev.Node.Interfaces[0].ServicePort != 0 {
			t.Errorf("event = %+v", ev)
		}
		warns := 0
		for _, e := range rec.Entries() {
			if e.Level == slog.LevelWarn {
				warns++
			}
		}
		if warns != 1 {
			t.Errorf("warn entries = %d, want 1", warns)
		}
	})

	t.Run("LocalState is empty and MergeRemoteState ignores its input", func(t *testing.T) {
		t.Parallel()
		rec := testlog.New()
		d := &NodeDelegate{getLocalNode: func() topology.Node { return awsNode(1) }, log: rec.Logger()}
		if got := d.LocalState(true); len(got) != 0 {
			t.Errorf("LocalState = %d bytes, want none (push/pull carries no node state)", len(got))
		}
		d.MergeRemoteState([]byte("garbage"), true)
		if n := len(rec.Entries()); n != 0 {
			t.Errorf("log entries = %d, want 0", n)
		}
	})
}
