package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
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
	httpSwagger "github.com/swaggo/http-swagger/v2"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/configwatch"
	"github.com/ldbl/sre/backend/pkg/telemetry"

	_ "github.com/ldbl/sre/backend/pkg/api/docs" // swagger docs
)

const maxRequestBodyBytes int64 = 1 << 20

// Server represents the HTTP API.
type Server struct {
	cfg           config.Config
	router        chi.Router
	logger        *otelzap.Logger
	registry      *prometheus.Registry
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	inFlight      prometheus.Gauge
	ready         atomic.Bool
	live          atomic.Bool
	randSrc       *rand.Rand
	randMu        sync.Mutex
	indexTmpl     *template.Template
	configWatcher *configwatch.Watcher
	users         authUserStore
}

// New constructs a fully configured HTTP server.
func New(cfg config.Config, logger *otelzap.Logger) *Server {
	if logger == nil {
		logger = otelzap.New(zap.NewExample())
	}

	s := &Server{
		cfg:      cfg,
		logger:   logger,
		registry: prometheus.NewRegistry(),
		randSrc:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	s.ready.Store(true)
	s.live.Store(true)

	if strings.TrimSpace(cfg.JWTSecret) == "" {
		logger.Fatal("JWT_SECRET must be set")
	}

	if strings.TrimSpace(cfg.DatabaseURL) != "" {
		users, err := newPostgresUserStore(cfg.DatabaseURL)
		if err != nil {
			logger.Fatal("failed to initialize postgres auth store", zap.Error(err))
		}
		s.users = users
		logger.Info("initialized auth store", zap.String("backend", "postgres"))
	} else {
		authStorePath := strings.TrimSpace(cfg.AuthDBPath)
		if authStorePath == "" {
			authStorePath = "/tmp/users.json"
		}
		if !filepath.IsAbs(authStorePath) {
			rewrittenPath := filepath.Clean(filepath.Join("/tmp", authStorePath))
			relPath, relErr := filepath.Rel("/tmp", rewrittenPath)
			if relErr != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
				safeName := filepath.Base(authStorePath)
				if safeName == "." || safeName == ".." || safeName == string(filepath.Separator) || safeName == "" {
					safeName = "users.json"
				}
				rewrittenPath = filepath.Join("/tmp", safeName)
			}
			logger.Warn("AUTH_DB_PATH is relative; rewriting to writable /tmp path",
				zap.String("original_path", authStorePath),
				zap.String("rewritten_path", rewrittenPath),
			)
			authStorePath = rewrittenPath
		}

		users, err := newFileUserStore(authStorePath)
		if err != nil {
			logger.Fatal("failed to initialize file auth store", zap.Error(err), zap.String("path", authStorePath))
		}
		s.users = users
		logger.Warn("DATABASE_URL not set, using file-based auth store", zap.String("path", authStorePath))
	}

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

	// Initialize config watcher if config path is set
	if cfg.ConfigPath != "" {
		watcher, err := configwatch.NewWatcher(cfg.ConfigPath, logger)
		if err != nil {
			logger.Warn("failed to initialize config watcher", zap.Error(err), zap.String("path", cfg.ConfigPath))
		} else {
			s.configWatcher = watcher
			// Register callback for config changes
			watcher.OnChange(func(key, value string) {
				logger.Info("config changed", zap.String("key", key), zap.String("value", value))
			})
			watcher.Watch()
		}
	}

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
	r.Get("/livez", s.handleLive)
	r.Get("/version", s.handleVersion)
	r.Get("/env", s.handleEnv)
	r.Get("/headers", s.handleHeaders)
	r.Post("/echo", s.handleEcho)
	r.Get("/status/{code}", s.handleStatus)
	r.Get("/delay/{seconds}", s.handleDelay)
	r.Get("/error", s.handleError)
	r.Get("/error/{level}", s.handleError)
	r.Get("/metrics", s.handleMetrics)
	r.Get("/openapi", s.handleOpenAPI)
	r.Get("/swagger/*", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))
	r.Get("/configs", s.handleConfigs)
	r.Post("/token", s.handleTokenGenerate)
	r.Get("/token/validate", s.handleTokenValidate)
	r.Post("/auth/register", s.handleAuthRegister)
	r.Post("/auth/login", s.handleAuthLogin)
	r.With(s.authMiddleware).Get("/auth/me", s.handleAuthMe)

	r.Group(func(protected chi.Router) {
		protected.Use(s.authMiddleware)
		protected.Put("/readyz/enable", s.handleReadyEnable)
		protected.Put("/readyz/disable", s.handleReadyDisable)
		protected.Put("/livez/enable", s.handleLiveEnable)
		protected.Put("/livez/disable", s.handleLiveDisable)
		protected.Get("/panic", s.handlePanic)
	})

	// pprof endpoints for profiling
	r.HandleFunc("/debug/pprof/", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)
	r.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	r.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	r.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	r.Handle("/debug/pprof/block", pprof.Handler("block"))
	r.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	r.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))

	s.router = r
	return s
}

// Handler returns the HTTP handler for serving requests.
func (s *Server) Handler() http.Handler {
	return s.router
}

// Shutdown releases server resources (watchers, stores).
func (s *Server) Shutdown(ctx context.Context) error {
	_ = ctx

	var shutdownErr error
	if s.configWatcher != nil {
		if err := s.configWatcher.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	if s.users != nil {
		if err := s.users.Close(); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}

	return shutdownErr
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow CORS for frontend
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, traceparent, tracestate, baggage")
		w.Header().Set("Access-Control-Expose-Headers", "traceparent, tracestate, baggage")

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

		// Skip logging for health checks to reduce noise
		switch r.URL.Path {
		case "/healthz", "/livez", "/readyz", "/metrics":
			return
		}

		fields := []zap.Field{
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", recorder.status),
			zap.Duration("duration", time.Since(start)),
		}

		// Use context for trace correlation
		s.logger.Ctx(r.Context()).Info("request", appendTraceFields(r.Context(), fields...)...)
		telemetry.AddEvent(r.Context(), "request.log",
			attribute.String("http.method", r.Method),
			attribute.String("http.path", r.URL.Path),
			attribute.Int("http.status_code", recorder.status),
			attribute.Int64("http.duration_ms", time.Since(start).Milliseconds()),
		)
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

// handleIndex godoc
// @Summary      Landing page
// @Description  Returns HTML landing page with service information
// @Tags         General
// @Produce      html
// @Success      200  {string}  string  "HTML page"
// @Router       / [get]
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

// handleHealth godoc
// @Summary      Health check
// @Description  Returns service health status
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /healthz [get]
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady godoc
// @Summary      Readiness probe
// @Description  Returns readiness status for Kubernetes
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Failure      503  {object}  StatusResponse
// @Router       /readyz [get]
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleReadyEnable godoc
// @Summary      Enable readiness
// @Description  Sets the service as ready
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /readyz/enable [put]
func (s *Server) handleReadyEnable(w http.ResponseWriter, r *http.Request) {
	s.ready.Store(true)
	respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleReadyDisable godoc
// @Summary      Disable readiness
// @Description  Sets the service as not ready (for graceful shutdown testing)
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /readyz/disable [put]
func (s *Server) handleReadyDisable(w http.ResponseWriter, r *http.Request) {
	s.ready.Store(false)
	respondJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleLive godoc
// @Summary      Liveness probe
// @Description  Returns liveness status for Kubernetes
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Failure      503  {object}  StatusResponse
// @Router       /livez [get]
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if !s.live.Load() {
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not live"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

// handleLiveEnable godoc
// @Summary      Enable liveness
// @Description  Sets the service as live
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /livez/enable [put]
func (s *Server) handleLiveEnable(w http.ResponseWriter, r *http.Request) {
	s.live.Store(true)
	respondJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

// handleLiveDisable godoc
// @Summary      Disable liveness
// @Description  Sets the service as not live (triggers container restart)
// @Tags         Health
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /livez/disable [put]
func (s *Server) handleLiveDisable(w http.ResponseWriter, r *http.Request) {
	s.live.Store(false)
	respondJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// handleVersion godoc
// @Summary      Version info
// @Description  Returns service version, commit, and build information
// @Tags         General
// @Produce      json
// @Success      200  {object}  VersionResponse
// @Router       /version [get]
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"version":      s.cfg.Version,
		"commit":       s.cfg.Commit,
		"commit_short": s.cfg.CommitShort,
		"build_time":   s.cfg.BuildDate,
	})
}

// handleEnv godoc
// @Summary      Environment variables
// @Description  Returns all environment variables (use with caution in production)
// @Tags         Debug
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /env [get]
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

// handleHeaders godoc
// @Summary      Request headers
// @Description  Returns all HTTP request headers
// @Tags         Debug
// @Produce      json
// @Success      200  {object}  map[string]string
// @Router       /headers [get]
func (s *Server) handleHeaders(w http.ResponseWriter, r *http.Request) {
	headers := make(map[string]string)
	for key, vals := range r.Header {
		headers[key] = strings.Join(vals, ",")
	}
	respondJSON(w, http.StatusOK, headers)
}

// handleEcho godoc
// @Summary      Echo request body
// @Description  Returns the request body as-is
// @Tags         Debug
// @Accept       json
// @Produce      json
// @Param        body  body  string  false  "Request body to echo"
// @Success      200  {string}  string  "Echoed body"
// @Success      204  "Empty body"
// @Failure      400  {string}  string  "Bad request"
// @Router       /echo [post]
func (s *Server) handleEcho(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
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

// handleStatus godoc
// @Summary      Return specific HTTP status
// @Description  Returns the specified HTTP status code (for testing)
// @Tags         Chaos
// @Produce      plain
// @Param        code  path  int  true  "HTTP status code (100-599)"
// @Success      200  {string}  string  "Status message"
// @Failure      400  {string}  string  "Invalid status code"
// @Router       /status/{code} [get]
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

// handleDelay godoc
// @Summary      Delay response
// @Description  Delays the response by the specified number of seconds
// @Tags         Chaos
// @Produce      json
// @Param        seconds  path  number  true  "Delay in seconds"
// @Success      200  {object}  DelayResponse
// @Failure      400  {string}  string  "Invalid delay"
// @Router       /delay/{seconds} [get]
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

// handlePanic godoc
// @Summary      Terminate process
// @Description  Terminates the process with exit code 255 (for testing pod restarts)
// @Tags         Chaos
// @Produce      json
// @Success      200  {object}  StatusResponse
// @Router       /panic [get]
func (s *Server) handlePanic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	panicErr := errors.New("panic endpoint invoked")
	telemetry.RecordError(ctx, panicErr)

	traceID := traceIDFromContext(ctx)

	s.logger.Ctx(ctx).Error("/panic invoked, terminating process with exit code 255", appendTraceFields(ctx,
		zap.Error(panicErr),
	)...)
	telemetry.AddEvent(ctx, "panic.log",
		attribute.String("event.name", "panic_termination"),
		attribute.String("trace_id", traceID),
	)

	go func() {
		time.Sleep(50 * time.Millisecond)
		os.Exit(255)
	}()
	respondJSON(w, http.StatusOK, map[string]string{
		"status":   "terminating",
		"trace_id": traceID,
	})
}

// handleError godoc
// @Summary      Generate test logs
// @Description  Generates log entries at different levels for testing observability
// @Tags         Observability
// @Produce      json
// @Param        level  path  string  false  "Log level: debug, info, warn, error"  default(error)
// @Success      200  {object}  ErrorResponse
// @Success      500  {object}  ErrorResponse  "Error level generates 500"
// @Failure      400  {string}  string  "Invalid level"
// @Router       /error [get]
// @Router       /error/{level} [get]
func (s *Server) handleError(w http.ResponseWriter, r *http.Request) {
	level := chi.URLParam(r, "level")
	if level == "" {
		level = "error"
	}

	testErr := errors.New("test error for observability verification")
	ctx := r.Context()

	switch level {
	case "debug":
		s.logger.Ctx(ctx).Debug("debug level test message", appendTraceFields(ctx,
			zap.String("endpoint", "/error/debug"),
			zap.String("request_id", middleware.GetReqID(ctx)),
		)...)
		respondJSON(w, http.StatusOK, map[string]string{
			"level":   "debug",
			"message": "debug log generated",
		})
	case "info":
		s.logger.Ctx(ctx).Info("info level test message", appendTraceFields(ctx,
			zap.String("endpoint", "/error/info"),
			zap.String("request_id", middleware.GetReqID(ctx)),
		)...)
		respondJSON(w, http.StatusOK, map[string]string{
			"level":   "info",
			"message": "info log generated",
		})
	case "warn":
		s.logger.Ctx(ctx).Warn("warning level test message", appendTraceFields(ctx,
			zap.String("endpoint", "/error/warn"),
			zap.String("request_id", middleware.GetReqID(ctx)),
		)...)
		respondJSON(w, http.StatusOK, map[string]string{
			"level":   "warn",
			"message": "warning log generated",
		})
	case "error":
		// Record error in span for tracing
		telemetry.RecordError(ctx, testErr)
		s.logger.Ctx(ctx).Error("error level test message", appendTraceFields(ctx,
			zap.Error(testErr),
			zap.String("endpoint", "/error/error"),
			zap.String("request_id", middleware.GetReqID(ctx)),
		)...)
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"level":   "error",
			"message": "error log generated",
			"error":   testErr.Error(),
		})
	default:
		http.Error(w, "invalid level: use debug, info, warn, or error", http.StatusBadRequest)
	}
}

// handleMetrics godoc
// @Summary      Prometheus metrics
// @Description  Returns Prometheus metrics in text exposition format
// @Tags         Observability
// @Produce      plain
// @Success      200  {string}  string  "Prometheus metrics"
// @Router       /metrics [get]
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}).ServeHTTP(w, r)
}

// handleOpenAPI godoc
// @Summary      OpenAPI specification
// @Description  Returns OpenAPI 3.0 specification in JSON format
// @Tags         Documentation
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Router       /openapi [get]
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, s.openAPISpec())
}

// handleConfigs godoc
// @Summary      Config watcher values
// @Description  Returns values from watched ConfigMaps/Secrets
// @Tags         Config
// @Produce      json
// @Success      200  {object}  ConfigsResponse
// @Router       /configs [get]
func (s *Server) handleConfigs(w http.ResponseWriter, r *http.Request) {
	if s.configWatcher == nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"enabled": false,
			"message": "config watcher not configured (set CONFIG_PATH env var)",
		})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"path":    s.cfg.ConfigPath,
		"configs": s.configWatcher.GetAll(),
	})
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
	s.logger.Info("listening", zap.String("addr", s.cfg.Addr()))
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
