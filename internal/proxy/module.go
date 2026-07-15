package proxy

import (
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/proxy/conntrack"
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
var Module = fx.Module("proxy",
	fx.Provide(conntrack.NewTracker),
	fx.Provide(provideMetrics),
	fx.Provide(provideTunnelLimiter),
	fx.Invoke(StartProxyServers),
)

// provideMetrics builds the proxy data-plane metrics on top of the
// OTel MeterProvider that httpapi.NewMeterProvider configured with
// the OTLP/HTTP exporter.
func provideMetrics(mp metric.MeterProvider) (*metrics.Metrics, error) {
	return metrics.New(mp)
}

// provideTunnelLimiter builds the global tunnel limiter from
// cfg.ProxyMaxTunnels. Zero (default) disables the cap.
func provideTunnelLimiter(cfg *hydra.Config) *limiter.Tunnel {
	return limiter.New(cfg.ProxyMaxTunnels)
}
