package cluster

import (
	"context"

	"go.uber.org/fx"
)

// Module wires the cluster discovery layer. Exposes Discovery as the
// public interface and controls the gossip engine's Start/Stop lifecycle.
//
// The concrete SeedProvider selection is provided from a higher-level
// package to avoid import cycles with the implementations.
var Module = fx.Module("cluster",
	fx.Provide(NewMemberlistDiscovery),
	fx.Provide(func(m *MemberlistDiscovery) Discovery { return m }),
	fx.Provide(NewRing),
	fx.Provide(NewNodeReadinessChecker),
	fx.Invoke(registerLifecycle),
)

// registerLifecycle binds Discovery's Start/Stop to the fx lifecycle.
func registerLifecycle(lc fx.Lifecycle, d Discovery) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error { return d.Start() },
		OnStop:  func(_ context.Context) error { return d.Stop() },
	})
}
