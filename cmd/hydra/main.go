package main

import (
	"log"
	"log/slog"
	"os"

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
			// Identity tags on every record: unified service tagging for
			// Datadog and per-version/env aggregation in CloudWatch. Sourced
			// without Config to preserve the Config↔Logger cycle break.
			return logger.New(os.Getenv("LOG_LEVEL")).With(
				slog.String("service", "hydra"),
				slog.String("version", version.GitHash),
				slog.String("env", os.Getenv("ENVIRONMENT")),
			)
		}),
		fx.WithLogger(func() fxevent.Logger {
			// fx lifecycle events are noisy at Info; cap them at the stricter
			// of LOG_LEVEL and Warn (mirrors the previous zap IncreaseLevel).
			lvl := max(logger.ParseLevel(os.Getenv("LOG_LEVEL")), slog.LevelWarn)
			return &fxevent.SlogLogger{Logger: logger.NewWithLevel(lvl)}
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
