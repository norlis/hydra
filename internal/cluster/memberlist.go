package cluster

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/hashicorp/memberlist"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
	hlogger "github.com/norlis/hydra/pkg/logger"
)

// MemberlistDiscovery implements the Discovery interface using HashiCorp
// memberlist as the gossip transport.
type MemberlistDiscovery struct {
	cfg          *hydra.Config
	list         *memberlist.Memberlist
	eventBus     bus.EventBus
	netProvider  network.Provider
	seedProvider SeedProvider
	log          *slog.Logger
	stopCh       chan struct{}
	startedAt    *time.Time
}

// NewMemberlistDiscovery builds the gossip engine with all its dependencies.
func NewMemberlistDiscovery(
	cfg *hydra.Config,
	eventBus bus.EventBus,
	netProvider network.Provider,
	seedProvider SeedProvider,
	log *slog.Logger,
) *MemberlistDiscovery {
	return &MemberlistDiscovery{
		cfg:          cfg,
		eventBus:     eventBus,
		netProvider:  netProvider,
		seedProvider: seedProvider,
		log:          log,
		stopCh:       make(chan struct{}),
		startedAt:    new(time.Now().UTC()),
	}
}

// Start initializes the protocol and joins the P2P network. A background
// auto-join loop keeps healing the membership if peers become reachable
// after this node started.
func (m *MemberlistDiscovery) Start() error {
	// Low-latency defaults tuned for LAN. DefaultWANConfig would be used
	// for cross-internet topologies.
	config := memberlist.DefaultLANConfig()

	if strings.EqualFold(m.cfg.Environment, "local") {
		config = memberlist.DefaultLocalConfig()
	}

	advertiseAddr, err := m.resolveAdvertiseAddr()
	if err != nil {
		// Fail fast if we can't determine the advertise IP.
		return err
	}

	// Validation + "disabled" warn already happened at config load;
	// a decode error here would mean someone mutated cfg afterwards.
	if key, _ := m.cfg.GossipSecret(); key != nil {
		config.SecretKey = key
	}

	config.Name = m.cfg.GossIPNodeName
	config.BindAddr = m.cfg.GossIPBindAddr
	config.AdvertiseAddr = advertiseAddr
	config.BindPort = m.cfg.GossIPBindPort

	delegate := &NodeDelegate{
		eventBus: m.eventBus,
		getLocalNode: func() topology.Node {
			return m.GetLocalNode()
		},
		log: m.log,
	}

	// memberlist uses the same field for both delegate interfaces.
	config.Delegate = delegate
	config.Events = delegate

	// Silence HashiCorp's internal logger; observability is handled by
	// our own application logger.
	config.Logger = log.New(io.Discard, "", 0)

	list, err := memberlist.Create(config)
	if err != nil {
		return fmt.Errorf("creating memberlist: %w", err)
	}
	m.list = list

	// First join attempt is best-effort. Peers may not be up yet; the
	// auto-join loop below keeps retrying so the cluster converges
	// regardless of startup ordering.
	if seeds := m.resolveSeeds(); len(seeds) > 0 {
		if _, err := m.list.Join(seeds); err != nil {
			m.log.Warn("initial join failed, will retry in background",
				slog.Any("seeds", seeds), hlogger.Err(err))
		}
	}

	go m.autoJoinLoop()

	return nil
}

// Stop shuts the gossip channel down safely.
func (m *MemberlistDiscovery) Stop() error {
	close(m.stopCh)

	if m.list == nil {
		return nil
	}
	// Announce graceful leave and give the cluster 5s to propagate it.
	if err := m.list.Leave(5 * time.Second); err != nil {
		m.log.Warn("graceful leave failed", hlogger.Err(err))
	}
	return m.list.Shutdown()
}

// autoJoinLoop periodically re-resolves seeds and calls Join, but only
// while this node is alone (NumMembers <= 1). Once at least one peer
// has been discovered, gossip itself maintains membership; running Join
// again would just be wasted mDNS queries / Cloud Map API calls.
//
// The loop wakes back up automatically if the membership ever drops to
// 1 (last peer left) and resumes seed discovery until a peer is found.
//
// Trade-off: a split-brain where two healthy clusters never see each
// other (both sides have NumMembers > 1 but pointed at disjoint peers)
// won't auto-heal. Mitigate with static HYDRA_GOSSIP_SEEDS shared by
// every node, or upgrade to an adaptive-backoff variant if needed.
func (m *MemberlistDiscovery) autoJoinLoop() {
	interval := m.cfg.GossipRejoinInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			if m.list.NumMembers() > 1 {
				// Already part of a cluster; let gossip handle it.
				continue
			}
			seeds := m.resolveSeeds()
			if len(seeds) == 0 {
				continue
			}
			if _, err := m.list.Join(seeds); err != nil {
				m.log.Debug("auto-join tick failed", hlogger.Err(err))
			}
		}
	}
}

// GetActiveNodes returns a snapshot of currently healthy peers. Each
// member's Meta payload (populated by NodeDelegate.NodeMeta) carries
// the full topology.Node the peer published, so we decode it instead
// of fabricating a stub from Addr alone.
func (m *MemberlistDiscovery) GetActiveNodes() []topology.Node {
	members := m.list.Members()
	nodes := make([]topology.Node, 0, len(members))
	for _, member := range members {
		nodes = append(nodes, decodeMember(member, m.log))
	}
	return nodes
}

// decodeMember prefers the gossiped Meta payload; falls back to a
// minimal stub only when Meta is empty or cannot be parsed.
func decodeMember(member *memberlist.Node, logger *slog.Logger) topology.Node {
	if len(member.Meta) > 0 {
		if raw, err := decodeMeta(member.Meta); err == nil {
			var node topology.Node
			if err := json.Unmarshal(raw, &node); err == nil {
				// memberlist owns the authoritative ID and reachable address,
				// so we normalize them regardless of what the peer published.
				node.ID = member.Name
				return node
			}
		}
		if logger != nil {
			logger.Warn("failed to decode peer NodeMeta, falling back to stub",
				slog.String("node_id", member.Name),
				slog.String("addr", member.Addr.String()))
		}
	}
	return topology.Node{
		ID: member.Name,
		Interfaces: []topology.NetworkInterface{
			{
				Name:      "default",
				PrivateIP: member.Addr.String(),
			},
		},
	}
}

// encodeMeta zlib-compresses a JSON payload so it fits in memberlist's
// 512-byte NodeMeta budget with room to spare.
func encodeMeta(raw []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(raw)
	_ = w.Close()
	return buf.Bytes()
}

// decodeMeta reverses encodeMeta. Returns an error if the payload is
// not a valid zlib stream.
func decodeMeta(meta []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(meta))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// GetLocalNode returns this instance's topology view.
func (m *MemberlistDiscovery) GetLocalNode() topology.Node {
	interfaces, err := m.netProvider.Discover()
	if err != nil {
		m.log.Error("failed to discover local interfaces", hlogger.Err(err))
		return topology.Node{ID: m.cfg.GossIPNodeName}
	}
	return topology.Node{
		ID:         m.cfg.GossIPNodeName,
		Interfaces: interfaces,
		Healthy:    true,
		LastSeen:   time.Now(),
		StartedAt:  m.startedAt,
	}
}

// Subscribe delegates subscription to the internal event bus.
func (m *MemberlistDiscovery) Subscribe() <-chan bus.ClusterEvent {
	return m.eventBus.Subscribe()
}

// resolveAdvertiseAddr picks the IP we announce to the cluster by
// autodiscovering the first valid interface through the network Provider.
func (m *MemberlistDiscovery) resolveAdvertiseAddr() (string, error) {
	ifaces, err := m.netProvider.Discover()
	if err != nil {
		return "", fmt.Errorf("discovering network for AdvertiseAddr: %w", err)
	}

	if len(ifaces) == 0 {
		return "", errors.New("no interfaces found to autodiscover AdvertiseAddr")
	}

	addr := ifaces[0].PrivateIP
	m.log.Info("AdvertiseAddr autodiscovered", slog.String("addr", addr))
	return addr, nil
}

// resolveSeeds merges static seeds from configuration with any
// dynamically discovered ones, deduplicating.
func (m *MemberlistDiscovery) resolveSeeds() []string {
	// map[string]struct{} as a zero-cost set.
	seedSet := make(map[string]struct{})

	for _, s := range m.cfg.GossIPSeeds {
		seedSet[s] = struct{}{}
	}

	if m.seedProvider != nil {
		discovered, err := m.seedProvider.Discover()
		if err != nil {
			m.log.Warn("dynamic seed discovery failed", hlogger.Err(err))
		} else if len(discovered) > 0 {
			for _, s := range discovered {
				seedSet[s] = struct{}{}
			}
			m.log.Debug("dynamic seeds added", slog.Any("seeds", discovered))
		}
	}

	finalSeeds := make([]string, 0, len(seedSet))
	for s := range seedSet {
		finalSeeds = append(finalSeeds, s)
	}
	return finalSeeds
}
