# Backend (Go)

Reference API service for the SRE control plane. This service is intentionally small but production-shaped: health probes, metrics, tracing hooks, and chaos endpoints.

## Endpoints

- `GET /` HTML landing page with build metadata.
- `GET /healthz` Liveness-style health check.
- `GET /readyz` Readiness status.
- `PUT /readyz/enable` / `PUT /readyz/disable` Toggle readiness.
- `GET /livez` Liveness status.
- `PUT /livez/enable` / `PUT /livez/disable` Toggle liveness.
- `GET /version` Build version, commit, and timestamp.
- `GET /env` Server-side environment variables (sanitized).
- `GET /headers` Request headers (for debugging).
- `POST /echo` Echo request body and metadata.
- `GET /status/{code}` Return a specific HTTP status code.
- `GET /delay/{seconds}` Add a fixed delay before responding.
- `GET /panic` Force a panic (chaos test).
- `GET /metrics` Prometheus metrics.
- `GET /openapi` OpenAPI 3 spec.
- `GET /swagger` Swagger UI.

## Runtime Config (Env Vars)

- `PORT` (default: `8080`)
- `UI_MESSAGE` / `UI_COLOR` for the landing page
- `APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`
- `RANDOM_DELAY_MAX` (ms)
- `RANDOM_ERROR_RATE` (0–1)

## Tracing

OpenTelemetry hooks are wired via `pkg/telemetry`. Provide tracing config through the environment used by your exporter (e.g. `UPTRACE_DSN` for Uptrace).

## Local Dev

```bash
go run ./cmd/api
```

## Docker

```bash
docker build -t backend:local .
docker run -p 8080:8080 backend:local
```
