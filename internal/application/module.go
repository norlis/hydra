package application

import (
	"context"
	"strings"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/cluster/seed"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/network/aws"
	"github.com/norlis/hydra/internal/network/local"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Module holds the environment-aware provider selection. Keeping this
// wiring isolated prevents base packages (network, cluster) from
// importing their concrete implementations.
var Module = fx.Module("application",
	fx.Provide(NewNetworkProvider),
	fx.Provide(NewSeedProvider),
)

// NewNetworkProvider picks a network.Provider based on cfg.Environment.
//
//	local -> local.Provider  (discovers host interfaces)
//	aws   -> aws.IMDSProvider (queries EC2 instance metadata, IMDSv2)
//
// Cloud-specific providers (GCP, Azure, ...) should be added as new cases.
func NewNetworkProvider(cfg *hydra.Config, log *zap.Logger) network.Provider {
	switch strings.ToLower(cfg.Environment) {
	case "aws":
		return aws.NewIMDSProvider(cfg, log)
	default:
		// local + development + production (on-prem) all use host discovery.
		return local.NewProvider(cfg)
	}
}

// NewSeedProvider picks a cluster.SeedProvider based on cfg.Environment.
//
//	local -> seed.MDNSProvider (mDNS autodiscovery on the LAN)
//	aws   -> seed.CloudMap     (AWS Cloud Map service discovery)
//
// Any other environment gets NoopSeedProvider and relies only on the
// static seeds from HYDRA_GOSSIP_SEEDS.
func NewSeedProvider(
	lc fx.Lifecycle,
	cfg *hydra.Config,
	netProvider network.Provider,
) (cluster.SeedProvider, error) {
	switch strings.ToLower(cfg.Environment) {
	case "local":
		return seed.NewMDNSProvider(
			cfg.GossIPBindPort,
			cfg.GossIPNodeName,
			cfg.ClusterTag,
			resolveAdvertiseIP(netProvider),
		), nil

	case "aws":
		cm, err := seed.NewCloudMap(context.Background(), seed.CloudMapConfig{
			Region:      cfg.CloudMapRegion,
			Namespace:   cfg.CloudMapNamespace,
			Service:     cfg.CloudMapService,
			AdvertiseIP: resolveAdvertiseIP(netProvider),
			Port:        cfg.GossIPBindPort,
			InstanceID:  cfg.GossIPNodeName,
		})
		if err != nil {
			return nil, err
		}
		lc.Append(fx.Hook{
			OnStop: func(ctx context.Context) error { return cm.Shutdown(ctx) },
		})
		return cm, nil

	default:
		return cluster.NoopSeedProvider{}, nil
	}
}

// resolveAdvertiseIP returns the IP we'll advertise to peers. On any
// failure it returns an empty string; providers that depend on it
// (mDNS, Cloud Map) will fall back to their own defaults.
func resolveAdvertiseIP(netProvider network.Provider) string {
	ifaces, err := netProvider.Discover()
	if err != nil || len(ifaces) == 0 {
		return ""
	}
	return ifaces[0].PrivateIP
}
