# Backend (Go)

Reference API service for the SRE Control Plane, part of the [SafeOps Academy](https://safeops.work/) course. Built with Go 1.25, modelled after [podinfo](https://github.com/stefanprodan/podinfo) — intentionally small but production-shaped. Deployed via [FluxCD](https://fluxcd.io/) to k3s on Hetzner Cloud with automatic image updates across develop, staging, and production environments.

## Specifications

- Health checks (readiness, liveness, startup probes)
- Graceful shutdown on SIGINT/SIGTERM signals
- File watcher for Kubernetes Secrets and ConfigMaps hot-reload
- Instrumented with Prometheus (custom registry) and OpenTelemetry
- Structured logging with zap (JSON in production, console in development)
- 12-factor app configuration via environment variables
- Fault injection (random errors and latency via `RANDOM_ERROR_RATE` / `RANDOM_DELAY_MAX`)
- Swagger UI and OpenAPI 3 spec
- JWT authentication with Postgres-backed user store
- Multi-arch container image (amd64 + arm64) with Docker buildx and GitHub Actions
- CVE scanning with Trivy (non-blocking in CI, blocking for production promotion)
- Go vulnerability scanning with govulncheck
- Container image signing with Sigstore cosign (keyless, GitHub OIDC)
- SBOM attestation (SPDX) embedded in the container image via cosign
- Supply chain verification with Kyverno policies (signature + attestation)
- Non-root container (uid 10001) with read-only root filesystem
- Kustomize-based deployment with per-environment overlays
- Canary deployments with Flagger (develop environment)
- HPA auto-scaling (develop environment)

## Endpoints

| Path | Method | Description |
|---|---|---|
| `/` | GET | HTML landing page with build metadata |
| `/healthz` | GET | Liveness-style health check |
| `/readyz` | GET | Readiness status |
| `/readyz/enable`, `/readyz/disable` | PUT | Toggle readiness (auth required) |
| `/livez` | GET | Liveness status |
| `/livez/enable`, `/livez/disable` | PUT | Toggle liveness (auth required) |
| `/version` | GET | Build version, commit, and timestamp |
| `/env` | GET | Server-side environment variables (sanitized) |
| `/headers` | GET | Request headers (for debugging) |
| `/echo` | POST | Echo request body and metadata |
| `/configs` | GET | Current ConfigMap/Secret values |
| `/status/{code}` | GET | Return a specific HTTP status code |
| `/delay/{seconds}` | GET | Add a fixed delay before responding |
| `/error/{level}` | GET | Log at specified level (debug/info/warn/error) |
| `/panic` | GET | Force a panic (chaos test, auth required) |
| `/metrics` | GET | Prometheus metrics (custom registry) |
| `/openapi` | GET | OpenAPI 3 JSON spec |
| `/swagger/*` | GET | Swagger UI |
| `/debug/pprof/*` | GET | Go profiling endpoints |
| `/auth/register` | POST | Create user and return JWT |
| `/auth/login` | POST | Login and return JWT |
| `/auth/me` | GET | Return current authenticated user |

## Runtime Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `UI_MESSAGE` | `Welcome to the SRE control plane` | Landing page message |
| `UI_COLOR` | `#2E5CFF` | Accent color |
| `RANDOM_DELAY_MAX` | `0` | Max random delay per request (ms) |
| `RANDOM_ERROR_RATE` | `0` | Probability 0–1 of injecting HTTP 500 |
| `CONFIG_PATH` | | Directory to watch for ConfigMap changes |
| `JWT_SECRET` | `change-me-in-production` | HMAC-SHA256 signing secret |
| `JWT_TOKEN_TTL_MINUTES` | `60` | Token expiry |
| `DEPLOYMENT_ENVIRONMENT` | | `production`/`staging` = JSON logging |
| `UPTRACE_DSN` | | Uptrace exporter DSN |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | | CloudNativePG credentials |
| `POSTGRES_HOST` | | Postgres host (e.g. `app-postgres-rw`) |
| `POSTGRES_DB` | `app` | Database name |

Version info (`APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`) is injected via ldflags at build time.

## Observability

- **Metrics** — Prometheus via custom registry at `/metrics`
- **Tracing** — OpenTelemetry SDK with Uptrace exporter, automatic HTTP instrumentation via `otelhttp`
- **Logging** — Structured logging with `otelzap` (JSON in production, console in development)
- **ConfigWatch** — `fsnotify`-based hot-reload for mounted ConfigMaps and Secrets

## CI/CD

Two GitHub Actions workflows:

- **build.yml** — triggers on push to `main`/`develop`: builds multi-platform Docker image (linux/amd64 + linux/arm64), pushes to GHCR, runs Trivy vulnerability scan (non-blocking)
- **promote-production.yml** — manual trigger: runs Trivy scan (blocking on CRITICAL), re-tags staging image as production, creates GitHub Release, bumps version tag

Images are pushed to `ghcr.io/safeops-course/backend` with tags like `develop-v0.0.5-abc1234-1234567890`.

## Kubernetes Deployment

Deployed via FluxCD with environment overlays:

- **develop** — 1 replica, minimal resources, HPA, Flagger canary
- **staging** — 1 replica, moderate resources
- **production** — 2 replicas, higher resource limits

Image tags are automatically updated by Flux ImageUpdateAutomation using ImagePolicy filters per environment.

Database is managed by CloudNativePG (`app-postgres` cluster) with per-environment instances.

## Local Development

```bash
go run ./cmd/api
```

## Docker

```bash
docker build -t backend:local .
docker run -p 8080:8080 backend:local
```

## Project Structure

```
cmd/api/main.go              # Entry point
pkg/
  config/config.go            # Config struct, env/flag parsing
  server/
    server.go                 # HTTP server, routes, middleware
    server_test.go            # Endpoint tests (httptest)
    auth_handlers.go          # Registration, login, me endpoints
    auth_store.go             # Postgres/file auth store
    token.go                  # JWT generation/validation
    types.go                  # Response types
  telemetry/
    telemetry.go              # OpenTelemetry + Uptrace init
    middleware.go             # otelhttp middleware
  configwatch/configwatch.go  # fsnotify ConfigMap/Secret hot-reload
  logger/logger.go            # otelzap structured logging
  version/version.go          # Build-time ldflags
```
