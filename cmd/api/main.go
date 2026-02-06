package main

import (
	"context"
	"net/http"
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
	ctx := context.Background()

	// Initialize OpenTelemetry with Uptrace
	shutdown := telemetry.Init(ctx)
	defer shutdown()

	cfg := config.Parse()
	log := logger.New()
	defer log.Sync()

	srv := server.New(cfg, log)

	httpServer := &http.Server{
		Addr:    cfg.Addr(),
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Ctx(ctx).Info("server starting", zap.String("addr", cfg.Addr()))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Ctx(ctx).Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	log.Ctx(ctx).Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Ctx(ctx).Fatal("graceful shutdown failed", zap.Error(err))
	}
	log.Ctx(ctx).Info("server stopped")
}
