package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/logger"
	"github.com/ldbl/sre/backend/pkg/server"
	"github.com/ldbl/sre/backend/pkg/telemetry"
)

// @title           SRE Control Plane Backend API
// @version         1.0
// @description     Podinfo-inspired Go microservice for Kubernetes demos. Exposes health probes, chaos endpoints, metrics, and observability features.
// @termsOfService  http://swagger.io/terms/

// @contact.name   LDBL Team
// @contact.url    https://github.com/ldbl/backend

// @license.name  MIT
// @license.url   https://opensource.org/licenses/MIT

// @host      localhost:8080
// @BasePath  /
// @schemes   http https

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Type "Bearer" followed by a space and JWT token

func main() {
	// `backend migrate` applies the schema migrations and exits (the migrate initContainer).
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(runMigrate())
	}

	ctx := context.Background()

	// Initialize OpenTelemetry with Uptrace
	shutdown := telemetry.Init(ctx)
	defer shutdown()

	cfg := config.Parse()
	log := logger.New()
	defer log.Sync()

	srv := server.New(cfg, log)

	// Timeouts: without ReadHeaderTimeout a client can hold a connection open forever by sending
	// headers one byte at a time (Slowloris). WriteTimeout leaves room for the longest /delay.
	httpServer := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      time.Duration(cfg.DelayMaxSeconds*float64(time.Second)) + 15*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	// Profiling runs on its own listener (loopback by default) and only when PPROF_ENABLED=true.
	// No WriteTimeout: /debug/pprof/profile streams for ?seconds=N.
	var pprofServer *http.Server
	if cfg.PprofEnabled {
		pprofServer = &http.Server{
			Addr:              cfg.PprofAddr,
			Handler:           server.PprofHandler(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       5 * time.Second, // requests have no body; the long part is the response
			IdleTimeout:       60 * time.Second,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Ctx(ctx).Info("server starting", zap.String("addr", cfg.Addr()))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Ctx(ctx).Fatal("server error", zap.Error(err))
		}
	}()

	if pprofServer != nil {
		go func() {
			log.Ctx(ctx).Warn("pprof enabled", zap.String("addr", cfg.PprofAddr))
			if err := pprofServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Ctx(ctx).Fatal("pprof server error", zap.Error(err))
			}
		}()
	}

	<-ctx.Done()
	log.Ctx(ctx).Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if pprofServer != nil {
		if err := pprofServer.Shutdown(shutdownCtx); err != nil {
			log.Ctx(ctx).Error("pprof shutdown failed", zap.Error(err))
		}
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Fatal("graceful shutdown failed", zap.Error(err))
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Error("resource shutdown failed", zap.Error(err))
	}
	log.Ctx(ctx).Info("server stopped")
}
