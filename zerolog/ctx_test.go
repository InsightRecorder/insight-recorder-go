package zerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/rs/zerolog"
)

func TestCtx_AttachesTheTrace(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(&buf)

	traced := Ctx(context.Background(), l, func(context.Context) (string, string) {
		return "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	})
	traced.Error().Msg("checkout failed")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("event is not JSON: %v", err)
	}
	if got["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || got["span_id"] != "00f067aa0ba902b7" {
		t.Fatalf("event did not carry the trace: %+v", got)
	}
}

// Outside a span the line still has to ship — plenty of logging happens with no
// request in flight, and a helper that swallowed those would be a trap.
func TestCtx_WithoutASpanTheLineStillShips(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(&buf)

	plain := Ctx(context.Background(), l, func(context.Context) (string, string) { return "", "" })
	plain.Info().Msg("startup complete")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("event is not JSON: %v", err)
	}
	if got["message"] != "startup complete" {
		t.Fatalf("message missing: %+v", got)
	}
	if _, ok := got["trace_id"]; ok {
		t.Error("no span means no trace_id, not an empty one")
	}
}

func TestCtx_NilExtractorIsANoop(t *testing.T) {
	var buf bytes.Buffer
	l := Ctx(context.Background(), zerolog.New(&buf), nil)
	l.Info().Msg("ok")
	if buf.Len() == 0 {
		t.Fatal("the logger must still work without an extractor")
	}
}
