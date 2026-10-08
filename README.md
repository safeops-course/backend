# Backend (Go)

Reference API service for the SRE Control Plane, part of the [SafeOps Academy](https://safeops.work/) course. Built with Go 1.25, modelled after [podinfo](https://github.com/stefanprodan/podinfo) — intentionally small but production-shaped. Deployed via [FluxCD](https://fluxcd.io/) to k3s on Hetzner Cloud with automatic image updates across develop, staging, and production environments.

## Specifications

- Health checks (readiness, liveness, startup probes)
- Graceful shutdown on SIGINT/SIGTERM signals
- File watcher for Kubernetes Secrets and ConfigMaps hot-reload
- Instrumented with Prometheus (custom registry) and OpenTelemetry
- Structured logging with zap (JSON in production, console in development)
- 12-factor app configuration via environment variables
- Fault injection (random errors and latency via `RANDOM_ERROR_RATE` / `RANDOM_DELAY_MAX`; the probes and `/metrics` are exempt)
- Swagger UI and OpenAPI 3 spec
- JWT authentication with Postgres-backed user store
- Multi-arch container image (amd64 + arm64) with Docker buildx and GitHub Actions
- CVE scanning with Trivy before the image is pushed (blocking on fixable CRITICAL/HIGH) and at production promotion
- Go vulnerability scanning with govulncheck
- Container image signing with Sigstore cosign (keyless, GitHub OIDC)
- SBOM attestation (SPDX) embedded in the container image via cosign
- Supply chain verification with Kyverno policies (signature + attestation)
- Non-root container (uid 10001) with read-only root filesystem
- Kustomize-based deployment with per-environment overlays
- Canary deployments with Flagger (develop, opt-in: switched on in the course's advanced chapter)
- HPA auto-scaling (develop environment)

## Endpoints

| Path | Method | Description |
|---|---|---|
| `/` | GET | HTML landing page with build metadata |
| `/healthz` | GET | Liveness-style health check |
| `/readyz` | GET | Readiness status |
| `/readyz/enable`, `/readyz/disable` | PUT | Toggle readiness (`CHAOS_ENABLED=true` + auth) |
| `/livez` | GET | Liveness status |
| `/livez/enable`, `/livez/disable` | PUT | Toggle liveness (`CHAOS_ENABLED=true` + auth) |
| `/version` | GET | Build version, commit, timestamp, `chaos_enabled` |
| `/env` | GET | Allowlisted runtime variables only (pod, namespace, environment, version, `FEATURE_*`) - never secrets |
| `/headers` | GET | Request headers (for debugging) |
| `/echo` | POST | Echo request body (always `application/octet-stream`) |
| `/configs` | GET | Keys of the watched directory (`CONFIG_PATH`) with a keyed fingerprint of each value (changes on reload) - never the values or anything to check a guess against |
| `/status/{code}` | GET | Return a specific HTTP status code |
| `/delay/{seconds}` | GET | Delay 0..`DELAY_MAX_SECONDS` seconds (stops when the client leaves) |
| `/error/{level}` | GET | Log at specified level (debug/info/warn/error) |
| `/panic` | GET | Exit the process with code 255 (`CHAOS_ENABLED=true` + auth) |
| `/metrics` | GET | Prometheus metrics (custom registry) |
| `/openapi` | GET | OpenAPI 3 JSON spec |
| `/swagger/*` | GET | Swagger UI |
| `/auth/register` | POST | Create user and return JWT (`AUTH_REGISTRATION_ENABLED`, rate limited) |
| `/auth/login` | POST | Login and return JWT (rate limited per username; at most 2 bcrypt checks per pod at once, beyond that 429). Wrong credentials: 401; the user store failing (the database down): 500, logged - so it counts against the error budget |
| `/auth/me` | GET | Return current authenticated user |
| `/token/validate` | GET | Validate a bearer token |

There is no endpoint that issues a token without a password, and Go profiling is not on this
router: with `PPROF_ENABLED=true` it listens on `PPROF_ADDR` (loopback) - use `kubectl port-forward`.
`TestRouteInventory` fails when a route is added without updating its list: review what a new route
exposes before adding it there.

## Runtime Configuration

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `UI_MESSAGE` | `Welcome to the SRE control plane` | Landing page message |
| `UI_COLOR` | `#2E5CFF` | Accent color |
| `RANDOM_DELAY_MAX` | `0` | Max random delay per request (ms); not for `/healthz`, `/readyz`, `/livez`, `/metrics` |
| `RANDOM_ERROR_RATE` | `0` | Probability 0–1 of injecting HTTP 500; not for the probes and `/metrics` |
| `CONFIG_PATH` | | Directory to watch for ConfigMap changes (`/configs` shows keys and keyed fingerprints, not values) |
| `JWT_SECRET` | (required) | HMAC-SHA256 signing secret, at least 32 characters - the server does not start without it |
| `JWT_TOKEN_TTL_MINUTES` | `60` | Token expiry |
| `DEPLOYMENT_ENVIRONMENT` | | `production`/`staging` = JSON logging |
| `UPTRACE_DSN` | | Uptrace exporter DSN |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | | CloudNativePG credentials |
| `POSTGRES_HOST` | | Postgres host (e.g. `app-postgres-rw`) |
| `POSTGRES_DB` | `app` | Database name |

Without a database the backend keeps users in a local file (`AUTH_DB_PATH`, default `/tmp/users.json`) - for local development only. Inside Kubernetes (`KUBERNETES_SERVICE_HOST` set) it refuses to start without one: every pod would have its own users. Each replica opens at most 10 connections to Postgres.
| `CHAOS_ENABLED` | `false` | Expose `/panic` and the readiness/liveness toggles (still need a token) |
| `PPROF_ENABLED` | `false` | Serve `/debug/pprof/*` on `PPROF_ADDR` |
| `PPROF_ADDR` | `127.0.0.1:6060` | Profiling listener (loopback: reach it with `kubectl port-forward`) |
| `DELAY_MAX_SECONDS` | `10` | Upper bound for `/delay/{seconds}` (greater than 0, at most 300; the HTTP write timeout is this + 15s) |
| `AUTH_REGISTRATION_ENABLED` | `true` | Allow `POST /auth/register` |
| `AUTH_LOGIN_ATTEMPTS_PER_MINUTE` | `10` | Login attempts per username per minute, per pod |
| `AUTH_REGISTRATIONS_PER_MINUTE` | `10` | Registrations per minute, per pod |

Invalid values fail loudly: a short `JWT_SECRET`, a boolean that is not `true`/`false` (unless its
flag overrides it), a zero limit, or a `DELAY_MAX_SECONDS` of 0 or less or greater than 300, stops the start with a
message naming the variable.

Rate limits: when 10 000 keys are tracked, the oldest window is evicted - login never locks out
everyone; more guesses on one account cost an attacker 10 000 requests per extra window.

Version info (`APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`) is injected via ldflags at build time.

## Observability

- **Metrics** — Prometheus via custom registry at `/metrics`
- **Tracing** — OpenTelemetry SDK with Uptrace exporter, automatic HTTP instrumentation via `otelhttp`
- **Logging** — Structured logging with `otelzap` (JSON in production, console in development)
- **ConfigWatch** — `fsnotify`-based hot-reload for a mounted ConfigMap (`/configs` shows which keys changed via their fingerprints)

## CI/CD

Two GitHub Actions workflows:

- **pr.yml** — every pull request: vet, race tests, govulncheck, golangci-lint + gosec, docker build, gitleaks on the PR commits
- **build.yml** — triggers on push to `main`/`develop`: builds linux/amd64 and linux/arm64 locally, Trivy-scans both (blocking, **before** anything is pushed), pushes exactly those scanned images and joins them into one multi-platform index, then signs it with cosign (keyless), attaches an SBOM attestation (SPDX) and SLSA build provenance (`actions/attest-build-provenance`, verify with `gh attestation verify`)
- **promote-production.yml** — manual trigger: runs Trivy scan (blocking on CRITICAL), re-tags staging image as production, creates GitHub Release, bumps version tag

Images are pushed to `ghcr.io/safeops-course/backend` with tags like `develop-v0.0.5-abc1234-1234567890`.

## Kubernetes Deployment

Deployed via FluxCD with environment overlays:

- **develop** — 1 replica, minimal resources, HPA (Flagger canary opt-in, see the sre repo)
- **staging** — 1 replica, moderate resources
- **production** — 2 replicas, higher resource limits

Image tags are automatically updated by Flux ImageUpdateAutomation using ImagePolicy filters per environment.

Database is managed by CloudNativePG (`app-postgres` cluster) with per-environment instances.

## Database Migrations

The schema lives in versioned SQL files, `pkg/migrations/sql/NNNN_name.up.sql`, embedded in the binary.

- `backend migrate` applies the ones the database does not have yet and exits. In Kubernetes it is the
  Deployment's `migrate` initContainer - the same image (the same signed digest) as the app.
  golang-migrate holds a Postgres advisory lock, so replicas starting together migrate once.
- The app never changes the schema. At startup it checks `schema_migrations`: never migrated, dirty,
  or older than `migrations.RequiredVersion` - it refuses to start, and says why. A **newer** schema
  is fine: the previous release must keep running after the next one migrated, so rolling back the
  image never needs a rollback of the data.
- Only `.up.sql` files. A schema is never rolled back; a mistake is fixed by the next migration.
  Every migration is compatible with the code of the previous release (expand / contract): add before
  use, stop using before remove. Chapter 18 of the course walks through it.
- A new file needs `RequiredVersion` raised in `pkg/migrations/migrations.go` - a test fails otherwise.
- `backend migrate` on a schema **newer** than the build knows (the image was rolled back after the next
  release migrated) applies nothing and succeeds - the rollback's initContainer must not fail. A newer
  schema that is dirty is never skipped.
- CI proves the rule on every pull request (`previous release on this schema (N-1)` in `pr.yml`): it
  migrates a database with the pull request's binary, runs the base branch's `migrate` on it (the
  rollback's initContainer), starts the base branch's app on it, and registers and logs in. A migration that breaks the previous release fails the pull request.
- Example of an expand step: `0002` adds the optional `display_name` (NULL, no default). The app writes
  it on register; `FEATURE_DISPLAY_NAME=true` returns it in the register and login responses - the
  read side is switched separately from the deploy.

The migration tests need a real Postgres (CI starts one); without `TEST_DATABASE_URL` they skip:

```bash
docker run -d --rm --name pg -e POSTGRES_PASSWORD=test -p 55432:5432 postgres:17
TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/postgres?sslmode=disable' go test ./pkg/migrations/
```

## Local Development

```bash
go run ./cmd/api              # file-based auth store, no database
# with Postgres: DATABASE_URL=... go run ./cmd/api migrate && DATABASE_URL=... go run ./cmd/api
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
