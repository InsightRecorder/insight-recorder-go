// Package insight is the Go SDK for InsightRecorder (insight_recorder): ship your
// application's logs, report bugs and panics with context, and query the API —
// from any Go service.
//
// # Getting started
//
// Create an API key at Settings → API keys (a long-lived "crk_…" credential
// scoped to logs/bugs; user JWTs expire after an hour and are not meant for
// machines), then:
//
//	c, err := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))
//	if err != nil {
//		return err
//	}
//
// # Shipping logs
//
// The Shipper batches records in the background and never blocks the caller:
// a full queue drops (counted by [Shipper.Dropped]) rather than stalling your
// request path. Always Close it so the final batch is flushed.
//
//	sh := c.NewShipper()
//	defer sh.Close()
//	slog.SetDefault(slog.New(insight.NewSlogHandler(sh, slog.LevelInfo)))
//
// For zerolog, use the github.com/InsightRecorder/insight-recorder-go/zerolog submodule
// (kept separate so this module stays dependency-free).
//
// # Reporting bugs and panics
//
// [Client.Recover] is net/http middleware that turns a panic into a bug report
// carrying the stack trace, the request's method and path, and — when a Shipper
// is attached — the log lines that led up to it.
//
//	mux.Handle("/", c.Recover(insight.WithShipper(sh))(handler))
//
// # Privacy
//
// The SDK never captures request bodies, headers, cookies, or query strings:
// those routinely carry credentials and personal data. Only the method and
// path are recorded. Whatever is submitted is additionally PII-redacted
// server-side before storage.
package insight
