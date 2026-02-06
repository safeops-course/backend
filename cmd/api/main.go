package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ldbl/sre/backend/pkg/config"
	"github.com/ldbl/sre/backend/pkg/server"
	"github.com/ldbl/sre/backend/pkg/telemetry"
)

func main() {
	ctx := context.Background()

	// Initialize OpenTelemetry with Uptrace
	shutdown := telemetry.Init(ctx)
	defer shutdown()

	cfg := config.Parse()
	logger := log.New(os.Stdout, "backend ", log.LstdFlags)
	srv := server.New(cfg, logger)

	httpServer := &http.Server{
		Addr:    cfg.Addr(),
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Printf("listening on %s", cfg.Addr())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Println("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Fatalf("graceful shutdown failed: %v", err)
	}
	logger.Println("server stopped")
}
