package application

import (
	"testing"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/cluster"
)

// Any env without a discovery backend (standalone, single, or an
// unrecognized value) must resolve to NoopSeedProvider. lc and
// netProvider are unused on this path.
func TestNewSeedProvider_NoDiscoveryUsesNoop(t *testing.T) {
	for _, env := range []string{"standalone", "single", "STANDALONE", "does-not-exist"} {
		cfg := &hydra.Config{Environment: env}

		sp, err := NewSeedProvider(nil, cfg, nil)
		if err != nil {
			t.Fatalf("env=%q: unexpected error: %v", env, err)
		}
		if _, ok := sp.(cluster.NoopSeedProvider); !ok {
			t.Fatalf("env=%q: want cluster.NoopSeedProvider, got %T", env, sp)
		}
	}
}
