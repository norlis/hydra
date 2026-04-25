package proxy

import "go.uber.org/fx"

// Module wires the proxy layer. It stands up one HTTP server per
// interface discovered by network.Registry; the per-interface Router
// and Forwarder pairs are built inside StartProxyServers since they
// are multi-instance (one per NIC) rather than singletons.
var Module = fx.Module("proxy",
	fx.Invoke(StartProxyServers),
)