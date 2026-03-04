package server

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func appendTraceFields(ctx context.Context, fields ...zap.Field) []zap.Field {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return fields
	}

	return append(fields,
		zap.String("trace_id", spanCtx.TraceID().String()),
		zap.String("span_id", spanCtx.SpanID().String()),
	)
}

func traceIDFromContext(ctx context.Context) string {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}
