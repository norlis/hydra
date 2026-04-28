package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/norlis/httpgate/pkg/adapter/apidriven/presenters"
	hydra "github.com/norlis/hydra/internal"
	"github.com/norlis/hydra/internal/httpapi/handlers"
	"github.com/norlis/hydra/internal/version"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// proxyControlMaxHeaderBytes — even on the control plane we cap headers
// so a misconfigured client cannot exhaust memory reading them.
const proxyControlMaxHeaderBytes = 16 << 10

// Module wires the control-plane HTTP server. It also acts as the
// admin endpoint host so /healthz and /debug/pprof share the listener
// with the API (separate from the proxy data plane).
//
// Telemetry is push-based: the OTel MeterProvider exports OTLP/HTTP to
// an OpenTelemetry Collector at infrastructure level. There is no
// in-process /metrics endpoint — scraping vs. pushing is a deployment
// concern handled by the Collector.
var Module = fx.Module("httpapi",
	fx.Provide(NewMeterProvider),
	fx.Provide(NewHttpServerMux),
	fx.Provide(presenters.NewPresenters),
	fx.Provide(handlers.NewTopologyHandler),
	fx.Provide(handlers.NewWebHandler),
	fx.Invoke(MountAdminEndpoints),
	fx.Invoke(NewHttpApi),
)

// NewMeterProvider builds the OTel SDK MeterProvider that pushes
// metrics over OTLP/HTTP to an OpenTelemetry Collector. Endpoint and
// auth are configured via the standard OTel env vars (honored by the
// exporter and resource detector):
//
//	OTEL_EXPORTER_OTLP_ENDPOINT          (default http://localhost:4318)
//	OTEL_EXPORTER_OTLP_METRICS_ENDPOINT
//	OTEL_EXPORTER_OTLP_HEADERS
//	OTEL_METRIC_EXPORT_INTERVAL          (default 60s)
//	OTEL_RESOURCE_ATTRIBUTES
//	OTEL_SDK_DISABLED=true               turns the SDK into a no-op
//
// Bucket boundaries for the latency histograms are pinned via SDK
// Views so dashboards keep working across SDK upgrades. Without them
// the SDK would default to base-2 exponential buckets.
//
// The provider is installed as the global so libraries calling
// otel.Meter(...) directly (and future tracing work) find it without
// extra wiring. Shutdown flushes pending exports through the fx
// lifecycle hook.
func NewMeterProvider(lc fx.Lifecycle, cfg *hydra.Config, log *zap.Logger) (metric.MeterProvider, error) {
	ctx := context.Background()

	if strings.ToLower(os.Getenv("OTEL_SDK_DISABLED")) == "true" {
		log.Info("OpenTelemetry SDK disabled via OTEL_SDK_DISABLED=true")
		return otel.GetMeterProvider(), nil
	}

	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("httpapi: building OTLP metric exporter: %w", err)
	}

	// Resource identifies *this* node in the Collector. service.name
	// groups every Hydra instance; service.instance.id distinguishes
	// them in dashboards. WithFromEnv lets operators inject extra
	// attributes (env=prod, region=us-east-1, …) without code changes.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("hydra"),
			semconv.ServiceVersion(version.GitHash),
			semconv.ServiceInstanceID(cfg.GossIPNodeName),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithHost(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("httpapi: building OTel resource: %w", err)
	}

	// Buckets carried over verbatim from the previous Prometheus
	// definitions so existing alerts and dashboards keep firing on
	// the same boundaries when the Collector re-exposes the data.
	setupBuckets := []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}
	tunnelBuckets := []float64{.1, 1, 5, 30, 60, 300, 1800, 3600, 7200}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		sdkmetric.WithView(
			sdkmetric.NewView(
				sdkmetric.Instrument{Name: "hydra.proxy.connect_setup_seconds"},
				sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
					Boundaries: setupBuckets,
				}},
			),
			sdkmetric.NewView(
				sdkmetric.Instrument{Name: "hydra.proxy.tunnel_seconds"},
				sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
					Boundaries: tunnelBuckets,
				}},
			),
		),
	)

	otel.SetMeterProvider(provider)

	// Go runtime metrics (heap, GC, goroutines, …) via the contrib
	// package. Uses the global MeterProvider just installed, so it
	// must run after SetMeterProvider.
	if err := runtime.Start(); err != nil {
		log.Warn("runtime instrumentation failed to start", zap.Error(err))
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			if err := provider.Shutdown(ctx); err != nil {
				log.Warn("meter provider shutdown failed", zap.Error(err))
				return err
			}
			return nil
		},
	})

	return provider, nil
}

// MountAdminEndpoints registers /healthz and /debug/pprof/* on the
// control-plane mux. Metrics exposition is push-only (OTLP) and lives
// outside this binary; nothing serves /metrics here.
func MountAdminEndpoints(mux *http.ServeMux) {
	// pprof endpoints. Net/http/pprof registers on http.DefaultServeMux
	// when imported as a side-effect; we wire each handler explicitly
	// to keep the registration scoped to our mux.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

func NewHttpServerMux(lc fx.Lifecycle, cfg *hydra.Config, logger *zap.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	listener := net.JoinHostPort("0.0.0.0", cfg.ControlPort)

	server := &http.Server{
		Addr:              listener,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    proxyControlMaxHeaderBytes,
	}

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			logger.Info("control plane listening", zap.String("addr", listener))
			go func() {
				if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("control plane server error", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(_ context.Context) error {
			logger.Info("stopping control plane")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("control plane shutdown failed", zap.Error(err))
				return fmt.Errorf("failed to shutdown HTTP server: %w", err)
			}
			logger.Info("control plane stopped")
			return nil
		},
	})

	return mux
}
