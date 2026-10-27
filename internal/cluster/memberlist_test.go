package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/memberlist"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/internal/version"
)

// fakeProvider returns a fixed interface list (or error) and counts calls.
type fakeProvider struct {
	ifaces []topology.NetworkInterface
	err    error
	calls  *atomic.Int32
}

func (f fakeProvider) Discover() ([]topology.NetworkInterface, error) {
	if f.calls != nil {
		f.calls.Add(1)
	}
	return f.ifaces, f.err
}

// newTestDiscovery wires a discovery on top of a Registry fed by p; the
// registry's refresh loop is stopped at test end.
func newTestDiscovery(t *testing.T, p fakeProvider) *MemberlistDiscovery {
	t.Helper()
	reg := network.NewRegistry(p, slog.New(slog.DiscardHandler))
	t.Cleanup(reg.Stop)
	return &MemberlistDiscovery{
		cfg:      &hydra.Config{GossIPNodeName: "node-1"},
		registry: reg,
		log:      slog.New(slog.DiscardHandler),
	}
}

func TestGetLocalNode_ReadsTheRegistryCacheWithoutCallingTheProvider(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	m := newTestDiscovery(t, fakeProvider{ifaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1"}}, calls: &calls})
	for range 5 {
		if n := len(m.GetLocalNode().Interfaces); n != 1 {
			t.Fatalf("interfaces = %d, want 1", n)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (only the registry's initial discovery)", got)
	}
}

func TestGetLocalNode_IncludesVersion(t *testing.T) {
	t.Parallel()
	m := newTestDiscovery(t, fakeProvider{
		ifaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1"}},
	})
	if got := m.GetLocalNode().Version; got == "" || got != version.GitHash {
		t.Errorf("Version = %q, want %q (non-empty)", got, version.GitHash)
	}
}

func TestGetLocalNode_VersionOnDiscoveryError(t *testing.T) {
	t.Parallel()
	m := newTestDiscovery(t, fakeProvider{err: errors.New("boom")})
	node := m.GetLocalNode()
	if node.Version != version.GitHash || len(node.Interfaces) != 0 {
		t.Errorf("node = %+v, want version set and no interfaces", node)
	}
}

func TestNodeVersion_SurvivesGossipRoundtrip(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(topology.Node{ID: "n1", Version: "v1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMeta(encodeMeta(raw))
	if err != nil {
		t.Fatal(err)
	}
	var got topology.Node
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1.2.3" {
		t.Errorf("Version = %q, want v1.2.3", got.Version)
	}
}

func TestDecodeMeta_Roundtrip(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"id":"node-1","interfaces":[{"name":"ens5","private_ip":"172.18.5.239"}]}`)

	got, err := decodeMeta(encodeMeta(payload))
	if err != nil {
		t.Fatalf("decodeMeta: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("roundtrip mismatch: got %q", got)
	}
}

// A zlib bomb from a malicious cluster member must not expand unbounded:
// gossiped meta is a few KB of JSON, so decompression is capped.
func TestDecodeMeta_CapsDecompressedSize(t *testing.T) {
	t.Parallel()
	bomb := encodeMeta(make([]byte, 10<<20)) // 10 MiB of zeros → ~10 KiB compressed

	got, err := decodeMeta(bomb)
	if err != nil {
		t.Fatalf("decodeMeta: %v", err)
	}
	if len(got) > decodeMetaMaxBytes {
		t.Errorf("decompressed %d bytes, want at most %d", len(got), decodeMetaMaxBytes)
	}
}

func TestDecodeMember_UsesTheGossipAddressAsNodeAddress(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(topology.Node{ID: "ignored", Addr: "10.9.9.9", Interfaces: []topology.NetworkInterface{{Name: "en0", PrivateIP: "198.18.60.115", ServicePort: 3128}}})
	if err != nil {
		t.Fatal(err)
	}
	member := &memberlist.Node{Name: "n2", Addr: net.ParseIP("198.18.60.115"), Meta: encodeMeta(raw)}

	got := decodeMember(member, slog.New(slog.DiscardHandler))
	// memberlist observed the address it gossips with; a value the peer
	// put in its own metadata is not trusted for routing.
	if got.ID != "n2" || got.Addr != "198.18.60.115" {
		t.Errorf("node = id:%q addr:%q, want n2 at 198.18.60.115", got.ID, got.Addr)
	}

	stub := decodeMember(&memberlist.Node{Name: "n3", Addr: net.ParseIP("10.0.0.3")}, slog.New(slog.DiscardHandler))
	if stub.Addr != "10.0.0.3" {
		t.Errorf("stub addr = %q, want 10.0.0.3", stub.Addr)
	}
}
