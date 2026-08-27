# OpenTelemetry and SLO foundation

`BE-P2-013` provides vendor-neutral OTLP/HTTP trace and metric export through an OpenTelemetry Collector. Traces and metrics are stable Go signals; application logs remain structured `slog` JSON with trace and span correlation rather than adopting the still-beta Go log signal.

The HTTP middleware records only a caller-supplied static route template, method and response status. Query strings, raw paths, headers, request bodies and end-user identifiers are deliberately absent. Outbound spans identify an approved dependency name but do not capture its URL. The same central sensitive-key policy redacts manually supplied trace attributes and log attributes.

W3C `traceparent` plus baggage propagation is configured explicitly. `Traceparent` writes the current context into an event envelope, and `StartConsumer` extracts it to continue the trace while recording low-cardinality queue-lag metrics. This provides HTTP-to-outbox-to-consumer correlation without putting sensitive data in events.

The service RED dashboard, multi-window availability alerts, latency alert, async queue/provider alerts and linked response runbooks live under `deploy/observability` and `docs/runbooks`. Production sends telemetry to a regional collector using standard `OTEL_EXPORTER_OTLP_*` variables; collector backend authorization is injected from the deployment secret store.

Official implementation references: [OpenTelemetry Go](https://opentelemetry.io/docs/languages/go/), [Go exporters](https://opentelemetry.io/docs/languages/go/exporters/), and [propagator requirements](https://opentelemetry.io/docs/specs/otel/context/api-propagators/).
