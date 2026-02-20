# Backend (Go)

Reference API service for the SRE control plane. This service is intentionally small but production-shaped: health probes, metrics, tracing hooks, auth, and chaos endpoints.

## Endpoints

- `GET /` HTML landing page with build metadata.
- `GET /healthz` Liveness-style health check.
- `GET /readyz` Readiness status.
- `PUT /readyz/enable` / `PUT /readyz/disable` Toggle readiness (auth required).
- `GET /livez` Liveness status.
- `PUT /livez/enable` / `PUT /livez/disable` Toggle liveness (auth required).
- `GET /version` Build version, commit, and timestamp.
- `GET /env` Server-side environment variables (sanitized).
- `GET /headers` Request headers (for debugging).
- `POST /echo` Echo request body and metadata.
- `GET /status/{code}` Return a specific HTTP status code.
- `GET /delay/{seconds}` Add a fixed delay before responding.
- `GET /panic` Force a panic (chaos test, auth required).
- `GET /metrics` Prometheus metrics.
- `GET /openapi` OpenAPI 3 spec.
- `GET /swagger` Swagger UI.
- `POST /auth/register` Create user and return JWT.
- `POST /auth/login` Login and return JWT.
- `GET /auth/me` Return current authenticated user.

## Runtime Config (Env Vars)

- `PORT` (default: `8080`)
- `UI_MESSAGE` / `UI_COLOR` for the landing page
- `APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`
- `RANDOM_DELAY_MAX` (ms)
- `RANDOM_ERROR_RATE` (0–1)
- `JWT_SECRET`
- `JWT_TOKEN_TTL_MINUTES` (default: `60`)
- `DATABASE_URL` (Postgres DSN for auth store)
- `AUTH_DB_PATH` (local fallback file store path, used when `DATABASE_URL` is empty)

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
