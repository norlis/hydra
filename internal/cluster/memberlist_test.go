package cluster

import (
	"bytes"
	"testing"
)

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
