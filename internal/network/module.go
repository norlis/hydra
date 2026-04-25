package network

import (
	"context"

	"go.uber.org/fx"
)

// Module wires the base services of the network package. The concrete
// Provider selection (local, aws, …) lives in a higher-level package to
// avoid an import cycle with the implementations.
var Module = fx.Module("network",
	fx.Provide(NewRegistry),
	fx.Invoke(registerLifecycle),
)

// registerLifecycle stops the Registry's background discovery when the
// application shuts down cleanly.
func registerLifecycle(lc fx.Lifecycle, r *Registry) {
	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			r.Stop()
			return nil
		},
	})
}
