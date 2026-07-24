// Package metrics defines the proxy data-plane metrics, instrumented
// via the OpenTelemetry Meter API.
//
// Export is push-only: httpapi wires an OTel SDK MeterProvider whose
// reader is the OTLP/HTTP exporter, sending to an OpenTelemetry
// Collector at infrastructure level. There is no /metrics endpoint in
// this binary — re-exposing as Prometheus, Datadog, etc. is the
// Collector's job.
//
// Cardinality is intentionally kept low: dst_host is NOT an attribute
// (would explode the series count); the result/reason/stage/direction
// attributes are finite enums.
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName scopes every instrument under a single OTel Meter. The
// OTLP exporter preserves dotted names; downstream Collector pipelines
// can rename or translate them per backend (e.g. `hydra.proxy.*` →
// `hydra_proxy_*` for Prometheus exposition).
const meterName = "hydra/proxy"

// Result attribute values for ConnectAttempts.
const (
	resultOK     = "ok"
	resultDenied = "denied"
	resultError  = "error"
)

// Direction attribute values for BytesTransferred.
const (
	DirectionClientToDest = "c2d"
	DirectionDestToClient = "d2c"
)

// Metrics groups every instrument the proxy emits. Construct once via
// New() and inject the same instance into every forwarder. All public
// methods are nil-safe so callers can keep using a *Metrics directly
// without guarding each call site.
type Metrics struct {
	activeTunnels    metric.Int64UpDownCounter
	connectAttempts  metric.Int64Counter
	connectDenied    metric.Int64Counter
	connectErrors    metric.Int64Counter
	bytesTransferred metric.Int64Counter
	requestsTotal    metric.Int64Counter
	setupDuration    metric.Float64Histogram
	tunnelDuration   metric.Float64Histogram

	// Pre-built attribute options for the hot paths so we don't
	// allocate a new []KeyValue on every Add call.
	attrAttemptOK     metric.MeasurementOption
	attrAttemptDenied metric.MeasurementOption
	attrAttemptError  metric.MeasurementOption
	attrDirC2D        metric.MeasurementOption
	attrDirD2C        metric.MeasurementOption
}

// New builds the Metrics bundle from an OTel MeterProvider. The
// MeterProvider must already have a reader configured (the OTLP/HTTP
// exporter; see httpapi.NewMeterProvider).
func New(mp metric.MeterProvider) (*Metrics, error) {
	m := mp.Meter(meterName)

	// Helper to keep error handling concise.
	type instrumentBuilder func() error

	out := &Metrics{
		attrAttemptOK:     metric.WithAttributes(attribute.String("result", resultOK)),
		attrAttemptDenied: metric.WithAttributes(attribute.String("result", resultDenied)),
		attrAttemptError:  metric.WithAttributes(attribute.String("result", resultError)),
		attrDirC2D:        metric.WithAttributes(attribute.String("direction", DirectionClientToDest)),
		attrDirD2C:        metric.WithAttributes(attribute.String("direction", DirectionDestToClient)),
	}

	builders := []instrumentBuilder{
		func() (err error) {
			out.activeTunnels, err = m.Int64UpDownCounter(
				"hydra.proxy.active_tunnels",
				metric.WithDescription("Number of currently open CONNECT tunnels."),
			)
			return err
		},
		func() (err error) {
			out.connectAttempts, err = m.Int64Counter(
				"hydra.proxy.connect_attempts",
				metric.WithDescription("CONNECT requests processed by outcome."),
			)
			return err
		},
		func() (err error) {
			out.connectDenied, err = m.Int64Counter(
				"hydra.proxy.connect_denied",
				metric.WithDescription("CONNECT denials by reason."),
			)
			return err
		},
		func() (err error) {
			out.connectErrors, err = m.Int64Counter(
				"hydra.proxy.connect_errors",
				metric.WithDescription("CONNECT failures by pipeline stage."),
			)
			return err
		},
		func() (err error) {
			out.bytesTransferred, err = m.Int64Counter(
				"hydra.proxy.bytes",
				metric.WithDescription("Bytes piped through tunnels."),
				metric.WithUnit("By"),
			)
			return err
		},
		func() (err error) {
			out.requestsTotal, err = m.Int64Counter(
				"hydra.proxy.requests",
				metric.WithDescription("Proxy requests by method, status class and routing decision."),
			)
			return err
		},
		func() (err error) {
			// Bucket boundaries are configured via SDK Views in
			// httpapi.NewMeterProvider so they live with the SDK
			// configuration, not the instrument definition.
			out.setupDuration, err = m.Float64Histogram(
				"hydra.proxy.connect_setup_seconds",
				metric.WithDescription("Time from CONNECT received to 200 Established."),
			)
			return err
		},
		func() (err error) {
			out.tunnelDuration, err = m.Float64Histogram(
				"hydra.proxy.tunnel_seconds",
				metric.WithDescription("Total CONNECT tunnel lifetime."),
			)
			return err
		},
	}
	for _, b := range builders {
		if err := b(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// -- Facade methods ---------------------------------------------------
//
// Every recording method here is nil-safe: when *Metrics is nil the
// call is a no-op, so call sites don't need to wrap each metric write
// in `if m != nil`. They also encapsulate the OTel API so the rest
// of the codebase only depends on this package.

// RecordTunnelStarted reports a successful CONNECT setup: it observes
// the time it took to negotiate the tunnel, increments the active
// counter, and bumps connect_attempts{result=ok}.
func (m *Metrics) RecordTunnelStarted(ctx context.Context, setup time.Duration) {
	if m == nil {
		return
	}
	m.setupDuration.Record(ctx, setup.Seconds())
	m.activeTunnels.Add(ctx, 1)
	m.connectAttempts.Add(ctx, 1, m.attrAttemptOK)
}

// RecordTunnelEnded fires once a tunnel has finished, reporting the
// active-tunnel decrement, total lifetime, and total bytes per
// direction in a single call so the close callback stays terse.
func (m *Metrics) RecordTunnelEnded(ctx context.Context, dur time.Duration, bytesC2D, bytesD2C int64) {
	if m == nil {
		return
	}
	m.activeTunnels.Add(ctx, -1)
	m.tunnelDuration.Record(ctx, dur.Seconds())
	if bytesC2D > 0 {
		m.bytesTransferred.Add(ctx, bytesC2D, m.attrDirC2D)
	}
	if bytesD2C > 0 {
		m.bytesTransferred.Add(ctx, bytesD2C, m.attrDirD2C)
	}
}

// RecordDenied increments connect_attempts{result=denied} and
// connect_denied{reason=…}. Use for ACL/limit/policy rejections.
func (m *Metrics) RecordDenied(ctx context.Context, reason string) {
	if m == nil {
		return
	}
	m.connectAttempts.Add(ctx, 1, m.attrAttemptDenied)
	m.connectDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordError increments connect_attempts{result=error} and
// connect_errors{stage=…}. Use for unexpected failures (dial,
// hijack, handshake).
func (m *Metrics) RecordError(ctx context.Context, stage string) {
	if m == nil {
		return
	}
	m.connectAttempts.Add(ctx, 1, m.attrAttemptError)
	m.connectErrors.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage)))
}

// RecordRequest counts a finished non-CONNECT proxy request.
// status_class is one of {2xx, 3xx, 4xx, 5xx, other}; decision is one
// of the router's verdicts (local, peer, hop-local).
func (m *Metrics) RecordRequest(ctx context.Context, method, statusClass, decision string) {
	if m == nil {
		return
	}
	m.requestsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("status_class", statusClass),
		attribute.String("decision", decision),
	))
}

// StatusClass maps an HTTP status code to a coarse cardinality-friendly
// label (2xx, 3xx, …). Used by callers to derive the status_class
// attribute for RecordRequest.
func StatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}
