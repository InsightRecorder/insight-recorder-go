# insight-recorder-go

The Go SDK for [InsightRecorder](https://insightrecorder.com): ship your
service's logs, turn panics into bug reports with full context, and query the
API — from any Go application.

```sh
go get github.com/InsightRecorder/insight-recorder-go
```

Requires **Go 1.22+**. The core module has **zero third-party dependencies** —
nothing enters your `go.mod` but this.

## Quick start

```go
client, err := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))
if err != nil {
    return err
}

// Logs: batched in the background, flushed on shutdown.
shipper := client.NewShipper()
defer shipper.Close()

// Keep logging to stdout AND ship to InsightRecorder.
slog.SetDefault(slog.New(insight.MultiHandler(
    slog.NewJSONHandler(os.Stdout, nil),
    insight.NewSlogHandler(shipper, slog.LevelInfo),
)))

// Panics become bug reports carrying the stack and the recent log trail.
handler := client.Recover(insight.WithShipper(shipper), insight.WithService("checkout-api"))(mux)
http.ListenAndServe(":8080", handler)
```

## Configuring the base URL

The base URL you pass to `insight.New(...)` is the address of **your InsightRecorder deployment** — the server that
receives your logs and bug reports — **not the application you are monitoring**.
This SDK runs *inside* your app and points *at* that server.

- **Self-hosted:** your own instance, e.g. `https://insight.yourcompany.com`.
- **SaaS:** your InsightRecorder host / regional endpoint, e.g. `https://app.insightrecorder.com`.

`https://insightrecorder.example.com` in the examples is only a placeholder for
that address.

## Credentials

Use a **long-lived API key** (`crk_…`) created at *Settings → API keys*, scoped
to `logs:write` (and `bug:write` for reports). User JWTs expire after an hour and
are not meant for machines.

Add the matching **read** scopes for anything the SDK reads back — `bug:read`
for `ListBugs`/`GetBug`, `logs:read` for `ListLogs`. A write-only key is the
common mistake: shipping works, and the first read returns 403.

Keys are hashed at rest, shown exactly once, and revocable. A key containing
whitespace or control characters is rejected at construction — the common
accident is a trailing newline from a copy-paste, which would otherwise surface
as a baffling 401.

## What it does

| Area | API |
|---|---|
| Log shipping | `client.NewShipper()`, `insight.NewSlogHandler`, `insight.MultiHandler`, `client.IngestLogs` |
| Panic capture | `client.Recover(...)` middleware |
| Bug reports | `ReportBug`, `GetBug`, `UpdateBug`, `SendBug`, `SummarizeBug`, `DeleteBug` |
| Queries | `ListBugs`, `ListLogs`, `AnalyzeLogs` |
| Trace correlation | `NewSlogHandler(...).WithTraceExtractor(insightotel.TraceIDs)` — companion module below |
| Errors | `IsUnauthorized`, `IsForbidden`, `IsNotFound`, `IsConflict`, `IsQuotaExceeded`, `IsUnavailable`, `RetryAfter` |

## Trace correlation

A log line and the request that wrote it are one investigation — that join is
the reason to ship logs here rather than anywhere else, and it happens **without
you passing `trace_id` on every call**.

The extractor lives in a separate module so the core stays dependency-free:

```sh
go get github.com/InsightRecorder/insight-recorder-go/otel
```

```go
import insightotel "github.com/InsightRecorder/insight-recorder-go/otel"

h := insight.NewSlogHandler(shipper, slog.LevelInfo).
    WithTraceExtractor(insightotel.TraceIDs)
slog.SetDefault(slog.New(insight.MultiHandler(stdout, h)))
```

From then on `slog.InfoContext(ctx, …)` correlates by itself; plain `slog.Info`
cannot, because there is no span to read. With zerolog, use
`insightzerolog.Ctx(ctx, logger, insightotel.TraceIDs)` — a writer sees bytes,
never a context, so the correlation has to come from the logger.

An id you set on the entry yourself always wins: what you wrote is a deliberate
statement, the ambient span is a default. Outside a request there is no span,
the line ships without one, and that is not an error.

## zerolog

The zerolog adapter is a **separate module**, so the core never drags zerolog
into a build that does not use it:

```sh
go get github.com/InsightRecorder/insight-recorder-go/zerolog
```

```go
import insightzerolog "github.com/InsightRecorder/insight-recorder-go/zerolog"

log.Logger = zerolog.New(zerolog.MultiLevelWriter(
    os.Stdout,
    insightzerolog.NewWriter(shipper),
)).With().Timestamp().Logger()
```

## Behaviour that matters in production

- **Logging never blocks your application.** `Send` returns immediately; when the
  queue is full, entries are dropped and counted by `shipper.Dropped()`. Rising
  drops mean `WithQueueSize`/`WithBatchSize` are undersized for your volume.
- **Always `Close()` the shipper** (defer it next to your server shutdown), or
  the final batch is lost.
- **Retries are bounded and quota-aware**: 429 and 5xx are retried with
  exponential backoff, honouring `Retry-After`; 4xx are not retried.
- **Delivery failures are silent by default.** A logging SDK that logs its own
  failures through your logger causes recursion. Use `WithErrorHandler` to route
  them somewhere safe.
- **Panic reports are filed out of band** — the HTTP response is not delayed by
  the report, and a failed report never masks the original panic.
  `http.ErrAbortHandler` is re-panicked untouched, as the stdlib expects.

## Privacy contract

The SDK **never** captures request bodies, headers, cookies, or query strings —
they routinely carry credentials and personal data. Not redacted: never read.
Panic reports record the method and path only. Everything submitted is
additionally PII-redacted server-side before storage.

## Metrics and traces

This SDK ships **logs** and files **bugs**. It does not export metrics or traces,
and that is deliberate: your own OpenTelemetry SDK already does it well, and
wrapping it would pin our OTel version inside your application.

Point the OTel exporter at the deployment and the three signals land in the same
workspace, joined by trace id. **Use the per-signal variables**, not the generic
one:

```sh
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=https://insightrecorder.example.com/api/traces/otlp
OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=https://insightrecorder.example.com/api/metrics/otlp
OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer crk_…
```

The one trap: `OTEL_EXPORTER_OTLP_ENDPOINT` has the signal path **appended** by
the SDK (`…/v1/traces`), which InsightRecorder does not serve — you get a 404 and
no telemetry, with nothing obviously wrong in the application. The per-signal
variables are used exactly as written. The key needs `traces:write` and
`metrics:write` for those; OTLP/gRPC on `:4317` works too.

## Development

```sh
go test -race ./...                 # core module
cd otel     && go test -race ./...  # OpenTelemetry adapter
cd zerolog  && go test -race ./...  # zerolog adapter
```

The tests run against a real `httptest` server, so the assertions cover the
request that would actually reach InsightRecorder — headers, encoding, status
handling and retries included.

## Security

Found a way the SDK captures something it says it does not, or any other
vulnerability? See [SECURITY.md](../SECURITY.md). Report to
**security@insightrecorder.com** — no NDA required.

## License

[MIT](./LICENSE) © Solution4Mac
