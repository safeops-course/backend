package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/telemetry"
)

// Server represents the HTTP API.
type Server struct {
	cfg       config.Config
	router    chi.Router
	logger    *log.Logger
	registry  *prometheus.Registry
	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	ready     atomic.Bool
	live      atomic.Bool
	randSrc   *rand.Rand
	randMu    sync.Mutex
	indexTmpl *template.Template
}

// New constructs a fully configured HTTP server.
func New(cfg config.Config, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	s := &Server{
		cfg:      cfg,
		logger:   logger,
		registry: prometheus.NewRegistry(),
		randSrc:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	s.ready.Store(true)
	s.live.Store(true)

	s.requests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "app",
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)
	s.duration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "app",
			Name:      "http_request_duration_seconds",
			Help:      "Duration of HTTP requests in seconds",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
	s.inFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "app",
		Name:      "http_in_flight_requests",
		Help:      "Current number of in-flight requests",
	})

	s.registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
		s.requests,
		s.duration,
		s.inFlight,
	)

	tmpl := template.Must(template.New("index").Parse(indexTemplate))
	s.indexTmpl = tmpl

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.corsMiddleware)
	r.Use(telemetry.HTTPMiddleware)
	r.Use(s.metricsMiddleware)
	r.Use(s.loggingMiddleware)
	r.Use(s.randomBehaviorMiddleware)

	r.Get("/", s.handleIndex)
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Put("/readyz/enable", s.handleReadyEnable)
	r.Put("/readyz/disable", s.handleReadyDisable)
	r.Get("/livez", s.handleLive)
	r.Put("/livez/enable", s.handleLiveEnable)
	r.Put("/livez/disable", s.handleLiveDisable)
	r.Get("/version", s.handleVersion)
	r.Get("/env", s.handleEnv)
	r.Get("/headers", s.handleHeaders)
	r.Post("/echo", s.handleEcho)
	r.Get("/status/{code}", s.handleStatus)
	r.Get("/delay/{seconds}", s.handleDelay)
	r.Get("/panic", s.handlePanic)
	r.Get("/metrics", s.handleMetrics)
	r.Get("/openapi", s.handleOpenAPI)
	r.Get("/swagger", s.handleSwagger)

	s.router = r
	return s
}

// Handler returns the HTTP handler for serving requests.
func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow CORS for frontend
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, traceparent, tracestate")
		w.Header().Set("Access-Control-Expose-Headers", "traceparent, tracestate")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.inFlight.Inc()
		defer s.inFlight.Dec()

		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		duration := time.Since(start).Seconds()
		path := routePattern(r)

		s.duration.WithLabelValues(r.Method, path).Observe(duration)
		s.requests.WithLabelValues(r.Method, path, strconv.Itoa(recorder.status)).Inc()
	})
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.logger.Printf("%s %s status=%d duration=%s", r.Method, r.URL.Path, recorder.status, time.Since(start))
	})
}

func (s *Server) randomBehaviorMiddleware(next http.Handler) http.Handler {
	if s.cfg.RandomDelayMax == 0 && s.cfg.RandomErrorRate <= 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.RandomDelayMax > 0 {
			delay := s.randomDelay()
			time.Sleep(delay)
		}

		if s.cfg.RandomErrorRate > 0 && s.randomFloat() < s.cfg.RandomErrorRate {
			http.Error(w, "random error injected", http.StatusInternalServerError)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) randomDelay() time.Duration {
	if s.cfg.RandomDelayMax <= 0 {
		return 0
	}
	s.randMu.Lock()
	defer s.randMu.Unlock()
	return time.Duration(s.randSrc.Intn(s.cfg.RandomDelayMax+1)) * time.Millisecond
}

func (s *Server) randomFloat() float64 {
	s.randMu.Lock()
	defer s.randMu.Unlock()
	return s.randSrc.Float64()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]string{
		"Message":     s.cfg.UIMessage,
		"Color":       s.cfg.UIColor,
		"Version":     s.cfg.Version,
		"Commit":      s.cfg.Commit,
		"CommitShort": s.cfg.CommitShort,
		"Build":       s.cfg.BuildDate,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.indexTmpl.Execute(w, data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleReadyEnable(w http.ResponseWriter, r *http.Request) {
	s.ready.Store(true)
	respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleReadyDisable(w http.ResponseWriter, r *http.Request) {
	s.ready.Store(false)
	respondJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if !s.live.Load() {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not live"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Server) handleLiveEnable(w http.ResponseWriter, r *http.Request) {
	s.live.Store(true)
	respondJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Server) handleLiveDisable(w http.ResponseWriter, r *http.Request) {
	s.live.Store(false)
	respondJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"version":      s.cfg.Version,
		"commit":       s.cfg.Commit,
		"commit_short": s.cfg.CommitShort,
		"build_time":   s.cfg.BuildDate,
	})
}

func (s *Server) handleEnv(w http.ResponseWriter, r *http.Request) {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	respondJSON(w, http.StatusOK, env)
}

func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	headers := make(map[string]string)
	for key, vals := range r.Header {
		headers[key] = strings.Join(vals, ",")
	}
	respondJSON(w, http.StatusOK, headers)
}

func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
	if len(body) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	codeParam := chi.URLParam(r, "code")
	code, err := strconv.Atoi(codeParam)
	if err != nil || code < 100 || code > 599 {
		http.Error(w, "invalid status code", http.StatusBadRequest)
		return
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte(fmt.Sprintf("status forced to %d\n", code)))
}

func (s *Server) handleDelay(w http.ResponseWriter, r *http.Request) {
	secondsParam := chi.URLParam(r, "seconds")
	delaySeconds, err := strconv.ParseFloat(secondsParam, 64)
	if err != nil || delaySeconds < 0 {
		http.Error(w, "invalid delay", http.StatusBadRequest)
		return
	}
	time.Sleep(time.Duration(delaySeconds * float64(time.Second)))
	respondJSON(w, http.StatusOK, map[string]string{"delay": secondsParam})
}

func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request) {
	go func() {
		time.Sleep(10 * time.Millisecond)
		s.logger.Println("/panic invoked, terminating process with exit code 255")
		os.Exit(255)
	}()
	respondJSON(w, http.StatusOK, map[string]string{"status": "terminating"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, s.openAPISpec())
}

func (s *Server) handleSwagger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerTemplate))
}

func (s *Server) openAPISpec() map[string]any {
	version := s.cfg.Version
	if version == "" {
		version = "dev"
	}
	commit := s.cfg.Commit
	if commit == "" {
		commit = "dev"
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":          "SRE Control Plane Backend",
			"version":        version,
			"description":    "Podinfo-inspired Go service exposing health probes, chaos endpoints, and metrics.",
			"x-commit":       commit,
			"x-commit-short": s.cfg.CommitShort,
			"x-build-date":   s.cfg.BuildDate,
		},
		"servers": []map[string]any{
			{"url": "http://localhost:8080"},
		},
		"paths": map[string]any{
			"/": map[string]any{
				"get": map[string]any{
					"summary":     "HTML landing page",
					"description": "Returns a static HTML page describing the service.",
					"responses": map[string]any{
						"200": map[string]any{"description": "Rendered HTML"},
					},
				},
			},
			"/healthz": map[string]any{
				"get": map[string]any{
					"summary": "Health check",
					"responses": map[string]any{
						"200": map[string]any{"description": "Service is healthy"},
					},
				},
			},
			"/readyz": map[string]any{
				"get": map[string]any{
					"summary": "Readiness probe",
					"responses": map[string]any{
						"200": map[string]any{"description": "Service ready"},
						"503": map[string]any{"description": "Service not ready"},
					},
				},
			},
			"/readyz/enable": map[string]any{
				"put": map[string]any{
					"summary":   "Enable readiness",
					"responses": map[string]any{"200": map[string]any{"description": "Readiness enabled"}},
				},
			},
			"/readyz/disable": map[string]any{
				"put": map[string]any{
					"summary":   "Disable readiness",
					"responses": map[string]any{"200": map[string]any{"description": "Readiness disabled"}},
				},
			},
			"/livez": map[string]any{
				"get": map[string]any{
					"summary": "Liveness probe",
					"responses": map[string]any{
						"200": map[string]any{"description": "Service live"},
						"503": map[string]any{"description": "Service not live"},
					},
				},
			},
			"/livez/enable": map[string]any{
				"put": map[string]any{
					"summary":   "Enable liveness",
					"responses": map[string]any{"200": map[string]any{"description": "Liveness enabled"}},
				},
			},
			"/livez/disable": map[string]any{
				"put": map[string]any{
					"summary":   "Disable liveness",
					"responses": map[string]any{"200": map[string]any{"description": "Liveness disabled"}},
				},
			},
			"/version": map[string]any{
				"get": map[string]any{
					"summary":   "Service version",
					"responses": map[string]any{"200": map[string]any{"description": "Version info"}},
				},
			},
			"/env": map[string]any{
				"get": map[string]any{
					"summary":   "Environment variables",
					"responses": map[string]any{"200": map[string]any{"description": "Environment map"}},
				},
			},
			"/headers": map[string]any{
				"get": map[string]any{
					"summary":   "Echo request headers",
					"responses": map[string]any{"200": map[string]any{"description": "Headers"}},
				},
			},
			"/echo": map[string]any{
				"post": map[string]any{
					"summary": "Echo request body",
					"requestBody": map[string]any{
						"description": "Arbitrary payload that will be echoed back",
						"required":    false,
					},
					"responses": map[string]any{
						"200": map[string]any{"description": "Payload echoed"},
						"204": map[string]any{"description": "Empty payload"},
					},
				},
			},
			"/status/{code}": map[string]any{
				"get": map[string]any{
					"summary": "Return arbitrary HTTP status",
					"parameters": []map[string]any{
						{
							"name":     "code",
							"in":       "path",
							"required": true,
							"schema":   map[string]any{"type": "integer", "minimum": 100, "maximum": 599},
						},
					},
					"responses": map[string]any{
						"default": map[string]any{"description": "Requested status returned"},
					},
				},
			},
			"/delay/{seconds}": map[string]any{
				"get": map[string]any{
					"summary": "Delay response by N seconds",
					"parameters": []map[string]any{
						{
							"name":     "seconds",
							"in":       "path",
							"required": true,
							"schema":   map[string]any{"type": "number", "minimum": 0},
						},
					},
					"responses": map[string]any{"200": map[string]any{"description": "Delay acknowledged"}},
				},
			},
			"/panic": map[string]any{
				"get": map[string]any{
					"summary":   "Terminate process with exit code 255",
					"responses": map[string]any{"200": map[string]any{"description": "Process termination initiated"}},
				},
			},
			"/metrics": map[string]any{
				"get": map[string]any{
					"summary":   "Prometheus metrics",
					"responses": map[string]any{"200": map[string]any{"description": "Prometheus text exposition"}},
				},
			},
			"/openapi": map[string]any{
				"get": map[string]any{
					"summary":   "OpenAPI specification",
					"responses": map[string]any{"200": map[string]any{"description": "OpenAPI document"}},
				},
			},
			"/swagger": map[string]any{
				"get": map[string]any{
					"summary":   "Swagger UI",
					"responses": map[string]any{"200": map[string]any{"description": "Swagger UI HTML"}},
				},
			},
		},
	}
}

// Serve launches the HTTP server on the configured port.
func (s *Server) Serve() error {
	srv := &http.Server{
		Addr:    s.cfg.Addr(),
		Handler: s.router,
	}
	s.logger.Printf("listening on %s", s.cfg.Addr())
	return srv.ListenAndServe()
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func routePattern(r *http.Request) string {
	if ctx := chi.RouteContext(r.Context()); ctx != nil {
		pattern := ctx.RoutePattern()
		if pattern != "" {
			return pattern
		}
	}
	return "unknown"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

const indexTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>SRE Control Plane</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;600&display=swap" rel="stylesheet">
  <style>
    :root {
      color-scheme: light dark;
      --accent: {{.Color}};
      --bg: #0f172a;
      --fg: #f8fafc;
      --card-bg: rgba(15, 23, 42, 0.85);
      --text: #e2e8f0;
      --muted: #94a3b8;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      display: flex;
      min-height: 100vh;
      font-family: 'Inter', Arial, sans-serif;
      background: radial-gradient(circle at top left, rgba(37, 99, 235, 0.35), rgba(15, 23, 42, 0.95));
      color: var(--fg);
      align-items: center;
      justify-content: center;
      padding: 24px;
    }
    .card {
      width: min(720px, 100%);
      background: var(--card-bg);
      border-radius: 18px;
      border: 1px solid rgba(148, 163, 184, 0.15);
      box-shadow: 0 40px 80px -20px rgba(15, 23, 42, 0.65);
      padding: 32px;
      backdrop-filter: blur(12px);
    }
    .badge {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      padding: 6px 12px;
      border-radius: 999px;
      background: rgba(148, 163, 184, 0.15);
      color: var(--muted);
      font-size: 0.8rem;
      font-weight: 500;
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }
    h1 {
      margin: 24px 0 12px;
      font-size: clamp(2rem, 3vw, 2.5rem);
      font-weight: 600;
    }
    p.lead {
      margin: 0 0 24px;
      color: var(--text);
      font-size: 1.05rem;
      line-height: 1.6;
    }
    .links {
      display: grid;
      gap: 12px;
      margin-top: 24px;
    }
    .link {
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 14px 18px;
      border-radius: 12px;
      background: rgba(148, 163, 184, 0.12);
      color: var(--fg);
      text-decoration: none;
      transition: transform 0.15s ease, background 0.15s ease;
    }
    .link:hover {
      background: rgba(148, 163, 184, 0.22);
      transform: translateY(-2px);
    }
    .link span {
      font-weight: 500;
    }
    .meta {
      margin-top: 28px;
      font-size: 0.85rem;
      color: var(--muted);
      display: flex;
      flex-wrap: wrap;
      gap: 12px;
    }
    .pill {
      padding: 4px 12px;
      border-radius: 999px;
      background: rgba(37, 99, 235, 0.18);
      color: var(--fg);
      font-size: 0.8rem;
      letter-spacing: 0.04em;
    }
  </style>
</head>
<body>
  <div class="card">
    <span class="badge">SRE Control Plane · Backend</span>
    <h1>{{.Message}}</h1>
    <p class="lead">
      This Go service powers the control-plane demos. Probe health, inject failures, or inspect telemetry endpoints before deploying to Kubernetes.
    </p>
    <div class="links">
      <a class="link" href="/swagger">
        <span>Swagger UI</span>
        <small>/swagger</small>
      </a>
      <a class="link" href="/metrics">
        <span>Prometheus Metrics</span>
        <small>/metrics</small>
      </a>
      <a class="link" href="/readyz">
        <span>Readiness Probe</span>
        <small>/readyz</small>
      </a>
      <a class="link" href="/env">
        <span>Runtime Environment</span>
        <small>/env</small>
      </a>
    </div>
    <div class="meta">
      <span class="pill">Version {{.Version}}</span>
      <span class="pill">Commit {{.CommitShort}}</span>
      <span class="pill">Color {{.Color}}</span>
      {{- if .Build }}<span class="pill">Built {{.Build}}</span>{{ end -}}
    </div>
  </div>
</body>
</html>`

const swaggerTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>SRE Control Plane API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css" />
  <style>
    body { margin: 0; background-color: #0f172a; }
    #swagger-ui { max-width: 960px; margin: 0 auto; padding: 24px; background: #ffffff; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      SwaggerUIBundle({
        url: '/openapi',
        dom_id: '#swagger-ui',
        presets: [SwaggerUIBundle.presets.apis],
        layout: 'BaseLayout'
      });
    };
  </script>
</body>
</html>`
