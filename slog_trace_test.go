package insight

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// shipOne runs one record through a handler and returns the entry as it was
// delivered on the wire — the only place the correlation ids are visible, since
// Recent() keeps the human-readable line rather than the full entry.
func shipOne(t *testing.T, build func(*Shipper) slog.Handler, log func(*slog.Logger)) map[string]any {
	t.Helper()
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithBatchSize(1), WithFlushInterval(10*time.Millisecond))
	defer sh.Close()

	log(slog.New(build(sh)))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := rec.records(); len(got) > 0 {
			return got[len(got)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing was delivered")
	return nil
}

// The join is the product: a log line written inside a request must carry that
// request's trace without the caller attaching it by hand.
func TestSlogHandler_CorrelatesWithTheAmbientTrace(t *testing.T) {
	got := shipOne(t,
		func(sh *Shipper) slog.Handler {
			return NewSlogHandler(sh, slog.LevelInfo).WithTraceExtractor(
				func(context.Context) (string, string) {
					return "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
				})
		},
		func(l *slog.Logger) { l.InfoContext(context.Background(), "checkout failed") })

	if got["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || got["span_id"] != "00f067aa0ba902b7" {
		t.Fatalf("the record did not pick up the ambient span: %+v", got)
	}
}

// An id the caller wrote is a deliberate statement; the ambient span is a
// default. The explicit one has to win, or an application that already
// correlates by hand would be silently overridden.
func TestSlogHandler_ExplicitTraceIDWins(t *testing.T) {
	got := shipOne(t,
		func(sh *Shipper) slog.Handler {
			return NewSlogHandler(sh, slog.LevelInfo).WithTraceExtractor(
				func(context.Context) (string, string) { return "ambient", "ambientspan" })
		},
		func(l *slog.Logger) {
			l.InfoContext(context.Background(), "checkout failed", "trace_id", "explicit")
		})

	if got["trace_id"] != "explicit" {
		t.Fatalf("trace id = %v, want the explicit attribute to win", got["trace_id"])
	}
}

// No extractor configured is the behaviour every existing user has today, and
// it must not change.
func TestSlogHandler_WithoutAnExtractorNothingChanges(t *testing.T) {
	got := shipOne(t,
		func(sh *Shipper) slog.Handler { return NewSlogHandler(sh, slog.LevelInfo) },
		func(l *slog.Logger) { l.InfoContext(context.Background(), "plain") })

	if _, ok := got["trace_id"]; ok {
		t.Fatalf("expected no correlation ids, got %+v", got)
	}
}

// A context with no span returns empty ids, and that is not an error: plenty of
// log lines happen outside a request. The record still ships.
func TestSlogHandler_ShipsWhenThereIsNoSpan(t *testing.T) {
	got := shipOne(t,
		func(sh *Shipper) slog.Handler {
			return NewSlogHandler(sh, slog.LevelInfo).WithTraceExtractor(
				func(context.Context) (string, string) { return "", "" })
		},
		func(l *slog.Logger) { l.InfoContext(context.Background(), "startup complete") })

	if got["message"] != "startup complete" {
		t.Fatalf("the line must still ship: %+v", got)
	}
}
