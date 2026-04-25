package hydra

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/norlis/hydra/pkg/env"
	"go.uber.org/zap"
)

// Config holds the Hydra node configuration loaded from environment variables.
type Config struct {
	Environment string `env:"ENVIRONMENT"  envDefault:"development"` // production | development | local
	Provider    string `env:"PROVIDER"     envDefault:"aws"`         // aws | local
	BasePort    int    `env:"BASE_PORT"    envDefault:"3128"`
	ControlPort string `env:"CONTROL_PORT" envDefault:"9090"`
	LogLevel    string `env:"LOG_LEVEL"    envDefault:"info"` // debug | info | warn | error

	// Gossip plane (memberlist).
	GossIPNodeName       string        `env:"HYDRA_NODE_NAME"              envDefault:"node"`
	GossIPBindAddr       string        `env:"HYDRA_GOSSIP_ADDR"            envDefault:"0.0.0.0"`
	GossIPBindPort       int           `env:"HYDRA_GOSSIP_PORT"            envDefault:"7946"`
	GossIPSeeds          []string      `env:"HYDRA_GOSSIP_SEEDS"           envSeparator:","`
	GossipRejoinInterval time.Duration `env:"HYDRA_GOSSIP_REJOIN_INTERVAL" envDefault:"15s"`
	// Hex-encoded AES key (16, 24 or 32 bytes after decoding → AES-128/192/256).
	// Empty disables gossip encryption; peers without the same key cannot
	// join. Generate with: openssl rand -hex 16
	GossIPSecretKey string `env:"HYDRA_GOSSIP_SECRET"`

	// Cluster tag used by discovery providers (e.g. mDNS) to filter
	// peers that belong to the same logical mesh.
	ClusterTag string `env:"HYDRA_CLUSTER_TAG" envDefault:"hydra"`

	// Extra request headers to remove before forwarding to the upstream
	// (internet) service. Control-plane headers (X-Hydra-Hop, X-Entity-ID)
	// are always stripped regardless of this list.
	ProxyStripHeaders []string `env:"HYDRA_STRIP_HEADERS" envSeparator:","`

	// AWS Cloud Map — only used when Environment == "aws".
	CloudMapRegion    string `env:"HYDRA_CLOUDMAP_REGION"    envDefault:"us-east-1"`
	CloudMapNamespace string `env:"HYDRA_CLOUDMAP_NAMESPACE"`
	CloudMapService   string `env:"HYDRA_CLOUDMAP_SERVICE"   envDefault:"hydra"`
}

// NewConfigFromEnv loads configuration from environment variables and
// validates fields that need interpretation (hex secrets, enumerations).
// Returns a descriptive error for any malformed value so fx aborts at
// startup instead of at first use.
func NewConfigFromEnv(logger *zap.Logger) (*Config, error) {
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
