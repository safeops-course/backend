package logger

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/log/logtest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// A request log line carries trace_id and span_id but not the context itself, and the record sent to
// OpenTelemetry is linked to the same span.
func TestCtxLinksStdoutAndOpenTelemetryToTheSpan(t *testing.T) {
	recorder := logtest.NewRecorder()
	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(recorder)
	t.Cleanup(func() { global.SetLoggerProvider(previous) })

	core, stdout := observer.New(zap.DebugLevel)
	log := Wrap(zap.New(core))

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "request")
	defer span.End()
	log.Ctx(ctx).Info("request")

	entries := stdout.All()
	if len(entries) != 1 {
		t.Fatalf("stdout entries: %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if _, found := fields["context"]; found {
		t.Errorf("stdout line contains the context field: %v", fields)
	}
	if fields["trace_id"] != span.SpanContext().TraceID().String() || fields["span_id"] != span.SpanContext().SpanID().String() {
		t.Errorf("stdout trace_id/span_id = %v/%v, want the span's", fields["trace_id"], fields["span_id"])
	}

	var records []logtest.Record
	for _, scopeRecords := range recorder.Result() {
		records = append(records, scopeRecords...)
	}
	if len(records) != 1 {
		t.Fatalf("OpenTelemetry records: %d, want 1", len(records))
	}
	if got := trace.SpanContextFromContext(records[0].Context).TraceID(); got != span.SpanContext().TraceID() {
		t.Errorf("record trace id = %s, want %s", got, span.SpanContext().TraceID())
	}
}

// Debug lines stay on stdout; only Info and above go to OpenTelemetry.
func TestDebugIsNotSentToOpenTelemetry(t *testing.T) {
	recorder := logtest.NewRecorder()
	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(recorder)
	t.Cleanup(func() { global.SetLoggerProvider(previous) })

	core, stdout := observer.New(zap.DebugLevel)
	Wrap(zap.New(core)).Ctx(context.Background()).Debug("debug only")

	if stdout.Len() != 1 {
		t.Fatalf("stdout entries: %d, want 1", stdout.Len())
	}
	for _, scopeRecords := range recorder.Result() {
		if len(scopeRecords) != 0 {
			t.Fatalf("OpenTelemetry got %d debug records, want 0", len(scopeRecords))
		}
	}
}
