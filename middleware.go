package insight

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// reportTimeout bounds a panic report so a slow API cannot delay the response
// (the report already runs detached, but it must not leak a goroutine either).
const reportTimeout = 15 * time.Second

// recoverConfig tunes the panic-recovery middleware.
type recoverConfig struct {
	shipper  *Shipper
	severity Severity
	service  string
	onPanic  func(v any, stack string)
	onError  func(error)
	respond  func(http.ResponseWriter, *http.Request)
}

// RecoverOption configures [Client.Recover].
type RecoverOption func(*recoverConfig)

// WithShipper attaches the log lines the Shipper still holds in memory to
// each panic report — the trail that led to the crash.
func WithShipper(s *Shipper) RecoverOption {
	return func(c *recoverConfig) { c.shipper = s }
}

// WithPanicSeverity overrides the severity filed for panics (default P0).
func WithPanicSeverity(s Severity) RecoverOption {
	return func(c *recoverConfig) {
		if s != "" {
			c.severity = s
		}
	}
}

// WithService labels reports with the service or build identifier.
func WithService(name string) RecoverOption {
	return func(c *recoverConfig) { c.service = name }
}

// WithOnPanic installs a callback invoked synchronously with the recovered
// value and stack — for your own logging or metrics.
func WithOnPanic(fn func(v any, stack string)) RecoverOption {
	return func(c *recoverConfig) {
		if fn != nil {
			c.onPanic = fn
		}
	}
}

// WithReportErrorHandler receives failures to file the bug report itself.
func WithReportErrorHandler(fn func(error)) RecoverOption {
	return func(c *recoverConfig) {
		if fn != nil {
			c.onError = fn
		}
	}
}

// WithResponder replaces the response written after a panic (default: a bare
// 500 with no body, so no internal detail leaks to the caller).
func WithResponder(fn func(http.ResponseWriter, *http.Request)) RecoverOption {
	return func(c *recoverConfig) {
		if fn != nil {
			c.respond = fn
		}
	}
}

// Recover returns net/http middleware that turns a panic into a bug report
// and a 500, instead of a crashed process.
//
//	mux.Handle("/", c.Recover(insight.WithShipper(sh))(handler))
//
// The report carries the panic value, the stack trace, the request's method
// and path, and (with [WithShipper]) recent log lines. Request bodies,
// headers, cookies, and query strings are never captured: they routinely
// carry credentials and personal data.
func (c *Client) Recover(opts ...RecoverOption) func(http.Handler) http.Handler {
	cfg := &recoverConfig{
		severity: P0,
		onPanic:  func(any, string) {},
		onError:  func(error) {},
		respond: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { // deliberate abort: not a bug
					panic(v)
				}
				stack := Stack(3)
				cfg.onPanic(v, stack)
				c.reportPanic(cfg, r, v, stack)
				cfg.respond(w, r)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// reportPanic files the bug out of band: the client's response must not wait
// on our API, and a failure to report must not mask the original panic.
func (c *Client) reportPanic(cfg *recoverConfig, r *http.Request, v any, stack string) {
	bug := NewBug{
		Title:    fmt.Sprintf("panic: %v (%s %s)", v, r.Method, r.URL.Path),
		Severity: cfg.severity,
		// Path only — the query string may carry tokens.
		URL: r.URL.Path,
		Steps: []string{
			fmt.Sprintf("%s %s", r.Method, r.URL.Path),
			fmt.Sprintf("panic: %v", v),
		},
		Meta: runtimeMeta(cfg.service, traceparentID(r)),
	}
	bug.Console = append(recentLines(cfg.shipper), LogLine{
		Time:    time.Now(),
		Level:   "error",
		Source:  "panic",
		Message: stack,
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		if _, err := c.ReportBug(ctx, bug); err != nil {
			cfg.onError(err)
		}
	}()
}

// recentLines returns the shipper's buffered log lines, or nil.
func recentLines(s *Shipper) []LogLine {
	if s == nil {
		return nil
	}
	return s.Recent()
}

// traceparentID extracts the W3C trace id from the incoming request so the
// bug can be correlated with the logs and traces of the same request.
func traceparentID(r *http.Request) string {
	tp := r.Header.Get("traceparent")
	// version "-" trace-id "-" parent-id "-" flags; trace id is 32 hex chars.
	const idStart, idEnd = 3, 35
	if len(tp) >= idEnd && tp[2] == '-' {
		return tp[idStart:idEnd]
	}
	return ""
}
