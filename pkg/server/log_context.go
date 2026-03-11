package server

import (
	"context"

	oteltrace "go.opentelemetry.io/otel/trace"
)

func traceIDFromContext(ctx context.Context) string {
	spanCtx := oteltrace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}
