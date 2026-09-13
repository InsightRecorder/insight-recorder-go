package insight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient returns a client pointed at srv with fast retries.
func testClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithRetryBackoff(time.Millisecond)}, opts...)
	c, err := New(srv.URL, "crk_test-key", opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_Validation(t *testing.T) {
	for name, tc := range map[string]struct{ base, key string }{
		"empty base":   {"", "crk_x"},
		"bad scheme":   {"ftp://host", "crk_x"},
		"no host":      {"notaurl", "crk_x"},
		"empty apiKey": {"https://host", ""},
	} {
		if _, err := New(tc.base, tc.key); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	c, err := New("https://host/", "crk_x")
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if c.baseURL != "https://host" {
		t.Errorf("trailing slash not trimmed: %q", c.baseURL)
	}
}

func TestDo_SendsAuthAndUserAgent(t *testing.T) {
	var gotAuth, gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotAccept = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := testClient(t, srv, WithUserAgent("myapp/2.0"))
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if gotAuth != "Bearer crk_test-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "myapp/2.0 insight-recorder-go/") {
		t.Errorf("user-agent = %q", gotUA)
	}
	if gotAccept != "application/json" {
		t.Errorf("accept = %q", gotAccept)
	}
}

func TestDo_RetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := testClient(t, srv).Health(context.Background()); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestDo_DoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Bad Request"}`))
	}))
	defer srv.Close()

	err := testClient(t, srv).Health(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Errorf("4xx must not be retried, calls = %d", calls.Load())
	}
	if !strings.Contains(err.Error(), "Bad Request") {
		t.Errorf("server message not surfaced: %v", err)
	}
}

func TestDo_QuotaExceededSurfacesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"quota_exceeded"}`))
	}))
	defer srv.Close()

	// maxRetries 0 so the test does not wait out the backoff.
	err := testClient(t, srv, WithMaxRetries(0)).Health(context.Background())
	if !IsQuotaExceeded(err) {
		t.Fatalf("IsQuotaExceeded = false for %v", err)
	}
	if got := RetryAfter(err); got != time.Hour {
		t.Errorf("RetryAfter = %v, want 1h", got)
	}
}

func TestErrorClassifiers(t *testing.T) {
	cases := map[int]func(error) bool{
		http.StatusUnauthorized:       IsUnauthorized,
		http.StatusForbidden:          IsForbidden,
		http.StatusNotFound:           IsNotFound,
		http.StatusConflict:           IsConflict,
		http.StatusServiceUnavailable: IsUnavailable,
	}
	for status, classifier := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		err := testClient(t, srv, WithMaxRetries(0)).Health(context.Background())
		if !classifier(err) {
			t.Errorf("status %d not classified: %v", status, err)
		}
		srv.Close()
	}
}

func TestDo_RespectsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // always retryable
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c := testClient(t, srv, WithRetryBackoff(500*time.Millisecond))
	if err := c.Health(ctx); err == nil {
		t.Fatal("expected the cancelled context to abort retrying")
	}
}
