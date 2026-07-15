package hydra

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/norlis/hydra/pkg/env"
)

// Config holds the Hydra node configuration loaded from environment variables.
type Config struct {
	Environment string `env:"ENVIRONMENT"  envDefault:"local"` // aws | local
	BasePort    int    `env:"BASE_PORT"    envDefault:"3128"`
	ControlPort string `env:"CONTROL_PORT" envDefault:"9192"`
	LogLevel    string `env:"LOG_LEVEL"    envDefault:"info"` // debug | info | warn | error

	// DrainDelay is how long the /ready probe reports draining (503)
	// before the control-plane server begins its shutdown, giving a
	// load balancer time to stop routing new requests to this node.
	DrainDelay time.Duration `env:"HYDRA_DRAIN_DELAY" envDefault:"5s"`

	// Gossip plane (memberlist).
	GossIPNodeName       string        `env:"HYDRA_NODE_NAME"              envDefault:"node"`
	GossIPBindAddr       string        `env:"HYDRA_GOSSIP_ADDR"            envDefault:"0.0.0.0"`
	GossIPBindPort       int           `env:"HYDRA_GOSSIP_PORT"            envDefault:"7946"`
	GossIPSeeds          []string      `env:"HYDRA_GOSSIP_SEEDS"           envSeparator:","`
	GossipRejoinInterval time.Duration `env:"HYDRA_GOSSIP_REJOIN_INTERVAL" envDefault:"5s"`
	// Hex-encoded AES key (16, 24 or 32 bytes after decoding → AES-128/192/256).
	// Empty disables gossip encryption; peers without the same key cannot
	// join. Generate with: openssl rand -hex 16
	GossIPSecretKey string `env:"HYDRA_GOSSIP_SECRET"`

	// Cluster tag used by discovery providers (e.g. mDNS) to filter
	// peers that belong to the same logical mesh.
	ClusterTag string `env:"HYDRA_CLUSTER_TAG" envDefault:"hydra"`

	// Extra request headers to remove before forwarding to the upstream
	// (internet) service. Control-plane headers (X-Hydra-Hop, X-Entity-ID)
	// and RFC 7230 hop-by-hop headers are always stripped regardless.
	ProxyStripHeaders []string `env:"HYDRA_STRIP_HEADERS" envSeparator:","`

	// Allowed destination ports for CONNECT. Any other port is rejected
	// with 403 before dialing. Default 443,80 — opening more is a
	// conscious choice (SMTP/SSH/RDP turn the proxy into a relay).
	ProxyAllowedPorts []int `env:"HYDRA_PROXY_ALLOWED_PORTS" envDefault:"443,80" envSeparator:","`

	// Extra CIDR ranges to deny in addition to the built-in list
	// (RFC1918, loopback, link-local, IETF reserved, IPv6 embeddings).
	// Comma-separated. Example: "203.0.113.0/24,2001:db8::/32".
	ProxyDenyCIDR []string `env:"HYDRA_PROXY_DENY_CIDR" envSeparator:","`

	// Per-deployment allow CIDRs that override the built-in deny list.
	// Use to permit a specific private range (e.g. partner VPC accessible
	// via VPC peering). Empty by default.
	ProxyAllowCIDR []string `env:"HYDRA_PROXY_ALLOW_CIDR" envSeparator:","`

	// Maximum concurrent CONNECT tunnels per node. Zero (default) means
	// unlimited; turn it on once you have a credible upper bound from
	// production load. Going over the cap returns 503 before the dial.
	ProxyMaxTunnels int `env:"HYDRA_PROXY_MAX_TUNNELS" envDefault:"0"`

	// Per-tunnel idle timeout. After this much time without bytes flowing
	// in either direction the tunnel is force-closed. Zero (default)
	// disables the watchdog. Pick a value larger than the longest legit
	// idle period (e.g. WebSocket keepalives).
	ProxyIdleTimeout time.Duration `env:"HYDRA_PROXY_IDLE_TIMEOUT" envDefault:"0s"`

	// Auth mode applied to inbound proxy requests:
	//
	//	none  -> no auth check (default; safe only on a trusted LAN)
	//	basic -> RFC 7617 Proxy-Authorization with HYDRA_PROXY_AUTH_USER/PASS
	//
	// Peer hops (X-Hydra-Hop set) are always exempt — peers are trusted
	// because the inter-node link is expected to live on a private
	// interface.
	ProxyAuthMode string `env:"HYDRA_PROXY_AUTH_MODE" envDefault:"none"`
	ProxyAuthUser string `env:"HYDRA_PROXY_AUTH_USER"`
	ProxyAuthPass string `env:"HYDRA_PROXY_AUTH_PASS"`

	// AWS Cloud Map — only used when Environment == "aws".
	CloudMapRegion    string `env:"HYDRA_CLOUDMAP_REGION"    envDefault:"us-east-1"`
	CloudMapNamespace string `env:"HYDRA_CLOUDMAP_NAMESPACE"`
	CloudMapService   string `env:"HYDRA_CLOUDMAP_SERVICE"   envDefault:"hydra"`
}

// NewConfigFromEnv loads configuration from environment variables and
// validates fields that need interpretation (hex secrets, enumerations).
// Returns a descriptive error for any malformed value so fx aborts at
// startup instead of at first use.
func NewConfigFromEnv(logger *slog.Logger) (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	// Validate the gossip secret eagerly. If it's unset, surface that as
	// a warning here so operators see it at startup; memberlist only
	// needs the decoded bytes later via GossipSecret().
	key, err := cfg.GossipSecret()
	if err != nil {
		return nil, err
	}
	if key == nil {
		logger.Warn("gossip encryption disabled (HYDRA_GOSSIP_SECRET not set)")
	}
	// Validate ProxyAuthMode now so a typo doesn't silently degrade to
	// "none" at first request.
	switch cfg.ProxyAuthMode {
	case "", "none":
		cfg.ProxyAuthMode = "none"
	case "basic":
		if cfg.ProxyAuthUser == "" || cfg.ProxyAuthPass == "" {
			return nil, errors.New("HYDRA_PROXY_AUTH_MODE=basic requires HYDRA_PROXY_AUTH_USER and HYDRA_PROXY_AUTH_PASS")
		}
	default:
		return nil, fmt.Errorf("HYDRA_PROXY_AUTH_MODE must be one of: none, basic (got %q)", cfg.ProxyAuthMode)
	}
	return cfg, nil
}

// GossipSecret decodes HYDRA_GOSSIP_SECRET and checks its length. Returns
// (nil, nil) when unset (gossip encryption disabled). The key must be
// hex-encoded and decode to 16, 24 or 32 bytes for AES-128/192/256.
func (c *Config) GossipSecret() ([]byte, error) {
	if c.GossIPSecretKey == "" {
		return nil, nil
	}
	key, err := hex.DecodeString(c.GossIPSecretKey)
	if err != nil {
		return nil, fmt.Errorf("HYDRA_GOSSIP_SECRET must be hex-encoded: %w", err)
	}
	switch len(key) {
	case 16, 24, 32:
		return key, nil
	default:
		return nil, fmt.Errorf("HYDRA_GOSSIP_SECRET decoded length must be 16/24/32 bytes (got %d)", len(key))
	}
}
