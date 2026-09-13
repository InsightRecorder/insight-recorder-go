# Changelog

All notable changes to the Go SDK are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); this module follows
semantic versioning once it reaches v1.

## [Unreleased]

## [0.1.0] — 2026-09-07

First release. Requires Go 1.22+; the core module has no dependencies.

### Added

- `Client` with API-key auth, bounded exponential-backoff retries (429/5xx),
  `Retry-After` support, and typed error classifiers (`IsUnauthorized`,
  `IsForbidden`, `IsNotFound`, `IsConflict`, `IsQuotaExceeded`,
  `IsUnavailable`, `RetryAfter`).
- `Shipper`: background log batching by size and interval, non-blocking
  `Send` with drop counting, `Flush`, and flush-on-`Close`.
- `SlogHandler` and `MultiHandler` for `log/slog`; `trace_id`/`span_id`
  attributes are lifted into the record's correlation fields.
- `Recover` net/http middleware: panics become bug reports carrying the stack
  trace, request method and path, W3C trace id, and recent log lines.
- Bug APIs (`ReportBug`, `GetBug`, `UpdateBug`, `SendBug`, `SummarizeBug`,
  `DeleteBug`) with `IdempotencyKey` support.
- Query APIs (`ListBugs`, `ListLogs`, `AnalyzeLogs`).
- `zerolog` adapter as a separate module so the core stays dependency-free.

### Security

- Request bodies, headers, cookies, and query strings are never captured.
- Bug ids are path-escaped before being placed in a URL.
