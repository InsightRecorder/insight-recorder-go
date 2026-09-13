package insight_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	insight "github.com/InsightRecorder/insight-recorder-go"
)

// The typical wiring for a Go service: ship logs, catch panics, shut down
// cleanly.
func Example() {
	client, err := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))
	if err != nil {
		panic(err)
	}

	// Logs: batched in the background, flushed on shutdown.
	shipper := client.NewShipper()
	defer shipper.Close()

	// Keep logging to stdout AND ship to InsightRecorder.
	slog.SetDefault(slog.New(insight.MultiHandler(
		slog.NewJSONHandler(os.Stdout, nil),
		insight.NewSlogHandler(shipper, slog.LevelInfo),
	)))

	// Panics become bug reports carrying the stack and the log trail.
	mux := http.NewServeMux()
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("handling order", "trace_id", r.Header.Get("traceparent"))
		w.WriteHeader(http.StatusOK)
	})
	handler := client.Recover(
		insight.WithShipper(shipper),
		insight.WithService("checkout-api"),
	)(mux)

	_ = http.ListenAndServe(":8080", handler)
}

// Report a bug explicitly — for a failure you handle rather than a panic.
func ExampleClient_ReportBug() {
	client, _ := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bug, err := client.ReportBug(ctx, insight.NewBug{
		Title:    "payment provider rejected a valid card",
		Severity: insight.P1,
		Steps:    []string{"POST /checkout", "provider returned 502"},
	}, insight.IdempotencyKey("order-4711")) // safe to retry
	if err != nil {
		if insight.IsQuotaExceeded(err) {
			// The workspace hit its daily plan quota; back off.
			time.Sleep(insight.RetryAfter(err))
		}
		return
	}
	slog.Info("filed", "ref", bug.Ref)
}

// Ship a batch of logs without the background shipper.
func ExampleClient_IngestLogs() {
	client, _ := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))

	res, err := client.IngestLogs(context.Background(), []insight.LogEntry{{
		Time:    time.Now(),
		Level:   "error",
		Message: "checkout failed",
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		Attrs:   map[string]string{"order_id": "1234"},
	}})
	if err != nil {
		return
	}
	slog.Info("ingested", "accepted", res.Accepted, "rejected", res.Rejected)
}

// Query what was captured — for CI checks or an internal dashboard.
func ExampleClient_ListBugs() {
	client, _ := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))

	bugs, page, err := client.ListBugs(context.Background(), insight.BugFilter{
		Severity: insight.P0,
		Limit:    20,
	})
	if err != nil {
		if insight.IsForbidden(err) {
			slog.Error("the API key lacks the bug:read scope")
		}
		return
	}
	slog.Info("critical bugs", "shown", len(bugs), "total", page.Total)
}

// Surface the incident the AI found across recent logs.
func ExampleClient_AnalyzeLogs() {
	client, _ := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))

	analysis, err := client.AnalyzeLogs(context.Background())
	switch {
	case insight.IsUnavailable(err):
		slog.Warn("no AI provider configured for this workspace")
	case err != nil:
		slog.Error("analysis failed", "err", err)
	default:
		slog.Info("incident", "label", analysis.Incident, "severity", analysis.Severity)
	}
}

// Drop everything when the credential is wrong — a common first-run mistake.
func ExampleIsUnauthorized() {
	client, _ := insight.New("https://insightrecorder.example.com", "crk_revoked")
	_, _, err := client.ListBugs(context.Background(), insight.BugFilter{})
	if insight.IsUnauthorized(err) {
		slog.Error("API key rejected: revoked, or from another workspace")
	}
	var apiErr *insight.APIError
	if errors.As(err, &apiErr) {
		slog.Error("api", "status", apiErr.StatusCode, "endpoint", apiErr.Endpoint)
	}
}
