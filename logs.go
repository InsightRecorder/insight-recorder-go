package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// LogEntry is one log record to ship. Recognized fields map to the canonical
// OpenTelemetry model server-side (level→severity, message→body, time→
// timestamp, trace_id→correlation); everything in Attrs becomes a searchable
// attribute.
type LogEntry struct {
	Time    time.Time
	Level   string // debug|info|warn|error|fatal
	Message string
	TraceID string
	SpanID  string
	Attrs   map[string]string
}

// MarshalJSON flattens the entry into the shape the ingest API recognizes.
// Reserved keys win over Attrs so a stray attribute cannot shadow the level
// or message.
func (e LogEntry) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Attrs)+5)
	for k, v := range e.Attrs {
		m[k] = v
	}
	if !e.Time.IsZero() {
		m["time"] = e.Time.UTC().Format(time.RFC3339Nano)
	}
	if e.Level != "" {
		m["level"] = e.Level
	}
	m["message"] = e.Message
	if e.TraceID != "" {
		m["trace_id"] = e.TraceID
	}
	if e.SpanID != "" {
		m["span_id"] = e.SpanID
	}
	return json.Marshal(m)
}

// IngestLogs ships a batch of entries synchronously and reports how many were
// stored. Most callers want [Client.NewShipper] instead, which batches in the
// background and never blocks the caller.
func (c *Client) IngestLogs(ctx context.Context, entries []LogEntry) (IngestResult, error) {
	var out IngestResult
	if len(entries) == 0 {
		return out, nil
	}
	payload, err := encodeNDJSON(entries)
	if err != nil {
		return out, err
	}
	body := rawBody{data: payload, contentType: "application/x-ndjson"}
	err = c.do(ctx, http.MethodPost, "/api/logs", body, &out)
	return out, err
}

// encodeNDJSON renders entries as newline-delimited JSON.
func encodeNDJSON(entries []LogEntry) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode appends a newline per record
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// Shipper defaults.
const (
	defaultBatchSize     = 100
	defaultQueueSize     = 4096
	defaultFlushInterval = 5 * time.Second
	defaultRecentLogs    = 50
	shipTimeout          = 30 * time.Second
)

// Shipper batches log entries and delivers them in the background. Send never
// blocks: when the queue is full, entries are dropped and counted by
// [Shipper.Dropped] — losing a log line must never stall the application that
// produced it.
//
// A Shipper must be closed to flush its final batch.
type Shipper struct {
	c        *Client
	ch       chan LogEntry
	flushReq chan chan error
	quit     chan struct{}
	done     chan struct{}

	batchSize int
	interval  time.Duration
	onError   func(error)

	dropped   atomic.Int64
	closeOnce sync.Once
	recent    *ringBuffer
}

// ShipperOption configures a Shipper.
type ShipperOption func(*Shipper)

// WithBatchSize sets how many entries accumulate before a delivery is
// triggered (default 100).
func WithBatchSize(n int) ShipperOption {
	return func(s *Shipper) {
		if n > 0 {
			s.batchSize = n
		}
	}
}

// WithFlushInterval sets how often a partial batch is delivered anyway
// (default 5s).
func WithFlushInterval(d time.Duration) ShipperOption {
	return func(s *Shipper) {
		if d > 0 {
			s.interval = d
		}
	}
}

// WithQueueSize sets the in-memory queue depth before entries are dropped
// (default 4096). Sizing it is a memory-vs-loss trade-off under bursts.
func WithQueueSize(n int) ShipperOption {
	return func(s *Shipper) {
		if n > 0 {
			s.ch = make(chan LogEntry, n)
		}
	}
}

// WithErrorHandler receives delivery failures. The default is silent: a
// logging SDK that logs its own failures through the application's logger
// invites infinite recursion.
func WithErrorHandler(fn func(error)) ShipperOption {
	return func(s *Shipper) {
		if fn != nil {
			s.onError = fn
		}
	}
}

// WithRecentLogs sets how many recent entries are kept in memory to attach to
// panic reports (default 50, 0 disables). See [Client.Recover].
func WithRecentLogs(n int) ShipperOption {
	return func(s *Shipper) { s.recent = newRingBuffer(n) }
}

// NewShipper starts a background log shipper. Close it on shutdown.
func (c *Client) NewShipper(opts ...ShipperOption) *Shipper {
	s := &Shipper{
		c:         c,
		ch:        make(chan LogEntry, defaultQueueSize),
		flushReq:  make(chan chan error),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
		batchSize: defaultBatchSize,
		interval:  defaultFlushInterval,
		onError:   func(error) {},
		recent:    newRingBuffer(defaultRecentLogs),
	}
	for _, opt := range opts {
		opt(s)
	}
	go s.run()
	return s
}

// Send queues an entry for delivery. It never blocks and never panics, even
// after Close (late entries are counted as dropped).
func (s *Shipper) Send(e LogEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	s.recent.add(e)
	select {
	case <-s.quit:
		s.dropped.Add(1)
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

// Dropped returns how many entries were discarded because the queue was full
// (or arrived after Close). A non-zero value means the queue or batch size is
// undersized for the application's log volume.
func (s *Shipper) Dropped() int64 { return s.dropped.Load() }

// Recent returns the log lines still held in memory, oldest first — the
// context attached to panic reports.
func (s *Shipper) Recent() []LogLine { return s.recent.lines() }

// Flush delivers everything queued so far and waits for the result.
func (s *Shipper) Flush(ctx context.Context) error {
	rc := make(chan error, 1)
	select {
	case <-s.done:
		return errors.New("insight: shipper is closed")
	case <-ctx.Done():
		return ctx.Err()
	case s.flushReq <- rc:
	}
	select {
	case err := <-rc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes pending entries and stops the background worker. It is safe
// to call more than once.
func (s *Shipper) Close() error {
	s.closeOnce.Do(func() { close(s.quit) })
	<-s.done
	return nil
}

// run is the background loop: accumulate, deliver on size or interval, and
// drain everything on shutdown.
func (s *Shipper) run() {
	defer close(s.done)

	batch := make([]LogEntry, 0, s.batchSize)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	drain := func() {
		for {
			select {
			case e := <-s.ch:
				batch = append(batch, e)
			default:
				return
			}
		}
	}
	deliver := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.post(batch)
		batch = batch[:0]
		if err != nil {
			s.onError(err)
		}
		return err
	}

	for {
		select {
		case e := <-s.ch:
			batch = append(batch, e)
			if len(batch) >= s.batchSize {
				_ = deliver()
			}
		case <-ticker.C:
			_ = deliver()
		case rc := <-s.flushReq:
			drain()
			rc <- deliver()
		case <-s.quit:
			drain()
			_ = deliver()
			return
		}
	}
}

// post delivers one batch with its own bounded context, so a hung server
// cannot wedge the worker forever.
func (s *Shipper) post(batch []LogEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), shipTimeout)
	defer cancel()
	res, err := s.c.IngestLogs(ctx, batch)
	if err != nil {
		return err
	}
	if res.Rejected > 0 {
		return &RejectedError{Rejected: res.Rejected, Accepted: res.Accepted}
	}
	return nil
}

// RejectedError reports that the server dead-lettered some records as
// unparseable. They are retained server-side for inspection, not lost.
type RejectedError struct {
	Accepted int
	Rejected int
}

func (e *RejectedError) Error() string {
	return "insight: " + itoa(e.Rejected) + " log record(s) rejected as unparseable (" +
		itoa(e.Accepted) + " accepted)"
}

// itoa avoids pulling strconv into this file's hot path formatting.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ringBuffer keeps the last N entries for panic context.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []LogLine
	next int
	full bool
}

func newRingBuffer(n int) *ringBuffer {
	if n <= 0 {
		return &ringBuffer{}
	}
	return &ringBuffer{buf: make([]LogLine, n)}
}

func (r *ringBuffer) add(e LogEntry) {
	if r == nil || len(r.buf) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = LogLine{Time: e.Time, Level: e.Level, Source: e.Attrs["source"], Message: e.Message}
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// lines returns the buffered entries oldest first.
func (r *ringBuffer) lines() []LogLine {
	if r == nil || len(r.buf) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]LogLine(nil), r.buf[:r.next]...)
	}
	out := make([]LogLine, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
