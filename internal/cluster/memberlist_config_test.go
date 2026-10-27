package cluster

import (
	"testing"
	"time"

	hydra "github.com/norlis/hydra/internal"
)

// Failure detection must tolerate a busy Wi-Fi LAN: loopback timings evicted
// live nodes from the ring under load and moved their entities elsewhere.
func TestGossipConfig_UsesLANFailureDetectionInEveryEnvironment(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"local", "LOCAL", "aws"} {
		t.Run(env, func(t *testing.T) {
			t.Parallel()
			c := gossipConfig(&hydra.Config{Environment: env}, "10.0.0.1")
			checks := []struct {
				name      string
				got, want any
			}{
				{"ProbeTimeout", c.ProbeTimeout, 500 * time.Millisecond},
				{"ProbeInterval", c.ProbeInterval, time.Second},
				{"IndirectChecks", c.IndirectChecks, 3},
				{"SuspicionMult", c.SuspicionMult, 4},
				{"TCPTimeout", c.TCPTimeout, 10 * time.Second},
			}
			for _, ck := range checks {
				if ck.got != ck.want {
					t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
				}
			}
		})
	}
}

func TestGossipConfig_AppliesNodeIdentityAndAddresses(t *testing.T) {
	t.Parallel()
	cfg := &hydra.Config{
		Environment:    "local",
		GossIPNodeName: "zero1",
		GossIPBindAddr: "0.0.0.0",
		GossIPBindPort: 7946,
	}
	c := gossipConfig(cfg, "192.168.100.158")
	if c.Name != "zero1" || c.BindAddr != "0.0.0.0" || c.BindPort != 7946 || c.AdvertiseAddr != "192.168.100.158" {
		t.Errorf("config = name %q bind %s:%d advertise %q", c.Name, c.BindAddr, c.BindPort, c.AdvertiseAddr)
	}
	if c.SecretKey != nil {
		t.Errorf("SecretKey = %x, want nil without HYDRA_GOSSIP_SECRET", c.SecretKey)
	}
}
