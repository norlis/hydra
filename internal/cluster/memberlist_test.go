package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/topology"
	"github.com/norlis/hydra/internal/version"
)

type fakeProvider struct {
	ifaces []topology.NetworkInterface
	err    error
}

func (f fakeProvider) Discover() ([]topology.NetworkInterface, error) {
	return f.ifaces, f.err
}

func newTestDiscovery(p fakeProvider) *MemberlistDiscovery {
	return &MemberlistDiscovery{
		cfg:         &hydra.Config{GossIPNodeName: "node-1"},
		netProvider: p,
		log:         slog.New(slog.DiscardHandler),
	}
}

func TestGetLocalNode_IncludesVersion(t *testing.T) {
	t.Parallel()
	m := newTestDiscovery(fakeProvider{
		ifaces: []topology.NetworkInterface{{Name: "eth0", PrivateIP: "10.0.0.1"}},
	})
	if got := m.GetLocalNode().Version; got == "" || got != version.GitHash {
		t.Errorf("Version = %q, want %q (non-empty)", got, version.GitHash)
	}
}

func TestGetLocalNode_VersionOnDiscoveryError(t *testing.T) {
	t.Parallel()
	m := newTestDiscovery(fakeProvider{err: errors.New("boom")})
	if got := m.GetLocalNode().Version; got != version.GitHash {
		t.Errorf("fallback Version = %q, want %q", got, version.GitHash)
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
