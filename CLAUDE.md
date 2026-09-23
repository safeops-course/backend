# CLAUDE.md — SRE Backend Service

## AI Agent Guidance

### Repository Context

This is the SRE Backend microservice — a Go reference API service modelled after [podinfo](https://github.com/stefanprodan/podinfo). Used as a learning target for SRE and Kubernetes workflows. Provides health probes, chaos engineering endpoints, Prometheus metrics, OpenTelemetry tracing, and JWT auth. Deployed via FluxCD to k3s on Hetzner Cloud.

### AI Agent Operating Principles

**Critical Instructions for AI Agents:**

- **Tool Result Reflection**: After receiving tool results, carefully reflect on their quality and determine optimal next steps before proceeding. Use your thinking to plan and iterate based on this new information, and then take the best next action.
- **Parallel Execution**: For maximum efficiency, whenever you need to perform multiple independent operations, invoke all relevant tools simultaneously rather than sequentially.
- **Temporary File Management**: If you create any temporary new files, scripts, or helper files for iteration, clean up these files by removing them at the end of the task.
- **High-Quality Solutions**: Write high quality, general purpose solutions. Implement solutions that work correctly for all valid inputs, not just specific cases. Do not hard-code values or create solutions that only work for specific scenarios.
- **Problem Understanding**: Focus on understanding the problem requirements and implementing the correct approach. Provide principled implementations that follow best practices and software design principles.
- **Feasibility Assessment**: If the task is unreasonable or infeasible, say so. The solution should be robust, maintainable, and extendable.

### Zen Principles of This Repo

*Inspired by PEP 20 — The Zen of Python, applied to Go service code:*

- **Beautiful is better than ugly** — Clean, readable Go over complex nested expressions
- **Explicit is better than implicit** — Clear variable names and documented intentions
- **Simple is better than complex** — Straightforward logic over clever abstractions
- **Complex is better than complicated** — When complexity is needed, make it organized not chaotic
- **Readability counts** — Code is read more often than written
- **Special cases aren't special enough to break the rules** — Consistency over exceptions
- **Errors should never pass silently** — Fail loud and early with clear messages
- **In the face of ambiguity, refuse the temptation to guess** — Test and verify, don't assume
- **If the implementation is hard to explain, it's a bad idea** — Complex patterns need clear documentation
- **If the implementation is easy to explain, it may be a good idea** — Simple solutions are often best
- **If you need a decoder ring to understand the code, rewrite it simpler** — No hieroglyphs!
- **There should be one obvious way to do it** — Establish patterns and stick to them
- **Be humble enough to build systems that are better than you** — Create safeguards that protect against human error, forgetfulness, and AI session resets

### Core Philosophical Principles

**KISS (Keep It Simple, Stupid)** — The fundamental principle guiding ALL decisions in this repository:
- Keep it simple and don't over-engineer solutions
- No hieroglyphs — code should be readable by humans, not just compilers
- Avoid complex regex patterns when simple logic works
- Replace nested function calls with clear step-by-step operations
- Use descriptive comments for complex validation logic
- If you need a decoder ring to understand the code, rewrite it simpler

**The "Be Humble" Principle** — Create safeguards that protect against:
- Human error and oversight
- AI session resets and context loss
- Complex edge cases that might be forgotten
- Future developers who may not understand the original intent

## Project Overview

Go microservice serving as the SRE Control Plane Backend — a reference API service modelled after [podinfo](https://github.com/stefanprodan/podinfo). Used as a learning target for SRE and Kubernetes workflows. Provides health probes, chaos engineering endpoints, Prometheus metrics, OpenTelemetry tracing, and JWT auth.

**Module:** `github.com/ldbl/sre/backend`

## Project Structure

```
cmd/api/main.go              # Entry point
pkg/
  config/config.go            # Config struct, env/flag parsing
  server/
    server.go                 # HTTP server, routes, middleware (~1000 lines)
    server_test.go            # Endpoint tests (httptest)
    token.go                  # JWT generation/validation
    types.go                  # Response types
  telemetry/
    telemetry.go              # OpenTelemetry + Uptrace init, metrics
    middleware.go             # otelhttp middleware
  configwatch/configwatch.go  # fsnotify ConfigMap/Secret hot-reload
  logger/logger.go            # otelzap structured logging
  version/version.go          # Build-time ldflags (version, commit, date)
  api/docs/                   # Auto-generated Swagger docs (swag)
```

## Key Technologies

- **Go 1.24** with chi router
- **Prometheus** client_golang for metrics
- **OpenTelemetry** + Uptrace for distributed tracing
- **otelzap** (zap) for structured logging
- **JWT** (golang-jwt/jwt/v5) for token auth
- **fsnotify** for ConfigMap/Secret hot-reload
- **swaggo** for Swagger UI

## Build & Run

```bash
make build          # compile to ./bin/backend with ldflags
make run            # go run with ldflags
make image          # docker build (multi-stage, alpine)
make publish        # build + push to ghcr.io/ldbl/sre-backend
go test ./...       # run tests
```

## Configuration (Environment Variables)

| Env Var | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `UI_MESSAGE` | `"Welcome to the SRE control plane"` | Landing page message |
| `UI_COLOR` | `"#2E5CFF"` | Accent color |
| `RANDOM_DELAY_MAX` | `0` | Max random delay per request (ms) |
| `RANDOM_ERROR_RATE` | `0` | Probability [0-1] of injecting HTTP 500 |
| `CONFIG_PATH` | `""` | Directory to watch for ConfigMap changes |
| `JWT_SECRET` | `"change-me-in-production"` | HMAC-SHA256 signing secret |
| `DEPLOYMENT_ENVIRONMENT` | `""` | `production`/`staging` = JSON logging |
| `UPTRACE_DSN` | `""` | Uptrace exporter DSN (optional) |

Version info (`APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`) is injected via ldflags at build time.

## API Endpoints

**Health probes:** `/healthz`, `/readyz`, `/livez` (with enable/disable toggles)
**Chaos:** `/panic`, `/status/{code}`, `/delay/{seconds}`
**Observability:** `/metrics` (Prometheus), `/error/{level}`
**Debug:** `/env`, `/headers`, `/echo`
**Auth:** `POST /token`, `GET /token/validate`
**Docs:** `/openapi` (JSON spec), `/swagger/*` (Swagger UI)
**Profiling:** `/debug/pprof/*`
**Info:** `/version`, `/` (HTML landing page), `/configs`

## CI/CD

- **build.yml** — on push to main/develop: build multi-platform Docker image (amd64+arm64), push to GHCR, Trivy scan
- **promote-production.yml** — manual: Trivy gate (blocking, CRITICAL only), re-tag staging image as production, create GitHub Release, bump version tag

## Coding Guidelines

- Keep the service simple — it's a reference/learning service, not a production product
- All config via environment variables — no config files
- Tests use `net/http/httptest` — no external test frameworks
- Prometheus metrics use a custom registry (not the global default)
- Middleware order matters: RequestID → RealIP → Recoverer → CORS → OTel → Metrics → Logging → RandomBehavior
- Version info is injected via ldflags — never hardcode versions
- Docker image runs as non-root user `app` (uid 10001)
- Trivy scans are non-blocking in CI (build), blocking for production promotion
