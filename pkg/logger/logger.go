// Package logger is zap plus OpenTelemetry. Every record goes to stdout and, through the official
// OpenTelemetry zap bridge, to the global LoggerProvider - which telemetry.Init points at Uptrace when
// UPTRACE_DSN is set (otherwise it is a no-op). Ctx(ctx) ties a record to the request's span.
package logger

import (
	"context"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// scopeName names the instrumentation scope of the records sent to OpenTelemetry.
const scopeName = "github.com/ldbl/sre/backend"

// Logger is a zap.Logger whose records also reach OpenTelemetry.
type Logger struct {
	*zap.Logger
}

// New builds the service logger: JSON in production and staging, console elsewhere; records at Info
// and above also go to OpenTelemetry.
func New() *Logger {
	env := os.Getenv("DEPLOYMENT_ENVIRONMENT")

	var config zap.Config
	if env == "production" || env == "staging" {
		config = zap.NewProductionConfig()
		config.EncoderConfig.TimeKey = "timestamp"
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	} else {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	zapLogger, err := config.Build()
	if err != nil {
		zapLogger = zap.NewExample()
	}
	return Wrap(zapLogger)
}

// Wrap adds the OpenTelemetry bridge to an existing zap logger (tests use zap.NewExample or NewNop).
func Wrap(zapLogger *zap.Logger) *Logger {
	bridge := zapcore.Core(otelzap.NewCore(scopeName, otelzap.WithLoggerProvider(global.GetLoggerProvider())))
	// Debug stays local: only Info and above are worth sending.
	bridge, err := zapcore.NewIncreaseLevelCore(bridge, zapcore.InfoLevel)
	if err != nil {
		// Only fails when the level is lower than the core's; keep stdout and say so.
		zapLogger.Warn("OpenTelemetry log bridge disabled", zap.Error(err))
		return &Logger{Logger: zapLogger}
	}
	return &Logger{Logger: zapLogger.WithOptions(zap.WrapCore(func(stdout zapcore.Core) zapcore.Core {
		return zapcore.NewTee(withoutContext{stdout}, bridge)
	}))}
}

// Ctx returns a logger for one request: the OpenTelemetry bridge links its records to the span in
// ctx, and the stdout line carries trace_id and span_id, so a line found with kubectl logs leads to
// its trace.
func (l *Logger) Ctx(ctx context.Context) *zap.Logger {
	fields := []zap.Field{zap.Any("context", ctx)} // read by the bridge, dropped from stdout
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		fields = append(fields,
			zap.String("trace_id", sc.TraceID().String()),
			zap.String("span_id", sc.SpanID().String()),
		)
	}
	return l.With(fields...)
}

// withoutContext drops context.Context fields before they reach stdout: the bridge needs them, a
// log line does not.
type withoutContext struct {
	zapcore.Core
}

func (c withoutContext) With(fields []zapcore.Field) zapcore.Core {
	return withoutContext{c.Core.With(dropContext(fields))}
}

func (c withoutContext) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c withoutContext) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	return c.Core.Write(entry, dropContext(fields))
}

func dropContext(fields []zapcore.Field) []zapcore.Field {
	kept := make([]zapcore.Field, 0, len(fields))
	for _, f := range fields {
		if _, isContext := f.Interface.(context.Context); isContext {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}
