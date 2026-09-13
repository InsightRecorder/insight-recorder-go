package insight

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bugByIDServer answers any /api/bugs/{id}{,/send,/summarize} call with a bug,
// so the CRUD wrappers can be exercised end to end.
func bugByIDServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"0192","ref":"BUG-1","title":"t","status":"Open"}`))
	}))
}

func TestBugCRUD_HappyPaths(t *testing.T) {
	srv := bugByIDServer(t)
	defer srv.Close()
	c := testClient(t, srv)
	ctx := context.Background()

	if _, err := c.GetBug(ctx, "0192"); err != nil {
		t.Fatalf("GetBug: %v", err)
	}
	if _, err := c.UpdateBug(ctx, "0192", "Fixed", "alice"); err != nil {
		t.Fatalf("UpdateBug: %v", err)
	}
	if _, err := c.SendBug(ctx, "0192", "github", IdempotencyKey("k1")); err != nil {
		t.Fatalf("SendBug: %v", err)
	}
	if _, err := c.SummarizeBug(ctx, "0192"); err != nil {
		t.Fatalf("SummarizeBug: %v", err)
	}
	if err := c.DeleteBug(ctx, "0192"); err != nil {
		t.Fatalf("DeleteBug: %v", err)
	}
}

func TestBugCRUD_ValidatesRequiredArgs(t *testing.T) {
	c := testClient(t, bugByIDServer(t))
	ctx := context.Background()
	checks := []struct {
		name string
		err  error
	}{
		{"GetBug empty id", mustErr(func() error { _, e := c.GetBug(ctx, ""); return e })},
		{"UpdateBug empty id", mustErr(func() error { _, e := c.UpdateBug(ctx, "", "x", "y"); return e })},
		{"DeleteBug empty id", c.DeleteBug(ctx, "")},
		{"SendBug empty id", mustErr(func() error { _, e := c.SendBug(ctx, "", "github"); return e })},
		{"SendBug empty dest", mustErr(func() error { _, e := c.SendBug(ctx, "id", ""); return e })},
		{"SummarizeBug empty id", mustErr(func() error { _, e := c.SummarizeBug(ctx, ""); return e })},
	}
	for _, tc := range checks {
		if tc.err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
		}
	}
}

func mustErr(fn func() error) error { return fn() }

func TestBugCRUD_PropagatesServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := testClient(t, srv, WithMaxRetries(0))
	ctx := context.Background()

	if _, err := c.GetBug(ctx, "id"); !IsUnavailable(err) {
		t.Errorf("GetBug: want unavailable, got %v", err)
	}
	if _, err := c.UpdateBug(ctx, "id", "s", "o"); err == nil {
		t.Error("UpdateBug: want error")
	}
	if err := c.DeleteBug(ctx, "id"); err == nil {
		t.Error("DeleteBug: want error")
	}
	if _, err := c.SummarizeBug(ctx, "id"); err == nil {
		t.Error("SummarizeBug: want error")
	}
}

func TestStack_TrimsFramesAndHandlesEdges(t *testing.T) {
	// A real stack, trimmed by two frames, keeps the goroutine header.
	full := Stack(0)
	if !strings.HasPrefix(full, "goroutine ") {
		t.Fatalf("Stack(0) missing header: %q", firstLine(full))
	}
	trimmed := Stack(2)
	if !strings.HasPrefix(trimmed, "goroutine ") {
		t.Errorf("Stack(2) dropped the header")
	}
	if len(trimmed) >= len(full) {
		t.Errorf("Stack(2) did not trim anything")
	}
	// Trimming more frames than exist stops gracefully rather than panicking.
	if got := Stack(1 << 20); !strings.HasPrefix(got, "goroutine ") {
		t.Errorf("Stack(huge) mangled the header")
	}
	// A single-line stack has no frame lines to cut.
	if got := trimFrames("only-one-line", 3); got != "only-one-line" {
		t.Errorf("trimFrames single line = %q", got)
	}
	if got := trimFrames("abc", 0); got != "abc" {
		t.Errorf("trimFrames n<=0 = %q", got)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestOptions_HTTPClientAndTimeout(t *testing.T) {
	custom := &http.Client{Timeout: 3 * time.Second}
	c, err := New("https://host", "crk_x",
		WithHTTPClient(nil), // no-op branch
		WithHTTPClient(custom),
		WithTimeout(0),             // no-op branch
		WithTimeout(7*time.Second), // sets it
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpc != custom {
		t.Errorf("WithHTTPClient(custom) not applied")
	}
	if c.httpc.Timeout != 7*time.Second {
		t.Errorf("WithTimeout = %v", c.httpc.Timeout)
	}
}

func TestHeaderCallOption_SetsHeader(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://host", nil)
	header("X-Custom", "42")(req)
	if req.Header.Get("X-Custom") != "42" {
		t.Errorf("header() did not set the value")
	}
}

func TestBackoffFor_HonoursRetryAfterAndCap(t *testing.T) {
	c := testClient(t, bugByIDServer(t), WithRetryBackoff(10*time.Millisecond))
	defer func() {}()
	// Exponential from the base when no Retry-After is given.
	if d := c.backoffFor(1, 0); d != 20*time.Millisecond {
		t.Errorf("backoffFor(1,0) = %v, want 20ms", d)
	}
	// A longer Retry-After wins over the exponential value.
	if d := c.backoffFor(0, 2*time.Second); d != 2*time.Second {
		t.Errorf("backoffFor honouring Retry-After = %v, want 2s", d)
	}
	// Everything is capped at maxRetryBackoff.
	if d := c.backoffFor(0, time.Hour); d != maxRetryBackoff {
		t.Errorf("backoffFor cap = %v, want %v", d, maxRetryBackoff)
	}
	if d := c.backoffFor(30, 0); d != maxRetryBackoff {
		t.Errorf("backoffFor exp cap = %v, want %v", d, maxRetryBackoff)
	}
}

func TestParseAPIError_BodyVariants(t *testing.T) {
	// Plain-text (non-JSON) body becomes the message.
	res := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Retry-After": {"5"}},
		Body:       bodyOf("just words"),
	}
	e := parseAPIError(res, "GET /x")
	if e.Message != "just words" {
		t.Errorf("plain-text message = %q", e.Message)
	}
	if e.RetryAfter != 5*time.Second {
		t.Errorf("RetryAfter = %v", e.RetryAfter)
	}
	// Empty body falls back to the HTTP status text when Error() is called.
	e2 := parseAPIError(&http.Response{StatusCode: http.StatusTeapot, Header: http.Header{}, Body: bodyOf("")}, "")
	if e2.Message != "" {
		t.Errorf("empty body should leave message empty, got %q", e2.Message)
	}
	if !strings.Contains(e2.Error(), http.StatusText(http.StatusTeapot)) {
		t.Errorf("Error() should fall back to status text: %q", e2.Error())
	}
	if strings.Contains(e2.Error(), ":") && strings.Count(e2.Error(), ":") > 1 {
		// no endpoint → the shorter form; just make sure it renders.
	}
}

func bodyOf(s string) *nopCloser { return &nopCloser{strings.NewReader(s)} }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func TestRetryAfter_NonAPIError(t *testing.T) {
	if RetryAfter(context.Canceled) != 0 {
		t.Error("RetryAfter of a non-API error should be 0")
	}
	e := &APIError{StatusCode: 429, RetryAfter: 3 * time.Second}
	if RetryAfter(e) != 3*time.Second {
		t.Error("RetryAfter of an APIError should surface the delay")
	}
}

func TestShipper_FlushAfterCloseAndCancelledContext(t *testing.T) {
	c := testClient(t, bugByIDServer(t))
	s := c.NewShipper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Flush(context.Background()); err == nil {
		t.Error("Flush after Close should error")
	}

	// A shipper whose Flush context is already cancelled returns that error.
	s2 := c.NewShipper()
	defer s2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s2.Flush(ctx); err == nil {
		t.Error("Flush with a cancelled context should error")
	}
}

func TestRejectedError_MessageAndItoa(t *testing.T) {
	e := &RejectedError{Accepted: 7, Rejected: 3}
	msg := e.Error()
	if !strings.Contains(msg, "3 log record(s) rejected") || !strings.Contains(msg, "7 accepted") {
		t.Errorf("RejectedError.Error() = %q", msg)
	}
	if itoa(0) != "0" || itoa(12345) != "12345" {
		t.Errorf("itoa broken: %q %q", itoa(0), itoa(12345))
	}
}

func TestShipper_SurfacesRejectedRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":2}`))
	}))
	defer srv.Close()

	got := make(chan error, 1)
	c := testClient(t, srv)
	s := c.NewShipper(WithErrorHandler(func(err error) {
		select {
		case got <- err:
		default:
		}
	}))
	defer s.Close()
	s.Send(LogEntry{Message: "x", Level: "info"})
	if err := s.Flush(context.Background()); err == nil {
		t.Error("Flush should report the rejected records")
	}
	select {
	case err := <-got:
		var re *RejectedError
		if !asRejected(err, &re) {
			t.Errorf("error handler got %T, want *RejectedError", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("error handler was not called")
	}
}

func TestReportErrorHandler_FiresWhenReportFails(t *testing.T) {
	// The bug POST always fails, so the panic report cannot be filed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := testClient(t, srv, WithMaxRetries(0))

	reportErr := make(chan error, 1)
	h := c.Recover(
		WithService("svc"),
		WithReportErrorHandler(func(err error) {
			select {
			case reportErr <- err:
			default:
			}
		}),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", rec.Code)
	}
	select {
	case <-reportErr:
	case <-time.After(2 * time.Second):
		t.Error("WithReportErrorHandler was never called")
	}
}

func TestFilters_EncodeEveryField(t *testing.T) {
	bf := BugFilter{Severity: P1, Owner: "alice", Query: "q", Sort: "severity", Limit: 20, Offset: 40}
	got := bf.values().Encode()
	for _, want := range []string{"filter=P1", "owner=alice", "q=q", "sort=severity", "limit=20", "offset=40"} {
		if !strings.Contains(got, want) {
			t.Errorf("BugFilter query missing %q in %q", want, got)
		}
	}
	lf := LogFilter{TraceID: "abc", Severity: "ERROR", Query: "level:error", Format: "json", Since: 15 * time.Minute, Limit: 100, Offset: 10}
	gotL := lf.values().Encode()
	for _, want := range []string{"trace_id=abc", "severity=ERROR", "format=json", "since=15m", "limit=100", "offset=10"} {
		if !strings.Contains(gotL, want) {
			t.Errorf("LogFilter query missing %q in %q", want, gotL)
		}
	}
}

func TestListAndAnalyze_PropagateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	c := testClient(t, srv, WithMaxRetries(0))
	ctx := context.Background()
	if _, _, err := c.ListBugs(ctx, BugFilter{Limit: 5}); !IsForbidden(err) {
		t.Errorf("ListBugs err = %v", err)
	}
	if _, _, err := c.ListLogs(ctx, LogFilter{Limit: 5}); !IsForbidden(err) {
		t.Errorf("ListLogs err = %v", err)
	}
	if _, err := c.AnalyzeLogs(ctx); !IsForbidden(err) {
		t.Errorf("AnalyzeLogs err = %v", err)
	}
}

func TestSlogHandler_NilLevelDefaultsToInfo(t *testing.T) {
	c := testClient(t, bugByIDServer(t))
	s := c.NewShipper()
	defer s.Close()
	h := NewSlogHandler(s, nil)
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("nil level should default to Info and enable Info")
	}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("Debug should be below the default Info threshold")
	}
}

func TestSlogHandler_WithGroupAndGroupValuedAttrs(t *testing.T) {
	c := testClient(t, bugByIDServer(t))
	s := c.NewShipper()
	defer s.Close()

	base := NewSlogHandler(s, slog.LevelInfo)
	// WithAttrs("") no-op returns the same handler; empty group name too.
	if base.WithAttrs(nil) != slog.Handler(base) {
		t.Error("WithAttrs(nil) should return the receiver")
	}
	if base.WithGroup("") != slog.Handler(base) {
		t.Error("WithGroup(\"\") should return the receiver")
	}

	// At the top level, trace_id/span_id are lifted into their dedicated fields.
	top := base.WithAttrs([]slog.Attr{
		slog.String("trace_id", "deadbeef"),
		slog.String("span_id", "cafe"),
		slog.String("kept", "yes"),
		{}, // empty attr, skipped
	}).(*SlogHandler)
	if top.preTrace != "deadbeef" || top.preSpan != "cafe" {
		t.Errorf("trace/span not lifted: trace=%q span=%q", top.preTrace, top.preSpan)
	}
	if top.preAttrs["kept"] != "yes" {
		t.Errorf("ordinary attr dropped: %v", top.preAttrs)
	}

	// Inside a group, keys are scoped and correlation ids are NOT lifted.
	h := base.WithGroup("http").WithAttrs([]slog.Attr{
		slog.String("method", "GET"),
		slog.Group("client", slog.String("ip", "127.0.0.1")),
	}).(*SlogHandler)
	if h.preAttrs["http.method"] != "GET" {
		t.Errorf("grouped attr not scoped: %v", h.preAttrs)
	}
	if h.preAttrs["http.client.ip"] != "127.0.0.1" {
		t.Errorf("nested group attr not flattened: %v", h.preAttrs)
	}
}

func TestMultiHandler_WithAttrsAndWithGroup(t *testing.T) {
	rec := &capturingHandler{}
	m := MultiHandler(rec).WithAttrs([]slog.Attr{slog.String("k", "v")}).WithGroup("g")
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hi", 0)
	if err := m.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !rec.handled {
		t.Error("record did not reach the child handler through WithAttrs/WithGroup")
	}
}

// capturingHandler is a minimal slog.Handler that records that it was called.
type capturingHandler struct{ handled bool }

func (c *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturingHandler) Handle(context.Context, slog.Record) error {
	c.handled = true
	return nil
}
func (c *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capturingHandler) WithGroup(string) slog.Handler      { return c }

// asRejected is errors.As specialized for *RejectedError, kept local so the
// test file needs no extra imports.
func asRejected(err error, target **RejectedError) bool {
	for err != nil {
		if re, ok := err.(*RejectedError); ok {
			*target = re
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
