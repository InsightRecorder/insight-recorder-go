package otel

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceIDs_ReturnsTheActiveSpan(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	gotTrace, gotSpan := TraceIDs(ctx)
	if gotTrace != "4bf92f3577b34da6a3ce929d0e0e4736" || gotSpan != "00f067aa0ba902b7" {
		t.Fatalf("TraceIDs = %q/%q", gotTrace, gotSpan)
	}
}

// An all-zero id is not a correlation key. Emitting it would group every
// uninstrumented line under one meaningless trace, which is worse than no id.
func TestTraceIDs_EmptyWithoutASpan(t *testing.T) {
	if tr, sp := TraceIDs(context.Background()); tr != "" || sp != "" {
		t.Fatalf("expected no ids outside a span, got %q/%q", tr, sp)
	}
}
