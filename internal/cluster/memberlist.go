package cluster

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/internal/version"
	hlogger "github.com/norlis/hydra/pkg/logger"
)

// MemberlistDiscovery implements the Discovery interface using HashiCorp
// memberlist as the gossip transport.
type MemberlistDiscovery struct {
	cfg          *hydra.Config
	list         *memberlist.Memberlist
	eventBus     bus.EventBus
	registry     *network.Registry
	seedProvider SeedProvider
	log          *slog.Logger
	stopCh       chan struct{}
	stopOnce     sync.Once
	startedAt    *time.Time
}

// NewMemberlistDiscovery builds the gossip engine with all its dependencies.
func NewMemberlistDiscovery(
	cfg *hydra.Config,
	eventBus bus.EventBus,
	registry *network.Registry,
	seedProvider SeedProvider,
	log *slog.Logger,
) *MemberlistDiscovery {
	return &MemberlistDiscovery{
		cfg:          cfg,
		eventBus:     eventBus,
		registry:     registry,
		seedProvider: seedProvider,
		log:          log,
		stopCh:       make(chan struct{}),
		startedAt:    new(time.Now().UTC()),
	}
}

// Start initializes the gossip protocol and returns without touching the
// network beyond binding the gossip port. Seed resolution and the join
// itself run in autoJoinLoop so a slow discovery backend or unreachable
// peers can never block application startup.
func (m *MemberlistDiscovery) Start() error {
	advertiseAddr, err := m.resolveAdvertiseAddr()
	if err != nil {
		// Fail fast if we can't determine the advertise IP.
		return err
	}
	config := gossipConfig(m.cfg, advertiseAddr)

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
	config.Logger = stdlog.New(io.Discard, "", 0)

	list, err := memberlist.Create(config)
	if err != nil {
		return fmt.Errorf("creating memberlist: %w", err)
	}
	m.list = list

	go m.autoJoinLoop()
	go m.metaRefreshLoop()

	return nil
}

// Stop shuts the gossip channel down safely. Calling it more than once is
// a no-op.
func (m *MemberlistDiscovery) Stop() error {
	var err error
	m.stopOnce.Do(func() {
		close(m.stopCh)
		if m.list == nil {
			return
		}
		// Announce graceful leave and give the cluster 5s to propagate it.
		if leaveErr := m.list.Leave(5 * time.Second); leaveErr != nil {
			m.log.Warn("graceful leave failed", hlogger.Err(leaveErr))
		}
		if shutdownErr := m.list.Shutdown(); shutdownErr != nil {
			err = fmt.Errorf("memberlist shutdown: %w", shutdownErr)
		}
	})
	return err
}

// gossipConfig builds the memberlist settings for this node.
func gossipConfig(cfg *hydra.Config, advertiseAddr string) *memberlist.Config {
	// LAN timings everywhere: memberlist's local profile is meant for
	// loopback and evicts live nodes on a busy Wi-Fi link.
	config := memberlist.DefaultLANConfig()

	// Validation + "disabled" warn already happened at config load;
	// a decode error here would mean someone mutated cfg afterwards.
	if key, _ := cfg.GossipSecret(); key != nil {
		config.SecretKey = key
	}

	config.Name = cfg.GossIPNodeName
	config.BindAddr = cfg.GossIPBindAddr
	config.AdvertiseAddr = advertiseAddr
	config.BindPort = cfg.GossIPBindPort
	return config
}

// autoJoinLoop resolves seeds and calls Join right away and then on every
// tick, but only while this node is alone (NumMembers <= 1). Once at least
// one peer has been discovered, gossip itself maintains membership;
// running Join again would just be wasted mDNS queries / Cloud Map calls.
//
// The loop wakes back up automatically if the membership ever drops to
// 1 (last peer left) and resumes seed discovery until a peer is found.
// The first failure is logged at Warn so operators see it; later retries
// at Debug to keep the log quiet while peers are still coming up.
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

	first := true
	for {
		if m.list.NumMembers() <= 1 {
			m.tryJoin(first)
			first = false
		}
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// tryJoin resolves seeds and attempts one Join. A failure is logged at
// Warn on the first attempt and at Debug afterwards.
func (m *MemberlistDiscovery) tryJoin(first bool) {
	seeds := m.resolveSeeds()
	if len(seeds) == 0 {
		return
	}
	if _, err := m.list.Join(seeds); err != nil {
		level := slog.LevelDebug
		if first {
			level = slog.LevelWarn
		}
		m.log.Log(context.Background(), level, "join attempt failed, will retry",
			slog.Any("seeds", seeds), hlogger.Err(err))
	}
}

const metaRefreshInterval = 60 * time.Second

// metaRefreshLoop periodically re-broadcasts this node's gossip metadata so
// runtime interface changes (EIP rotation, ENI re-attach) reach peers and
// /api/nodes without a restart. The registry cache is refreshed first so
// UpdateNode (which re-invokes NodeMeta) publishes a live view; every
// other reader of the local node uses the cache. Unconditional refresh
// doubles as anti-entropy: peers converge even if an update was lost.
func (m *MemberlistDiscovery) metaRefreshLoop() {
	ticker := time.NewTicker(metaRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.registry.Refresh()
			if err := m.list.UpdateNode(5 * time.Second); err != nil {
				m.log.Warn("failed to re-broadcast node metadata", hlogger.Err(err))
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
	if node, ok := decodePeerMeta(member); ok {
		return node
	}
	if len(member.Meta) > 0 && logger != nil {
		logger.Warn("failed to decode peer NodeMeta, falling back to stub",
			slog.String("node_id", member.Name),
			slog.String("addr", member.Addr.String()))
	}
	return topology.Node{
		ID:   member.Name,
		Addr: member.Addr.String(),
		Interfaces: []topology.NetworkInterface{
			{
				Name:      "default",
				PrivateIP: member.Addr.String(),
			},
		},
	}
}

// decodePeerMeta decodes the gossiped Meta payload into a Node, or ok=false
// when Meta is absent or malformed. memberlist owns the authoritative ID.
func decodePeerMeta(member *memberlist.Node) (topology.Node, bool) {
	if len(member.Meta) == 0 {
		return topology.Node{}, false
	}
	raw, err := decodeMeta(member.Meta)
	if err != nil {
		return topology.Node{}, false
	}
	var node topology.Node
	if err := json.Unmarshal(raw, &node); err != nil {
		return topology.Node{}, false
	}
	node.ID = member.Name
	node.Addr = member.Addr.String()
	return node, true
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

// decodeMetaMaxBytes caps the decompressed size of gossiped metadata.
// Legitimate payloads are a few KB of JSON; the cap stops zlib bombs
// from a malicious cluster member (~1000:1 expansion). A truncated
// payload fails JSON decoding downstream and falls back to a stub.
const decodeMetaMaxBytes = 1 << 20 // 1 MiB

// decodeMeta reverses encodeMeta. Returns an error if the payload is
// not a valid zlib stream.
func decodeMeta(meta []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(meta))
	if err != nil {
		return nil, fmt.Errorf("decode meta: %w", err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, decodeMetaMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	return data, nil
}

// GetLocalNode returns this instance's topology view from the registry
// cache. It never touches the network: health probes, /api/nodes and
// NodeMeta all call it, and discovery failures are already logged by the
// registry's refresh loop.
func (m *MemberlistDiscovery) GetLocalNode() topology.Node {
	var addr string
	if primary, ok := m.registry.Primary(); ok {
		addr = primary.PrivateIP
	}
	return topology.Node{
		ID:         m.cfg.GossIPNodeName,
		Addr:       addr,
		Interfaces: m.registry.All(),
		Healthy:    true,
		LastSeen:   time.Now(),
		StartedAt:  m.startedAt,
		Version:    version.GitHash,
	}
}

// resolveAdvertiseAddr picks the IP we announce to the cluster: the
// registry's primary interface.
func (m *MemberlistDiscovery) resolveAdvertiseAddr() (string, error) {
	primary, ok := m.registry.Primary()
	if !ok {
		return "", errors.New("no interfaces found to autodiscover AdvertiseAddr")
	}
	m.log.Info("AdvertiseAddr autodiscovered", slog.String("addr", primary.PrivateIP))
	return primary.PrivateIP, nil
}

// resolveSeeds merges static seeds from configuration with any
// dynamically discovered ones, deduplicating.
func (m *MemberlistDiscovery) resolveSeeds() []string {
	var discovered []string
	if m.seedProvider != nil {
		found, err := m.seedProvider.Discover()
		if err != nil {
			m.log.Warn("dynamic seed discovery failed", hlogger.Err(err))
		} else if len(found) > 0 {
			discovered = found
			m.log.Debug("dynamic seeds added", slog.Any("seeds", discovered))
		}
	}
	return mergeSeeds(m.cfg.GossIPSeeds, discovered)
}

// mergeSeeds returns the union of both lists without duplicates. Order is
// not guaranteed; memberlist tries seeds in the order given, so callers
// must not depend on it.
func mergeSeeds(static, discovered []string) []string {
	seedSet := make(map[string]struct{}, len(static)+len(discovered))
	for _, s := range static {
		seedSet[s] = struct{}{}
	}
	for _, s := range discovered {
		seedSet[s] = struct{}{}
	}
	out := make([]string, 0, len(seedSet))
	for s := range seedSet {
		out = append(out, s)
	}
	return out
}
