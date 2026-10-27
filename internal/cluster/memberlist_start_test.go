package cluster

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
)

var errSeedBoom = errors.New("seed boom")

// fakeSeeds is a SeedProvider with a scripted result, an optional delay and
// an optional hook fired on every Discover call.
type fakeSeeds struct {
	seeds      []string
	err        error
	delay      time.Duration
	onDiscover func()
}

func (f fakeSeeds) Discover() ([]string, error) {
	if f.onDiscover != nil {
		f.onDiscover()
	}
	if f.delay > 0 {
		time.Sleep(f.delay) // simulates a slow discovery backend (mDNS window, Cloud Map API)
	}
	return f.seeds, f.err
}

func loopbackCfg(name string, seeds ...string) *hydra.Config {
	return &hydra.Config{
		Environment:          "local",
		GossIPNodeName:       name,
		GossIPBindAddr:       "127.0.0.1",
		GossIPBindPort:       0,
		GossIPSeeds:          seeds,
		GossipRejoinInterval: time.Hour,
	}
}

func loopbackRegistry(t *testing.T) *network.Registry {
	t.Helper()
	reg := network.NewRegistry(fakeProvider{ifaces: []topology.NetworkInterface{{Name: "lo0", PrivateIP: "127.0.0.1", ServicePort: 3128}}}, slog.New(slog.DiscardHandler))
	t.Cleanup(reg.Stop)
	return reg
}

// startNode starts a discovery on loopback and stops it at test end.
func startNode(t *testing.T, cfg *hydra.Config, seeds SeedProvider) (*MemberlistDiscovery, bus.EventBus) {
	t.Helper()
	b := bus.NewEventBus(slog.New(slog.DiscardHandler))
	m := NewMemberlistDiscovery(cfg, b, loopbackRegistry(t), seeds, slog.New(slog.DiscardHandler))
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })
	return m, b
}

func TestMemberlistDiscovery_Start(t *testing.T) {
	t.Parallel()

	t.Run("starts without seeds and sees itself", func(t *testing.T) {
		t.Parallel()
		m, _ := startNode(t, loopbackCfg("solo"), NoopSeedProvider{})
		nodes := m.GetActiveNodes()
		if len(nodes) != 1 || nodes[0].ID != "solo" || nodes[0].Version == "" {
			t.Errorf("nodes = %+v", nodes)
		}
	})

	t.Run("an unreachable seed does not prevent startup", func(t *testing.T) {
		t.Parallel()
		m, _ := startNode(t, loopbackCfg("a", "127.0.0.1:1"), NoopSeedProvider{})
		if n := len(m.GetActiveNodes()); n != 1 {
			t.Errorf("active nodes = %d, want 1 (itself)", n)
		}
	})

	t.Run("a failing SeedProvider does not prevent startup", func(t *testing.T) {
		t.Parallel()
		asked := make(chan struct{}, 1)
		m, _ := startNode(t, loopbackCfg("b"), fakeSeeds{err: errSeedBoom, onDiscover: func() { asked <- struct{}{} }})
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		select {
		case <-asked:
		case <-ctx.Done():
			t.Fatal("the seed provider was never consulted")
		}
		if n := len(m.GetActiveNodes()); n != 1 {
			t.Errorf("active nodes = %d, want 1 (itself)", n)
		}
	})

	t.Run("Start returns before a slow SeedProvider answers and joins from the background loop", func(t *testing.T) {
		t.Parallel()
		const delay = 300 * time.Millisecond
		asked := make(chan struct{}, 1)
		seeds := fakeSeeds{delay: delay, onDiscover: func() { asked <- struct{}{} }}
		start := time.Now()
		startNode(t, loopbackCfg("c"), seeds)
		if elapsed := time.Since(start); elapsed >= delay {
			t.Errorf("Start took %v, want less than the %v seed delay", elapsed, delay)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		select {
		case <-asked:
		case <-ctx.Done():
			t.Fatal("the seed provider was never consulted after Start")
		}
	})

	t.Run("Stop is idempotent", func(t *testing.T) {
		t.Parallel()
		m, _ := startNode(t, loopbackCfg("d"), NoopSeedProvider{})
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
		if err := m.Stop(); err != nil {
			t.Errorf("second Stop = %v, want nil", err)
		}
	})

	t.Run("two loopback nodes discover each other and a leaving node is announced", func(t *testing.T) {
		t.Parallel()
		a, busA := startNode(t, loopbackCfg("a"), NoopSeedProvider{})
		events := busA.Subscribe()
		aPort := int(a.list.LocalNode().Port)
		bCfg := loopbackCfg("b", "127.0.0.1:"+itoa(aPort))
		bNode := NewMemberlistDiscovery(bCfg, bus.NewEventBus(slog.New(slog.DiscardHandler)), loopbackRegistry(t), NoopSeedProvider{}, slog.New(slog.DiscardHandler))
		if err := bNode.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = bNode.Stop() })

		ev := waitEvent(t, events, bus.NodeJoined)
		if ev.Node.ID != "b" || len(ev.Node.Interfaces) != 1 || ev.Node.Interfaces[0].ServicePort != 3128 {
			t.Errorf("joined event = %+v", ev)
		}
		if n := len(a.GetActiveNodes()); n != 2 {
			t.Errorf("a sees %d nodes, want 2", n)
		}

		if err := bNode.Stop(); err != nil {
			t.Fatal(err)
		}
		if ev := waitEvent(t, events, bus.NodeLeft); ev.Node.ID != "b" {
			t.Errorf("left event = %+v", ev)
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }

// gossipTimeout bounds how long a test waits for gossip to propagate. Local
// propagation takes well under a second; the margin covers a CPU-saturated
// machine running the whole suite with -race and -cover.
const gossipTimeout = 20 * time.Second

// waitEvent reads events until one of the wanted type arrives or the gossip
// timeout passes.
func waitEvent(t *testing.T, ch <-chan bus.ClusterEvent, want bus.EventType) bus.ClusterEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), gossipTimeout)
	defer cancel()
	for {
		select {
		case ev := <-ch:
			if ev.Type == want {
				return ev
			}
		case <-ctx.Done():
			t.Fatalf("no %v event within %v", want, gossipTimeout)
		}
	}
}
