package insight

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

// captureShipper collects entries without any network.
func captureShipper(t *testing.T) (*Shipper, func() []LogEntry) {
	t.Helper()
	var got []LogEntry
	s := &Shipper{
		ch:       make(chan LogEntry, 256),
		flushReq: make(chan chan error),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		recent:   newRingBuffer(defaultRecentLogs),
		onError:  func(error) {},
	}
	drain := func() []LogEntry {
		for {
			select {
			case e := <-s.ch:
				got = append(got, e)
			default:
				return got
			}
		}
	}
	return s, drain
}

func TestSlogHandler_ShipsRecords(t *testing.T) {
	sh, drain := captureShipper(t)
	logger := slog.New(NewSlogHandler(sh, slog.LevelInfo))

	logger.Info("checkout failed", "order_id", "1234", "trace_id", "abc", "span_id", "def")
	entries := drain()
	if len(entries) != 1 {
		t.Fatalf("shipped %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Message != "checkout failed" || e.Level != "info" {
		t.Errorf("entry = %+v", e)
	}
	if e.TraceID != "abc" || e.SpanID != "def" {
		t.Errorf("correlation ids not lifted: trace=%q span=%q", e.TraceID, e.SpanID)
	}
	if e.Attrs["order_id"] != "1234" {
		t.Errorf("attrs = %v", e.Attrs)
	}
	if _, dup := e.Attrs["trace_id"]; dup {
		t.Error("trace_id must not be duplicated into attrs")
	}
	if e.Time.IsZero() {
		t.Error("record time not carried")
	}
}

func TestSlogHandler_RespectsLevel(t *testing.T) {
	sh, drain := captureShipper(t)
	logger := slog.New(NewSlogHandler(sh, slog.LevelWarn))

	logger.Debug("nope")
	logger.Info("nope")
	logger.Warn("yes")
	logger.Error("yes")
	if got := len(drain()); got != 2 {
		t.Fatalf("shipped %d entries, want only warn+error", got)
	}
}

func TestSlogHandler_LevelNames(t *testing.T) {
	sh, drain := captureShipper(t)
	logger := slog.New(NewSlogHandler(sh, slog.LevelDebug))
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")

	want := []string{"debug", "info", "warn", "error"}
	entries := drain()
	if len(entries) != len(want) {
		t.Fatalf("got %d entries", len(entries))
	}
	for i, level := range want {
		if entries[i].Level != level {
			t.Errorf("entry %d level = %q, want %q", i, entries[i].Level, level)
		}
	}
}

func TestSlogHandler_WithAttrsAndGroup(t *testing.T) {
	sh, drain := captureShipper(t)
	base := slog.New(NewSlogHandler(sh, slog.LevelInfo))

	base.With("service", "checkout").WithGroup("http").Info("done", "status", 500)
	entries := drain()
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	attrs := entries[0].Attrs
	if attrs["service"] != "checkout" {
		t.Errorf("WithAttrs lost: %v", attrs)
	}
	if attrs["http.status"] != "500" {
		t.Errorf("WithGroup not applied (want http.status): %v", attrs)
	}
}

func TestSlogHandler_NilShipperIsInert(t *testing.T) {
	logger := slog.New(NewSlogHandler(nil, slog.LevelInfo))
	logger.Info("must not panic") // exercised for the absence of a panic
}

func TestMultiHandler_FansOut(t *testing.T) {
	sh, drain := captureShipper(t)
	var buf bytes.Buffer
	stdout := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	logger := slog.New(MultiHandler(stdout, NewSlogHandler(sh, slog.LevelInfo)))
	logger.Info("both places", "k", "v")

	if buf.Len() == 0 {
		t.Error("stdout handler received nothing")
	}
	if got := drain(); len(got) != 1 || got[0].Message != "both places" {
		t.Errorf("shipper received %+v", got)
	}
}

func TestMultiHandler_EnabledIsUnionOfChildren(t *testing.T) {
	var buf bytes.Buffer
	quiet := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})
	sh, _ := captureShipper(t)
	m := MultiHandler(quiet, NewSlogHandler(sh, slog.LevelDebug))

	if !m.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug must be enabled because one child accepts it")
	}
}

func TestRingBuffer_PartialAndWrapped(t *testing.T) {
	r := newRingBuffer(3)
	if got := r.lines(); len(got) != 0 {
		t.Errorf("empty ring returned %d lines", len(got))
	}
	r.add(LogEntry{Message: "a", Time: time.Now()})
	r.add(LogEntry{Message: "b", Time: time.Now()})
	if got := r.lines(); len(got) != 2 || got[0].Message != "a" {
		t.Errorf("partial ring = %+v", got)
	}
	r.add(LogEntry{Message: "c"})
	r.add(LogEntry{Message: "d"}) // wraps, evicting "a"
	got := r.lines()
	if len(got) != 3 || got[0].Message != "b" || got[2].Message != "d" {
		t.Errorf("wrapped ring = %+v", got)
	}
	// A zero-size ring is inert, never panics.
	newRingBuffer(0).add(LogEntry{Message: "x"})
	if got := newRingBuffer(0).lines(); got != nil {
		t.Errorf("zero ring returned %v", got)
	}
}
