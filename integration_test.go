package insight_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	insight "github.com/InsightRecorder/insight-recorder-go"
)

// Integration tests run against a real InsightRecorder deployment. They are skipped
// unless both variables are set:
//
//	INSIGHT_TEST_URL=http://localhost:8095 \
//	INSIGHT_TEST_KEY=crk_... \
//	go test -run Integration -v ./...
func integrationClient(t *testing.T) *insight.Client {
	t.Helper()
	base, key := os.Getenv("INSIGHT_TEST_URL"), os.Getenv("INSIGHT_TEST_KEY")
	if base == "" || key == "" {
		t.Skip("set INSIGHT_TEST_URL and INSIGHT_TEST_KEY to run integration tests")
	}
	c, err := insight.New(base, key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestIntegration_HealthAndAuth(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	if err := c.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	// A bad key must be rejected, not silently accepted.
	bad, _ := insight.New(os.Getenv("INSIGHT_TEST_URL"), "crk_definitely-not-a-real-key",
		insight.WithMaxRetries(0))
	if _, _, err := bad.ListBugs(ctx, insight.BugFilter{}); !insight.IsUnauthorized(err) {
		t.Errorf("revoked key must be unauthorized, got %v", err)
	}
}

func TestIntegration_ShipLogsAndQueryThemBack(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	traceID := "sdkit" + time.Now().UTC().Format("150405.000000000")
	res, err := c.IngestLogs(ctx, []insight.LogEntry{{
		Time:    time.Now(),
		Level:   "error",
		Message: "sdk integration probe",
		TraceID: traceID,
		Attrs:   map[string]string{"probe": "1"},
	}})
	if err != nil {
		t.Fatalf("IngestLogs: %v", err)
	}
	if res.Accepted != 1 || res.Rejected != 0 {
		t.Fatalf("ingest result = %+v, want 1 accepted", res)
	}

	records, _, err := c.ListLogs(ctx, insight.LogFilter{TraceID: traceID})
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("queried back %d records, want the one just shipped", len(records))
	}
	got := records[0]
	if got.Body != "sdk integration probe" {
		t.Errorf("body = %q", got.Body)
	}
	if got.SeverityText == "" {
		t.Errorf("level was not mapped to a severity: %+v", got)
	}
	if got.Attributes["probe"] != "1" {
		t.Errorf("attributes lost: %v", got.Attributes)
	}
}

func TestIntegration_ShipperDeliversThroughSlog(t *testing.T) {
	c := integrationClient(t)
	shipper := c.NewShipper(insight.WithFlushInterval(time.Hour))
	logger := slog.New(insight.NewSlogHandler(shipper, slog.LevelInfo))

	traceID := "sdkslog" + time.Now().UTC().Format("150405.000000000")
	logger.Error("shipped through slog", "trace_id", traceID, "order_id", "4711")
	if err := shipper.Close(); err != nil { // Close must flush
		t.Fatalf("Close: %v", err)
	}

	records, _, err := c.ListLogs(context.Background(), insight.LogFilter{TraceID: traceID})
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("slog record not delivered (got %d)", len(records))
	}
	if records[0].Attributes["order_id"] != "4711" {
		t.Errorf("slog attribute lost: %v", records[0].Attributes)
	}
}

func TestIntegration_ReportAndFetchBug(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	key := "sdkit-" + time.Now().UTC().Format("150405.000000000")
	bug, err := c.ReportBug(ctx, insight.NewBug{
		Title:    "sdk integration probe",
		Severity: insight.P3,
		Steps:    []string{"ran the SDK integration suite"},
		Console: []insight.LogLine{{
			Time: time.Now(), Level: "error", Source: "sdk",
			// PII the server must redact before storage.
			Message: "card 4111111111111111 for probe@victim.example",
		}},
	}, insight.IdempotencyKey(key))
	if err != nil {
		t.Fatalf("ReportBug: %v", err)
	}
	if bug.ID == "" || bug.Ref == "" {
		t.Fatalf("bug missing identifiers: %+v", bug)
	}
	t.Cleanup(func() { _ = c.DeleteBug(context.Background(), bug.ID) })

	// The same idempotency key must replay, not create a second bug.
	again, err := c.ReportBug(ctx, insight.NewBug{
		Title: "sdk integration probe", Severity: insight.P3,
		Steps: []string{"ran the SDK integration suite"},
		Console: []insight.LogLine{{
			Time: bug.Console[0].Time, Level: "error", Source: "sdk",
			Message: "card 4111111111111111 for probe@victim.example",
		}},
	}, insight.IdempotencyKey(key))
	if err == nil && again.ID != bug.ID {
		t.Errorf("idempotent retry created a second bug: %s vs %s", again.ID, bug.ID)
	}

	fetched, err := c.GetBug(ctx, bug.ID)
	if err != nil {
		t.Fatalf("GetBug: %v", err)
	}
	if fetched.Ref != bug.Ref {
		t.Errorf("ref = %q, want %q", fetched.Ref, bug.Ref)
	}

	// Triage round-trip.
	updated, err := c.UpdateBug(ctx, bug.ID, insight.StatusInProgress, "sdk@test")
	if err != nil {
		t.Fatalf("UpdateBug: %v", err)
	}
	if updated.Status != insight.StatusInProgress {
		t.Errorf("status = %q", updated.Status)
	}
}

func TestIntegration_NotFoundAndUnavailable(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	if _, err := c.GetBug(ctx, "01920000-0000-7000-8000-00000000dead"); !insight.IsNotFound(err) {
		t.Errorf("unknown bug: want not-found, got %v", err)
	}
	// Sending to an unconfigured tracker must surface as unavailable, not a
	// generic failure — the caller can tell the workspace to connect it.
	bug, err := c.ReportBug(ctx, insight.NewBug{Title: "unavailable probe", Severity: insight.P3})
	if err != nil {
		t.Fatalf("ReportBug: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteBug(context.Background(), bug.ID) })

	// Either outcome is correct and both are actionable for the caller:
	// Forbidden when the credential is an API key (bug:send is deliberately
	// NOT in the API-key scope allowlist — pushing to a tenant's tracker is a
	// human action), Unavailable when the integration simply is not connected.
	_, err = c.SendBug(ctx, bug.ID, "linear")
	if !insight.IsUnavailable(err) && !insight.IsForbidden(err) {
		t.Errorf("send without integration: want unavailable or forbidden, got %v", err)
	}
}
