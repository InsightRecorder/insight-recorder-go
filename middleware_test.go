package insight

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// bugRecorder captures reported bugs.
type bugRecorder struct {
	mu   sync.Mutex
	bugs []map[string]any
	got  chan struct{}
	srv  *httptest.Server
}

func newBugRecorder(t *testing.T) *bugRecorder {
	t.Helper()
	rec := &bugRecorder{got: make(chan struct{}, 8)}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		rec.mu.Lock()
		rec.bugs = append(rec.bugs, b)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1","ref":"BUG-1"}`))
		select {
		case rec.got <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// wait blocks until a bug is reported or the test times out.
func (b *bugRecorder) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-b.got:
	case <-time.After(3 * time.Second):
		t.Fatal("no bug was reported")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bugs[len(b.bugs)-1]
}

func TestRecover_ReportsPanicAndReturns500(t *testing.T) {
	rec := newBugRecorder(t)
	c := testClient(t, rec.srv)

	h := c.Recover(WithService("checkout-api"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom in handler")
	}))

	req := httptest.NewRequest(http.MethodPost, "/orders/42?token=SECRET", nil)
	req.Header.Set("Authorization", "Bearer super-secret")
	req.Header.Set("Cookie", "session=secret-cookie")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	bug := rec.wait(t)

	if title, _ := bug["title"].(string); !strings.Contains(title, "boom in handler") {
		t.Errorf("title = %q", title)
	}
	if bug["severity"] != string(P0) {
		t.Errorf("severity = %v, want P0", bug["severity"])
	}
	// Privacy contract: no credentials, no query string.
	raw, _ := json.Marshal(bug)
	for _, secret := range []string{"super-secret", "secret-cookie", "SECRET", "token="} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("report leaked %q: %s", secret, raw)
		}
	}
	if bug["url"] != "/orders/42" {
		t.Errorf("url = %v, want the path only", bug["url"])
	}
	// Trace correlation is preserved.
	meta, _ := bug["meta"].(map[string]any)
	if meta["session"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id not captured: %v", meta["session"])
	}
	if meta["build"] != "checkout-api" {
		t.Errorf("service = %v", meta["build"])
	}
	// The stack trace rides along as a console line.
	console, _ := bug["console"].([]any)
	if len(console) == 0 {
		t.Fatal("no console lines attached")
	}
	last, _ := console[len(console)-1].(map[string]any)
	if msg, _ := last["message"].(string); !strings.Contains(msg, "goroutine") {
		t.Errorf("stack trace missing: %q", msg)
	}
}

func TestRecover_AttachesRecentLogs(t *testing.T) {
	rec := newBugRecorder(t)
	c := testClient(t, rec.srv)
	sh := c.NewShipper(WithRecentLogs(10), WithBatchSize(1000), WithFlushInterval(time.Hour))
	defer sh.Close()

	logger := slog.New(NewSlogHandler(sh, slog.LevelDebug))
	h := c.Recover(WithShipper(sh))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		logger.Info("about to explode", "order_id", "42")
		panic("kaboom")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	bug := rec.wait(t)
	console, _ := bug["console"].([]any)
	if len(console) < 2 {
		t.Fatalf("expected the log trail plus the stack, got %d line(s)", len(console))
	}
	first, _ := console[0].(map[string]any)
	if first["message"] != "about to explode" {
		t.Errorf("log trail not attached: %v", first)
	}
}

func TestRecover_PassesThroughNormalRequests(t *testing.T) {
	rec := newBugRecorder(t)
	c := testClient(t, rec.srv)
	h := c.Recover()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the handler's own", w.Code)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bugs) != 0 {
		t.Error("a successful request must not file a bug")
	}
}

func TestRecover_RepanicsOnAbortHandler(t *testing.T) {
	rec := newBugRecorder(t)
	c := testClient(t, rec.srv)
	h := c.Recover()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if v := recover(); v != http.ErrAbortHandler {
			t.Errorf("ErrAbortHandler must propagate, got %v", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("expected the panic to propagate")
}

func TestRecover_CustomResponderAndCallback(t *testing.T) {
	rec := newBugRecorder(t)
	c := testClient(t, rec.srv)

	var seen string
	h := c.Recover(
		WithPanicSeverity(P1),
		WithOnPanic(func(v any, _ string) { seen = v.(string) }),
		WithResponder(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("custom") }))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want the custom responder's", w.Code)
	}
	if seen != "custom" {
		t.Errorf("callback saw %q", seen)
	}
	if bug := rec.wait(t); bug["severity"] != string(P1) {
		t.Errorf("severity = %v, want P1", bug["severity"])
	}
}

func TestTraceparentID(t *testing.T) {
	for name, tc := range map[string]struct{ header, want string }{
		"valid":   {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "4bf92f3577b34da6a3ce929d0e0e4736"},
		"absent":  {"", ""},
		"garbage": {"nonsense", ""},
		"short":   {"00-abc", ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			r.Header.Set("traceparent", tc.header)
		}
		if got := traceparentID(r); got != tc.want {
			t.Errorf("%s: traceparentID = %q, want %q", name, got, tc.want)
		}
	}
}
