# Backend Telemetry

What the backend sends, and where to find it.

## Scope

The backend sends:
- traces (OpenTelemetry, exported to Uptrace)
- logs correlated with traces (`trace_id`)
- Prometheus metrics on `/metrics`
- OpenTelemetry metrics (HTTP, database, Go runtime)

## Initialization

- `cmd/api/main.go` calls `telemetry.Init(ctx)` at startup, before the logger is created.
- With `UPTRACE_DSN` set, uptrace-go configures traces, metrics and logs and exports them to Uptrace.
- Without `UPTRACE_DSN` the service runs without remote export and logs that it does.
- On shutdown `uptrace.Shutdown(ctx)` flushes what is buffered.

Resource attributes:
- `service.name` (default `backend`)
- `service.version` (default: the version baked in at build time, `version.Version` - the same value
  `/version` reports; `SERVICE_VERSION` overrides it)
- `deployment.environment.name` (from `DEPLOYMENT_ENVIRONMENT`, default `development`)
- anything in `OTEL_RESOURCE_ATTRIBUTES` (the platform sets `k8s.cluster.name`)

## Tracing

HTTP:
- `pkg/telemetry/middleware.go` wraps the router with `otelhttp.NewHandler`.
- Span name: the method plus the chi route pattern (`GET /delay/{seconds}`), so the set of names stays
  bounded.
- Not traced: `/healthz`, `/readyz`, `/livez` and `/metrics` - probes and Prometheus scrapes say nothing
  about users and would otherwise be almost every span.

Database:
- `otelpgx` on the pgx connection (`pkg/server/auth_store.go`): one span per query.

Propagation:
- W3C `traceparent` / `tracestate` and `baggage`. The browser reaches the API on the same origin, through
  the frontend's nginx (`/api/`), so no CORS is involved.

Span helpers in `pkg/telemetry`: `StartSpan`, `AddEvent`, `SetAttributes`, `RecordError` (sets the span
status to ERROR).

## Logs and correlation

- `pkg/logger/logger.go` wraps zap with `otelzap`.
- Calls through `logger.Ctx(ctx)` also send an OpenTelemetry log record, linked to the active span.
  Calls without a context write only to stdout.
- The request log (`loggingMiddleware`) and `/panic` add `trace_id` to the log line itself
  (`traceIDFromContext`), so a line found with `kubectl logs` leads to its trace.
- Startup and fatal logs run outside a request and have no `trace_id`.

## Metrics

### Prometheus (`GET /metrics`)

Custom `prometheus.Registry` in `Server`:
- `app_http_requests_total{method,path,status}`
- `app_http_request_duration_seconds{method,path}`
- `app_http_in_flight_requests`
- Go and process collectors

`path` is the route pattern (`/status/{code}`), not the raw path.

### OpenTelemetry

- `http.server.request.duration` and the request/response body sizes - from `otelhttp`;
  `metricsMiddleware` adds `http.route` (the chi pattern) through otelhttp's labeler.
- `db.client.operation.duration` - from `otelpgx`.
- Go runtime metrics - from uptrace-go.

## Configuration

- `UPTRACE_DSN`
- `SERVICE_NAME`
- `SERVICE_VERSION` (optional; defaults to the build version)
- `DEPLOYMENT_ENVIRONMENT`
- `OTEL_RESOURCE_ATTRIBUTES`

## Quick check

1. The backend logs `OpenTelemetry initialized`, or that `UPTRACE_DSN` is not set.
2. A request to `/version` appears in Uptrace as `GET /version`, with `service.version` equal to what
   `/version` returns.
3. The request log for it carries `trace_id`, in `kubectl logs` and in Uptrace.
4. `/metrics` returns `app_http_requests_total` and `app_http_request_duration_seconds`; there is no
   `GET /metrics` span in Uptrace.
