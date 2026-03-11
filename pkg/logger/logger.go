package logger

import (
	"os"

	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger wraps otelzap.Logger for OpenTelemetry-correlated logging
type Logger = otelzap.Logger

// New creates a new otelzap logger
func New() *Logger {
	env := os.Getenv("DEPLOYMENT_ENVIRONMENT")

	var zapLogger *zap.Logger
	var err error

	if env == "production" || env == "staging" {
		// Production: JSON format, info level
		config := zap.NewProductionConfig()
		config.EncoderConfig.TimeKey = "timestamp"
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		zapLogger, err = config.Build()
	} else {
		// Development: console format, debug level
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapLogger, err = config.Build()
	}

	if err != nil {
		// Fallback to example logger
		zapLogger = zap.NewExample()
	}

	return otelzap.New(zapLogger,
		otelzap.WithMinLevel(zapcore.InfoLevel),
	)
}

// Sugar returns a sugared logger for printf-style logging
func Sugar(l *Logger) *otelzap.SugaredLogger {
	return l.Sugar()
}
