# Install and wire the InsightRecorder Go SDK

Paste this into a coding agent (Claude Code, Cursor, …) running inside your Go
service's repository. It is written to be self-contained — the agent needs no
prior knowledge of InsightRecorder.

---

You are working in a Go service. Install and wire the InsightRecorder SDK
(`github.com/InsightRecorder/insight-recorder-go`) so this service ships its logs to
InsightRecorder and reports panics as bug reports.

**Context you need:**

- InsightRecorder collects application logs and bug reports. This SDK is the
  supported way for a Go service to talk to it.
- The SDK requires **Go 1.22+** and has **zero third-party dependencies** in
  its core module.
- Authentication uses a long-lived **API key** that looks like `crk_…`, created
  in the InsightRecorder UI under *Settings → API keys* with the `logs:write` scope
  (add `bug:write` to report bugs). **Do not** use a login JWT: those expire
  after an hour and are not meant for machines.

  Add the matching **read** scopes for anything the SDK reads back — `bug:read` for `ListBugs/GetBug`, `logs:read` for `ListLogs`. A write-only key is the common mistake: shipping works, the first read returns 403.
- **If the application is instrumented with OpenTelemetry, wire the trace
  correlation** — it is one call, and without it the log line and the trace of
  the request that wrote it never join, which is the whole point of shipping
  logs here:

  ```go
  import (
      insight "github.com/InsightRecorder/insight-recorder-go"
      insightotel "github.com/InsightRecorder/insight-recorder-go/otel"
  )

  h := insight.NewSlogHandler(sh, slog.LevelInfo).
      WithTraceExtractor(insightotel.TraceIDs)
  slog.SetDefault(slog.New(insight.MultiHandler(stdoutHandler, h)))
  ```

  From then on `slog.InfoContext(ctx, …)` correlates by itself. Plain
  `slog.Info` (no context) cannot — there is no span to read, so prefer the
  `…Context` variants in request paths. With zerolog use
  `insightzerolog.Ctx(ctx, log.Logger, insightotel.TraceIDs)`; a zerolog writer
  sees bytes, never a context, so the correlation has to come from the logger.

- The base URL is the InsightRecorder deployment's origin, e.g.
  `https://insightrecorder.example.com`.

- **Wire trace correlation if the application uses OpenTelemetry.** A log line
  and the request that wrote it are one investigation, and that join is the
  reason to ship logs here rather than anywhere else — see the "Trace
  correlation" section of the README for what this language needs (several do it
  automatically; the rest take one hook). Without it, every log call has to carry
  `trace_id` by hand, which nobody sustains past the first week.

**Steps:**

1. Add the dependency:

   ```sh
   go get github.com/InsightRecorder/insight-recorder-go
   ```

   If — and only if — this service already logs with `github.com/rs/zerolog`,
   also run `go get github.com/InsightRecorder/insight-recorder-go/zerolog`. Otherwise
   skip it: the adapter is a separate module precisely so services that use
   `log/slog` never pull zerolog into their build.

2. Read configuration from the environment, never hard-code it. Add to the
   service's existing config loading (and to `.env.example` / deployment
   manifests, whichever this repo uses):

   - `INSIGHT_BASE_URL` — the InsightRecorder origin
   - `INSIGHT_API_KEY` — the `crk_…` key

   If either is empty, the service must start normally with shipping disabled.
   Telemetry is never allowed to be a startup dependency.

3. In the service's composition root (wherever the logger and HTTP server are
   built — do not create a new "init" package if one already exists), wire:

   ```go
   client, err := insight.New(os.Getenv("INSIGHT_BASE_URL"), os.Getenv("INSIGHT_API_KEY"))
   if err != nil {
       return err
   }

   shipper := client.NewShipper()
   defer shipper.Close() // MUST run on shutdown or the last batch is lost

   // Keep the existing local logging AND ship to InsightRecorder.
   slog.SetDefault(slog.New(insight.MultiHandler(
       slog.NewJSONHandler(os.Stdout, nil),
       insight.NewSlogHandler(shipper, slog.LevelInfo),
   )))

   // Panics become bug reports carrying the stack, the request's method and
   // path, and the log lines that led up to them.
   handler := client.Recover(
       insight.WithShipper(shipper),
       insight.WithService("<this service's name>"),
   )(existingHandler)
   ```

   Match the existing style: if the service uses zerolog, use
   `insightzerolog.NewWriter(shipper)` inside its `zerolog.MultiLevelWriter`
   instead of the slog block. If it uses a router (chi, gin, echo), apply
   `client.Recover(...)` as middleware in that router's idiom rather than
   wrapping the handler manually.

4. Ensure `shipper.Close()` actually runs on the service's existing graceful
   shutdown path (next to `srv.Shutdown`), not only via `defer` in a function
   that may not return.

**Rules — do not violate these:**

- Do **not** write a custom `io.Writer` that POSTs one HTTP request per log
  line. The SDK already batches, retries with backoff, respects the server's
  quota (429 + `Retry-After`), and flushes on close.
- Do **not** make logging block on the network: `shipper.Send` is
  non-blocking by design and drops when its queue is full. Do not wrap it in
  anything that waits.
- Do **not** log the API key, and do not commit it.
- Do **not** capture request bodies, headers, cookies, or query strings in bug
  reports. The SDK deliberately records only the method and path because those
  carry credentials and personal data.

**Verify before you finish:**

1. `go build ./...` and the repo's existing test command both pass.
2. Start the service with `INSIGHT_BASE_URL`/`INSIGHT_API_KEY` set, exercise an
   endpoint, then confirm the log lines appear in InsightRecorder under `/app/logs`
   (filter by the service name or a `trace_id` you logged).
3. Confirm the service still starts normally with both variables **unset**.
4. Report what you changed, and state explicitly whether `shipper.Close()` is
   wired into the shutdown path.

If anything is ambiguous — which logger the service uses, where the composition
root is, how shutdown is handled — inspect the repository and follow what is
already there. Do not restructure the service to fit the SDK.
