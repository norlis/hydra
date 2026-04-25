package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

var Module = fx.Module("httpapi",
	fx.Provide(NewHttpServerMux),
	fx.Provide(presenters.NewPresenters),
	fx.Provide(handlers.NewTopologyHandler),
	fx.Invoke(NewHttpApi),
)

func NewHttpServerMux(lc fx.Lifecycle, cfg *hydra.Config, logger *zap.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	listener := net.JoinHostPort("0.0.0.0", cfg.ControlPort)

	server := &http.Server{
		Addr:              listener,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			logger.Info(
				"ListenAndServe",
				zap.Any("addr", listener),
			)
			go func() {
				if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("Error al iniciar servidor HTTP: %v", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Deteniendo servidor HTTP...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("Error durante el apagado del servidor HTTP: %v", zap.Error(err))
				return fmt.Errorf("failed to shutdown HTTP server: %w", err)
			}
			logger.Info("Servidor HTTP detenido correctamente.")
			return nil
		},
	})

	return mux
}
