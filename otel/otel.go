// Package otel connects the InsightRecorder SDK to an application already
// instrumented with OpenTelemetry: it lifts the active span's ids out of the
// context so every shipped log line carries the trace it happened inside.
//
// That join is the point of the product — a log line, the trace of the request
// that wrote it, and the session that triggered it are one investigation — and
// before this it only happened if the caller remembered to attach trace_id to
// every log call.
//
//	import (
//	    insight "github.com/InsightRecorder/insight-recorder-go"
//	    insightotel "github.com/InsightRecorder/insight-recorder-go/otel"
//	)
//
//	h := insight.NewSlogHandler(sh, slog.LevelInfo).
//	    WithTraceExtractor(insightotel.TraceIDs)
//	slog.SetDefault(slog.New(insight.MultiHandler(stdout, h)))
//
// From then on `slog.InfoContext(ctx, "…")` correlates by itself. The plain
// `slog.Info` (no context) does not, and cannot: there is no span to read.
//
// This is a separate module on purpose. The core SDK has zero dependencies, and
// an application that does not use OpenTelemetry must not be made to download it
// — the same reason the zerolog adapter lives beside it rather than inside.
package otel

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// TraceIDs returns the active span's trace and span ids as lowercase hex, or
// two empty strings when the context carries no recording span.
//
// It reports nothing for a non-recording span rather than emitting the
// all-zero id: a zero trace id is not a correlation key, and storing it would
// group every uninstrumented line under one meaningless trace.
func TraceIDs(ctx context.Context) (string, string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return "", ""
	}
	return sc.TraceID().String(), sc.SpanID().String()
}
