package metrics_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/norlis/hydra/internal/proxy/metrics"
	"go.opentelemetry.io/otel/metric/noop"
)

// BenchmarkRecord compares the per-request attribute allocation of
// RecordRequest (builds an attribute set on every call) against
// RecordDenied (pre-built attributes).
func BenchmarkRecord(b *testing.B) {
	m, err := metrics.New(noop.NewMeterProvider())
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.Run("RecordRequest", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m.RecordRequest(ctx, http.MethodGet, "2xx", "local")
		}
	})
	b.Run("RecordDenied", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m.RecordDenied(ctx, "port")
		}
	})
	b.Run("RecordTunnelStarted", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m.RecordTunnelStarted(ctx, 0)
		}
	})
}
