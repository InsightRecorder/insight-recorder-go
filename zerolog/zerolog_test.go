package zerolog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	insight "github.com/InsightRecorder/insight-recorder-go"
	"github.com/rs/zerolog"
)

// shipped collects the records a shipper delivered.
type collector struct {
	mu      sync.Mutex
	records []map[string]any
	srv     *httptest.Server
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sc := bufio.NewScanner(bytes.NewReader(body))
		c.mu.Lock()
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err == nil {
				c.records = append(c.records, m)
			}
		}
		c.mu.Unlock()
		_, _ = w.Write([]byte(`{"accepted":1,"rejected":0}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) all() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.records...)
}

func TestWriter_ShipsZerologEvents(t *testing.T) {
	col := newCollector(t)
	client, err := insight.New(col.srv.URL, "crk_test")
	if err != nil {
		t.Fatal(err)
	}
	sh := client.NewShipper(insight.WithBatchSize(1000), insight.WithFlushInterval(time.Hour))

	logger := zerolog.New(zerolog.MultiLevelWriter(io.Discard, NewWriter(sh))).
		With().Timestamp().Logger()
	logger.Error().Str("trace_id", "abc123").Str("order_id", "42").Int("attempt", 3).
		Bool("retry", true).Msg("checkout failed")

	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}
	records := col.all()
	if len(records) != 1 {
		t.Fatalf("shipped %d records, want 1", len(records))
	}
	r := records[0]
	if r["level"] != "error" || r["message"] != "checkout failed" {
		t.Errorf("level/message wrong: %v", r)
	}
	if r["trace_id"] != "abc123" {
		t.Errorf("trace_id not lifted: %v", r)
	}
	if r["order_id"] != "42" || r["attempt"] != "3" || r["retry"] != "true" {
		t.Errorf("attributes wrong: %v", r)
	}
	if _, ok := r["time"]; !ok {
		t.Errorf("timestamp missing: %v", r)
	}
}

func TestWriter_KeepsStdoutWorking(t *testing.T) {
	col := newCollector(t)
	client, _ := insight.New(col.srv.URL, "crk_test")
	sh := client.NewShipper(insight.WithFlushInterval(time.Hour))
	defer sh.Close()

	var stdout bytes.Buffer
	logger := zerolog.New(zerolog.MultiLevelWriter(&stdout, NewWriter(sh)))
	logger.Info().Msg("also local")

	if !bytes.Contains(stdout.Bytes(), []byte("also local")) {
		t.Errorf("local sink lost the line: %q", stdout.String())
	}
}

func TestWriter_UnparseableInputIsNotDropped(t *testing.T) {
	col := newCollector(t)
	client, _ := insight.New(col.srv.URL, "crk_test")
	sh := client.NewShipper(insight.WithBatchSize(1000), insight.WithFlushInterval(time.Hour))

	w := NewWriter(sh)
	if _, err := w.Write([]byte("not json at all")); err != nil {
		t.Fatalf("Write must not fail the logging call: %v", err)
	}
	_ = sh.Close()

	records := col.all()
	if len(records) != 1 || records[0]["message"] != "not json at all" {
		t.Errorf("raw line not preserved: %v", records)
	}
}

func TestWriter_NilShipperIsInert(t *testing.T) {
	w := NewWriter(nil)
	if n, err := w.Write([]byte(`{"level":"info"}`)); err != nil || n == 0 {
		t.Errorf("nil shipper must be a silent no-op: (%d, %v)", n, err)
	}
}

// Example shows the intended wiring.
func ExampleNewWriter() {
	client, _ := insight.New("https://insightrecorder.example.com", os.Getenv("INSIGHT_API_KEY"))
	sh := client.NewShipper()
	defer sh.Close()

	logger := zerolog.New(zerolog.MultiLevelWriter(os.Stdout, NewWriter(sh))).
		With().Timestamp().Logger()
	logger.Error().Str("trace_id", "abc").Msg("checkout failed")
}
