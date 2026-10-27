package proxy

import (
	"fmt"
	"net/netip"

	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/proxy/conntrack"
	"github.com/norlis/hydra/internal/proxy/ipcheck"
	"github.com/norlis/hydra/internal/proxy/limiter"
	"github.com/norlis/hydra/internal/proxy/metrics"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/fx"
)

// Module wires the proxy layer. It stands up one HTTP server per
// interface discovered by network.Registry; the per-interface Router
// and Forwarder pairs are built inside StartProxyServers since they
// are multi-instance (one per NIC) rather than singletons.
//
// Tracker, Metrics and the tunnel Limiter live here as singletons so
// every per-NIC forwarder shares the same gauges/counters and the
// shutdown hook can drain every active tunnel from a single place.
// The ipcheck Classifier is a singleton too, and is also consumed by
// the control plane so both planes enforce one egress policy.
var Module = fx.Module("proxy",
	fx.Provide(provideClassifier),
	fx.Provide(conntrack.NewTracker),
	fx.Provide(provideMetrics),
	fx.Provide(provideTunnelLimiter),
	fx.Invoke(StartProxyServers),
)

// provideMetrics builds the proxy data-plane metrics on top of the
// OTel MeterProvider that httpapi.NewMeterProvider configured with
// the OTLP/HTTP exporter.
func provideMetrics(mp metric.MeterProvider) (*metrics.Metrics, error) {
	m, err := metrics.New(mp)
	if err != nil {
		return nil, fmt.Errorf("proxy metrics: %w", err)
	}
	return m, nil
}

// provideTunnelLimiter builds the global tunnel limiter from
// cfg.ProxyMaxTunnels. Zero (default) disables the cap.
func provideTunnelLimiter(cfg *hydra.Config) *limiter.Tunnel {
	return limiter.New(cfg.ProxyMaxTunnels)
}

// provideClassifier builds the egress policy from the configured CIDRs
// plus this node's own IPs, so a request that resolves back to us is
// denied as a self-connection.
func provideClassifier(cfg *hydra.Config, reg *network.Registry) (*ipcheck.Classifier, error) {
	ifaces := reg.All()
	localIPs := make([]netip.Addr, 0, len(ifaces))
	for _, iface := range ifaces {
		if ip, err := netip.ParseAddr(iface.PrivateIP); err == nil {
			localIPs = append(localIPs, ip)
		}
	}

	classifier, err := ipcheck.New(cfg.ProxyDenyCIDR, cfg.ProxyAllowCIDR, localIPs)
	if err != nil {
		return nil, fmt.Errorf("proxy: building IP classifier: %w", err)
	}
	return classifier, nil
}
