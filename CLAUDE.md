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

## Lab by Design - read this before a security review

This service is the **learning target** of the SafeOps course, not a product. Learners must be able to
break it and watch how Kubernetes and the platform react. The following is **intended** and is not a
finding by itself:

| What | Why it exists |
|---|---|
| `GET /panic`, `PUT /readyz\|livez/{enable,disable}` | Crash the pod, make it not ready or not live, and watch restarts, Endpoints, alerts and traces (Ch11, Ch13, Ch14, Ch15). Exist only with `CHAOS_ENABLED=true` (develop, staging); any registered user may call them - registration is open on purpose. |
| `/delay/{seconds}` (capped by `DELAY_MAX_SECONDS`), `/status/{code}`, `/error/{level}` | Produce latency, status codes and error logs on demand, so learners can see them in metrics, logs, traces and SLOs. Public, no token. |
| `/headers`, `/echo`, `/env` (allowlist), `/version`, `/metrics` | Let learners inspect what the system sees and exports. |

**What IS a finding** (report it):
- chaos reachable in **production** (`CHAOS_ENABLED` must be `"false"` there; sre `scripts/check-app-security.sh` enforces it);
- the delay cap removed or bypassed, pprof on the public router, `/env` returning a secret, a real secret or token in a response or log;
- anything that lets a learner or visitor reach **other** systems (the cluster API, other namespaces, the database) or other users' data;
- a crash or resource use the endpoints do not intend (e.g. a request that kills the pod without `/panic`).

Be proportional: rank by what a visitor can really do to *someone else*, not by the fact that the lab
lets people break their own demo environment.

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

- **Go 1.27** with chi router
- **Prometheus** client_golang for metrics
- **OpenTelemetry** + Uptrace for distributed tracing
- **otelzap** (zap) for structured logging
- **JWT** (golang-jwt/jwt/v5) for token auth
- **fsnotify** for ConfigMap/Secret hot-reload
- **swaggo** for Swagger UI

## Build & Run

```bash
make install-hooks  # once per clone: the CI checks as pre-commit/pre-push hooks (.pre-commit-config.yaml)
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
| `JWT_SECRET` | (required, >= 32 chars) | HMAC-SHA256 signing secret; start fails without it |
| `CHAOS_ENABLED` | `false` | `/panic` + readiness/liveness toggles exist only when true (still need a token) |
| `PPROF_ENABLED` / `PPROF_ADDR` | `false` / `127.0.0.1:6060` | Profiling on its own loopback listener, never the public router |
| `DELAY_MAX_SECONDS` | `10` | Upper bound for `/delay/{seconds}` |
| `AUTH_REGISTRATION_ENABLED` | `true` | Allow `POST /auth/register` |
| `AUTH_LOGIN_ATTEMPTS_PER_MINUTE` / `AUTH_REGISTRATIONS_PER_MINUTE` | `10` / `10` | In-pod rate limits (per username / per pod) |
| `DEPLOYMENT_ENVIRONMENT` | `""` | `production`/`staging` = JSON logging |
| `UPTRACE_DSN` | `""` | Uptrace exporter DSN (optional) |

Version info (`APP_VERSION`, `APP_COMMIT`, `APP_COMMIT_SHORT`, `APP_BUILD_DATE`) is injected via ldflags at build time.

## API Endpoints

**Health probes:** `/healthz`, `/readyz`, `/livez`
**Chaos (CHAOS_ENABLED=true + token):** `/panic`, `PUT /readyz|livez/enable|disable`
**Failure signals (always on, harmless):** `/status/{code}`, `/delay/{seconds}` (capped), `/error/{level}`
**Observability:** `/metrics` (Prometheus)
**Debug:** `/env` (allowlist only), `/headers`, `/echo` (octet-stream)
**Auth:** `POST /auth/register`, `POST /auth/login`, `GET /auth/me`, `GET /token/validate` - no password-less token endpoint
**Docs:** `/openapi` (JSON spec), `/swagger/*` (Swagger UI)
**Profiling:** separate listener `PPROF_ADDR` when `PPROF_ENABLED=true` (`server.PprofHandler`)
**Info:** `/version` (incl. `chaos_enabled`), `/` (HTML landing page), `/configs`

**Security guards (tests in `pkg/server/security_test.go`):** `/env` returns only `publicEnvKeys` /
`FEATURE_*`; `TestRouteInventory` pins the full route list (chaos on and off) - a new route must be
reviewed and added there. Never print response bodies of /env in test failures (CI logs).

## CI/CD

The same checks run locally first: `.pre-commit-config.yaml` (gofmt, go vet, golangci-lint, gitleaks on
commit; go test -race and govulncheck on push), pinned to the versions in pr.yml - change them together.
Run them before pushing; do not leave a lint or test failure for CI to find.

- **pr.yml** — on every pull request (all must pass before merge): `go vet`, `go test -race`, `govulncheck` (pinned v1.8.0); golangci-lint v2.14.0 with gosec (`.golangci.yml`, every exclusion commented); `docker build` (no push); gitleaks v8.30.1 on the PR commits (`.gitleaks.toml`: default rules, NO allowlist; a planted test secret is allowed per line with an inline `gitleaks:allow` comment - a path allowlist with `condition = "AND"` let every secret in `_test.go` through)
- **build.yml** — on push to main/develop: govulncheck, build each published platform (linux/amd64, linux/arm64) locally and **Trivy-scan it before anything is pushed** (blocking on fixable CRITICAL/HIGH), then push exactly those scanned images and join them into one multi-platform index (`docker buildx imagetools create`, no second build), cosign sign + SBOM attestation on the index digest + SLSA build provenance (`actions/attest-build-provenance`, verify with `gh attestation verify`)
- **promote-production.yml** — manual: Trivy gate (blocking, CRITICAL only), re-tag staging image as production, create GitHub Release, bump version tag

## Coding Guidelines

- Keep the service simple — it's a reference/learning service, not a production product
- All config via environment variables — no config files
- Tests use `net/http/httptest` — no external test frameworks
- Prometheus metrics use a custom registry (not the global default)
- Middleware order matters: RequestID → Recoverer → OTel → Metrics → Logging → RandomBehavior
  (no RealIP: X-Forwarded-For is caller-controlled; no CORS: the browser calls the API same-origin via nginx)
- Version info is injected via ldflags — never hardcode versions
- Docker image runs as non-root user `app` (uid 10001)
- Trivy blocks in CI before push (build.yml, fixable CRITICAL/HIGH) and again at production promotion
- `http.Server` has ReadHeaderTimeout/ReadTimeout/WriteTimeout/IdleTimeout (cmd/api/main.go); keep WriteTimeout above DELAY_MAX_SECONDS
