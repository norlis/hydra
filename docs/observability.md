# Observability

Hydra emits **OpenTelemetry** metrics over **OTLP/HTTP** to a collector you run alongside the fleet (e.g. the OpenTelemetry Collector). Re-exposing as Prometheus, Datadog, Cloud Watch, etc. is a collector pipeline concern and lives outside the binary — the node itself does not serve `/metrics`.

Configure the exporter through the standard OTel environment variables (no Hydra-specific knobs):

| Variable | Default | Description |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4318` | Base URL of the OTLP/HTTP receiver. |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | — | Override for the metrics signal only. |
| `OTEL_EXPORTER_OTLP_HEADERS` | — | Comma-separated `k=v` pairs; use for auth tokens. |
| `OTEL_METRIC_EXPORT_INTERVAL` | `60000` (ms) | Push cadence. |
| `OTEL_RESOURCE_ATTRIBUTES` | — | Extra resource labels (`env=prod,region=us-east-1`). |
| `OTEL_SDK_DISABLED` | `false` | `true` turns the SDK into a no-op (useful for tests). |

`service.name=hydra`, `service.version=<git hash>`, and `service.instance.id=<HYDRA_NODE_NAME>` are injected automatically; the resource detector also adds host/process attributes.

Bundled metric names (all dotted, prefixed `hydra.proxy.*`): `active_tunnels`, `connect_attempts{result}`, `connect_denied{reason}`, `connect_errors{stage}`, `bytes{direction}`, `requests{method,status_class,decision}`, `connect_setup_seconds`, `tunnel_seconds`. Go runtime metrics (heap, GC, goroutines) are emitted by the OTel runtime instrumentation under the standard `process.runtime.go.*` namespace.
