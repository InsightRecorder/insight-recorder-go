package zerolog

import (
	"testing"
	"time"

	insight "github.com/InsightRecorder/insight-recorder-go"
	"github.com/rs/zerolog"
)

func TestWithFieldNames_OverridesAndSkipsBlanks(t *testing.T) {
	w := NewWriter(nil,
		WithFieldNames("lvl", "msg", "ts"),
		WithFieldNames("", "", ""), // blanks are no-ops, must not clear the above
	)
	if w.levelKey != "lvl" || w.messageKey != "msg" || w.timeKey != "ts" {
		t.Fatalf("field names = %q/%q/%q", w.levelKey, w.messageKey, w.timeKey)
	}
}

func TestParseTime_Formats(t *testing.T) {
	rfc := "2023-05-04T10:00:00Z"
	if got := parseTime(rfc); got.IsZero() {
		t.Errorf("RFC3339 not parsed")
	}
	if got := parseTime("2023-05-04T10:00:00.5Z"); got.IsZero() {
		t.Errorf("RFC3339Nano not parsed")
	}
	if got := parseTime(float64(1_700_000_000)); got.Unix() != 1_700_000_000 {
		t.Errorf("unix seconds = %v", got.Unix())
	}
	if got := parseTime("not-a-time"); !got.IsZero() {
		t.Errorf("bad string should be zero, got %v", got)
	}
	if got := parseTime(true); !got.IsZero() {
		t.Errorf("unknown type should be zero, got %v", got)
	}
}

func TestStringify_AllKinds(t *testing.T) {
	cases := map[string]struct {
		in   any
		want string
	}{
		"string":     {"hi", "hi"},
		"bool":       {true, "true"},
		"int float":  {float64(42), "42"},
		"real float": {float64(3.5), "3.5"},
		"nil":        {nil, ""},
		"object":     {map[string]any{"a": 1}, `{"a":1}`},
		"array":      {[]any{1.0, 2.0}, "[1,2]"},
	}
	for name, tc := range cases {
		if got := stringify(tc.in); got != tc.want {
			t.Errorf("%s: stringify = %q, want %q", name, got, tc.want)
		}
	}
}

func TestWriteLevel_MapsThroughWriteWithCustomKeys(t *testing.T) {
	col := newCollector(t)
	client, err := insight.New(col.srv.URL, "crk_test")
	if err != nil {
		t.Fatal(err)
	}
	sh := client.NewShipper(insight.WithBatchSize(1000), insight.WithFlushInterval(time.Hour))
	w := NewWriter(sh, WithFieldNames("severity", "msg", "ts"))

	// WriteLevel is what zerolog.LevelWriter calls; it maps the custom keys and
	// the numeric time, and lifts trace_id/span_id.
	line := []byte(`{"severity":"error","msg":"boom","ts":1700000000,"trace_id":"abc","span_id":"def","k":"v"}`)
	if n, err := w.WriteLevel(zerolog.ErrorLevel, line); err != nil || n != len(line) {
		t.Fatalf("WriteLevel = %d, %v", n, err)
	}
	if err := sh.Close(); err != nil {
		t.Fatal(err)
	}
	recs := col.all()
	if len(recs) != 1 {
		t.Fatalf("shipped %d records, want 1", len(recs))
	}
	r := recs[0]
	if r["body"] != "boom" && r["message"] != "boom" {
		t.Errorf("custom message key not mapped: %v", r)
	}
}

func TestWriteLevel_NilShipperIsInert(t *testing.T) {
	w := NewWriter(nil, WithFieldNames("l", "m", "t"))
	p := []byte(`{"m":"x"}`)
	if n, err := w.WriteLevel(zerolog.InfoLevel, p); err != nil || n != len(p) {
		t.Fatalf("WriteLevel nil shipper = %d, %v", n, err)
	}
}
