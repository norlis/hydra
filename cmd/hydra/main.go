package main

import (
	"log"
	"log/slog"
	"os"

	"github.com/norlis/httpgate/logging"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/application"
	"github.com/norlis/hydra/internal/bus"
	"github.com/norlis/hydra/internal/cluster"
	"github.com/norlis/hydra/internal/httpapi"
	"github.com/norlis/hydra/internal/network"
	"github.com/norlis/hydra/internal/proxy"
	"github.com/norlis/hydra/internal/version"
	"github.com/norlis/hydra/pkg/logger"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
)

func main() {
	fxApp := fx.New(
		// Cross-cutting infrastructure. The logger reads LOG_LEVEL from
		// the environment directly so Config can depend on it (Config's
		// own LogLevel field is ignored here; we break what would be
		// a Config↔Logger cycle by keeping the logger free of Config).
		fx.Provide(func() *slog.Logger {
			// Platform-standard logger: OTel/ECS field names, ISO 8601 ms
			// timestamps and automatic trace-context injection. Sourced
			// without Config to preserve the Config↔Logger cycle break.
			return logging.New(os.Stderr,
				logging.WithService("hydra", version.GitHash),
				logging.WithEnvironment(os.Getenv("ENVIRONMENT")),
				logging.WithLevel(logger.ParseLevel(os.Getenv("LOG_LEVEL"))),
			)
		}),
		fx.WithLogger(func() fxevent.Logger {
			// fx lifecycle events are noisy at Info; cap them at the stricter
			// of LOG_LEVEL and Warn.
			lvl := max(logger.ParseLevel(os.Getenv("LOG_LEVEL")), slog.LevelWarn)
			return &fxevent.SlogLogger{Logger: logging.New(os.Stderr,
				logging.WithService("hydra", version.GitHash),
				logging.WithEnvironment(os.Getenv("ENVIRONMENT")),
				logging.WithLevel(lvl),
			)}
		}),

		// Configuration + internal event bus.
		fx.Provide(hydra.NewConfigFromEnv),
		fx.Provide(bus.NewEventBus),

		// Environment-aware provider selection (network + seeds).
		application.Module,

		// Domain modules.
		network.Module,
		cluster.Module,

		// HTTP control plane + data plane (forward proxy).
		httpapi.Module,
		proxy.Module,

		// Force graph construction so Discovery is built and hooked into
		// the lifecycle even without other consumers yet.
		fx.Invoke(func(cluster.Discovery) {}),
	)

	if err := fxApp.Err(); err != nil {
		log.Panicf("FX application failed to initialize: %v", err)
	}

	fxApp.Run()
}
