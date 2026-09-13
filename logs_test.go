package insight

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// batchRecorder captures every NDJSON batch the shipper delivers.
type batchRecorder struct {
	mu      sync.Mutex
	batches [][]map[string]any
	status  int
	srv     *httptest.Server
}

func newBatchRecorder(t *testing.T) *batchRecorder {
	t.Helper()
	rec := &batchRecorder{status: http.StatusOK}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []map[string]any
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("invalid NDJSON line: %v", err)
				continue
			}
			batch = append(batch, m)
		}
		rec.mu.Lock()
		if rec.status == http.StatusOK && len(batch) > 0 {
			rec.batches = append(rec.batches, batch)
		}
		status := rec.status
		rec.mu.Unlock()

		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"accepted":` + itoa(len(batch)) + `,"rejected":0}`))
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// records returns every entry delivered so far, flattened.
func (b *batchRecorder) records() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, batch := range b.batches {
		out = append(out, batch...)
	}
	return out
}

func (b *batchRecorder) batchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

func TestLogEntry_MarshalsRecognizedKeys(t *testing.T) {
	e := LogEntry{
		Time:    time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC),
		Level:   "error",
		Message: "checkout failed",
		TraceID: "abc123",
		SpanID:  "span1",
		Attrs:   map[string]string{"order_id": "1234", "level": "IGNORED"},
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["level"] != "error" {
		t.Errorf("reserved key must win over Attrs: level = %v", m["level"])
	}
	if m["message"] != "checkout failed" || m["trace_id"] != "abc123" || m["span_id"] != "span1" {
		t.Errorf("recognized keys wrong: %v", m)
	}
	if m["order_id"] != "1234" {
		t.Errorf("attribute lost: %v", m)
	}
	if m["time"] != "2026-09-07T12:00:00Z" {
		t.Errorf("time = %v", m["time"])
	}
}

func TestShipper_BatchesBySize(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithBatchSize(5), WithFlushInterval(time.Hour)) // size triggers, not the timer
	defer sh.Close()

	for i := 0; i < 10; i++ {
		sh.Send(LogEntry{Level: "info", Message: "line"})
	}
	// No Flush: size alone must trigger delivery (a Flush would race the
	// worker and legitimately coalesce everything into one batch).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(rec.records()) < 10 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(rec.records()); got != 10 {
		t.Fatalf("delivered %d records, want 10", got)
	}
	if got := rec.batchCount(); got != 2 {
		t.Errorf("batchSize 5 over 10 records should deliver 2 batches, got %d", got)
	}
}

func TestShipper_FlushDeliversEverythingPending(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithBatchSize(1000), WithFlushInterval(time.Hour))
	defer sh.Close()

	for i := 0; i < 10; i++ {
		sh.Send(LogEntry{Level: "info", Message: "line"})
	}
	if err := sh.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := len(rec.records()); got != 10 {
		t.Fatalf("Flush delivered %d records, want all 10", got)
	}
}

func TestShipper_FlushesOnInterval(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithBatchSize(1000), WithFlushInterval(20*time.Millisecond))
	defer sh.Close()

	sh.Send(LogEntry{Level: "info", Message: "solo"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(rec.records()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(rec.records()); got != 1 {
		t.Fatalf("interval flush delivered %d records, want 1", got)
	}
}

func TestShipper_CloseFlushesPending(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithBatchSize(1000), WithFlushInterval(time.Hour))

	for i := 0; i < 7; i++ {
		sh.Send(LogEntry{Level: "warn", Message: "pending"})
	}
	if err := sh.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(rec.records()); got != 7 {
		t.Fatalf("Close delivered %d records, want 7 — pending logs must not be lost", got)
	}
	// Close is idempotent and Send after Close must not panic.
	_ = sh.Close()
	sh.Send(LogEntry{Message: "late"})
}

func TestShipper_DropsInsteadOfBlocking(t *testing.T) {
	// A server that never responds would block delivery; the queue must fill
	// and Send must still return immediately.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(block)

	c := testClient(t, srv)
	sh := c.NewShipper(WithBatchSize(1), WithQueueSize(4), WithFlushInterval(time.Hour))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			sh.Send(LogEntry{Message: "flood"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked: a full queue must drop, never stall the caller")
	}
	if sh.Dropped() == 0 {
		t.Error("expected dropped entries with a stalled server and a small queue")
	}
}

func TestShipper_ReportsDeliveryErrors(t *testing.T) {
	rec := newBatchRecorder(t)
	rec.mu.Lock()
	rec.status = http.StatusUnauthorized
	rec.mu.Unlock()

	c := testClient(t, rec.srv, WithMaxRetries(0))
	errCh := make(chan error, 1)
	sh := c.NewShipper(WithBatchSize(1), WithFlushInterval(time.Hour), WithErrorHandler(func(err error) {
		select {
		case errCh <- err:
		default:
		}
	}))
	defer sh.Close()

	sh.Send(LogEntry{Message: "will fail"})
	select {
	case err := <-errCh:
		if !IsUnauthorized(err) {
			t.Errorf("error not classified: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delivery failure was never reported")
	}
}

func TestShipper_RecentLogsRing(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithRecentLogs(3), WithBatchSize(1000), WithFlushInterval(time.Hour))
	defer sh.Close()

	for _, msg := range []string{"one", "two", "three", "four"} {
		sh.Send(LogEntry{Level: "info", Message: msg})
	}
	lines := sh.Recent()
	if len(lines) != 3 {
		t.Fatalf("ring kept %d lines, want 3", len(lines))
	}
	if lines[0].Message != "two" || lines[2].Message != "four" {
		t.Errorf("ring order wrong (want oldest-first two..four): %+v", lines)
	}
}

func TestIngestLogs_EmptyIsNoop(t *testing.T) {
	rec := newBatchRecorder(t)
	c := testClient(t, rec.srv)
	res, err := c.IngestLogs(context.Background(), nil)
	if err != nil || res.Accepted != 0 {
		t.Fatalf("empty ingest = (%+v, %v)", res, err)
	}
	if rec.batchCount() != 0 {
		t.Error("empty ingest must not hit the network")
	}
}
