// Package zerolog adapts github.com/rs/zerolog to the InsightRecorder SDK: every
// log event written through the returned writer is also shipped to InsightRecorder.
//
//	sh := client.NewShipper()
//	defer sh.Close()
//
//	log.Logger = zerolog.New(zerolog.MultiLevelWriter(
//		os.Stdout,
//		insightzerolog.NewWriter(sh),
//	)).With().Timestamp().Logger()
//
// Keeping stdout in the MultiLevelWriter matters: shipping is best-effort, so
// the local log stays the source of truth for the running process.
package zerolog

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	insight "github.com/InsightRecorder/insight-recorder-go"
)

// Writer is an io.Writer accepting zerolog's JSON events and forwarding them
// to a Shipper. Writes never block and never fail the logging call.
type Writer struct {
	shipper *insight.Shipper
	// levelKey, messageKey and timeKey mirror zerolog's global field names,
	// captured at construction so a custom configuration still maps correctly.
	levelKey, messageKey, timeKey string
}

// Option configures a Writer.
type Option func(*Writer)

// WithFieldNames overrides the zerolog field names when the application has
// customized zerolog.LevelFieldName and friends.
func WithFieldNames(level, message, timestamp string) Option {
	return func(w *Writer) {
		if level != "" {
			w.levelKey = level
		}
		if message != "" {
			w.messageKey = message
		}
		if timestamp != "" {
			w.timeKey = timestamp
		}
	}
}

// NewWriter returns a zerolog writer that ships events through s.
func NewWriter(s *insight.Shipper, opts ...Option) *Writer {
	w := &Writer{shipper: s, levelKey: "level", messageKey: "message", timeKey: "time"}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Write parses one zerolog JSON event and queues it. Unparseable input is
// shipped as a plain message rather than dropped.
func (w *Writer) Write(p []byte) (int, error) {
	if w.shipper == nil {
		return len(p), nil
	}
	var fields map[string]any
	if err := json.Unmarshal(p, &fields); err != nil {
		w.shipper.Send(insight.LogEntry{Message: string(p)})
		return len(p), nil
	}

	entry := insight.LogEntry{Attrs: make(map[string]string, len(fields))}
	for k, v := range fields {
		switch k {
		case w.levelKey:
			entry.Level, _ = v.(string)
		case w.messageKey:
			entry.Message, _ = v.(string)
		case w.timeKey:
			entry.Time = parseTime(v)
		case "trace_id":
			entry.TraceID = stringify(v)
		case "span_id":
			entry.SpanID = stringify(v)
		default:
			entry.Attrs[k] = stringify(v)
		}
	}
	w.shipper.Send(entry)
	return len(p), nil
}

// WriteLevel satisfies zerolog.LevelWriter so the level survives even when the
// event carries no level field.
func (w *Writer) WriteLevel(level interface{ String() string }, p []byte) (int, error) {
	return w.Write(p)
}

// parseTime accepts zerolog's RFC3339 strings and its numeric Unix formats.
func parseTime(v any) time.Time {
	switch t := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	case float64:
		return time.Unix(int64(t), 0)
	}
	return time.Time{}
}

// stringify renders a JSON value as a flat attribute string.
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		if raw, err := json.Marshal(t); err == nil {
			return string(raw)
		}
		return ""
	}
}

// Ctx returns a zerolog logger whose events carry the trace of the context they
// were written inside.
//
// A zerolog writer sees bytes, never a context — by the time Write is called the
// event is already encoded, so the correlation cannot be added there the way the
// slog handler does it. It has to come from the logger, which is what this
// helper is for:
//
//	import (
//	    insightotel "github.com/InsightRecorder/insight-recorder-go/otel"
//	    insightzerolog "github.com/InsightRecorder/insight-recorder-go/zerolog"
//	)
//
//	insightzerolog.Ctx(ctx, log.Logger, insightotel.TraceIDs).
//	    Error().Msg("checkout failed")
//
// Without an extractor, or outside a span, the logger is returned untouched —
// the line still ships, just without a trace to join it to.
func Ctx(ctx context.Context, l zerolog.Logger, extract func(context.Context) (string, string)) zerolog.Logger {
	if extract == nil {
		return l
	}
	traceID, spanID := extract(ctx)
	if traceID == "" {
		return l
	}
	c := l.With().Str("trace_id", traceID)
	if spanID != "" {
		c = c.Str("span_id", spanID)
	}
	return c.Logger()
}
