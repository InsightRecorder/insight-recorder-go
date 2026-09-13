package insight

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// SlogHandler is a [log/slog] handler that ships every record to InsightRecorder
// through a [Shipper]. Compose it with the application's own handler so logs
// keep going to stdout as well:
//
//	stdout := slog.NewJSONHandler(os.Stdout, nil)
//	slog.SetDefault(slog.New(insight.MultiHandler(stdout, insight.NewSlogHandler(sh, slog.LevelInfo))))
type SlogHandler struct {
	shipper *Shipper
	level   slog.Leveler
	trace   TraceExtractor

	// preAttrs holds attributes added by WithAttrs, already qualified with the
	// groups that were open when they were added — slog requires that a later
	// WithGroup does not retroactively re-scope them.
	preAttrs map[string]string
	preTrace string
	preSpan  string
	groups   []string
}

// NewSlogHandler returns a handler shipping records at or above level.
func NewSlogHandler(s *Shipper, level slog.Leveler) *SlogHandler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &SlogHandler{shipper: s, level: level}
}

// TraceExtractor pulls the active trace and span ids out of a context, as
// lowercase hex. Returning empty strings means "no active span", which is not an
// error — plenty of log lines happen outside a request.
type TraceExtractor func(ctx context.Context) (traceID, spanID string)

// WithTraceExtractor makes the handler correlate every record with the trace it
// was written inside, so a log line and the request that produced it join on the
// server without the caller threading the id through by hand.
//
// The core module has no dependencies, and reading an OpenTelemetry span means
// importing OpenTelemetry — so the extractor is a function you supply rather
// than an import here. The one-liner lives in the companion module:
//
//	import insightotel "github.com/InsightRecorder/insight-recorder-go/otel"
//	h := insight.NewSlogHandler(sh, slog.LevelInfo).WithTraceExtractor(insightotel.TraceIDs)
//
// An id set explicitly on the record still wins: an attribute the caller wrote
// is a deliberate statement, and the ambient span is a default.
func (h *SlogHandler) WithTraceExtractor(fn TraceExtractor) *SlogHandler {
	clone := *h
	clone.trace = fn
	return &clone
}

var _ slog.Handler = (*SlogHandler)(nil)

// Enabled reports whether records at this level are shipped.
func (h *SlogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return h.shipper != nil && l >= h.level.Level()
}

// Handle queues the record. It never blocks and never returns an error: a
// logging backend that fails the caller's log call is worse than a lost line.
func (h *SlogHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.shipper == nil {
		return nil
	}
	// The ambient span first, so an explicit trace_id attribute below can
	// override it.
	traceID, spanID := h.preTrace, h.preSpan
	if h.trace != nil {
		if t, sp := h.trace(ctx); t != "" {
			traceID, spanID = t, sp
		}
	}
	e := LogEntry{
		Time:    r.Time,
		Level:   slogLevel(r.Level),
		Message: r.Message,
		TraceID: traceID,
		SpanID:  spanID,
		Attrs:   make(map[string]string, r.NumAttrs()+len(h.preAttrs)),
	}
	for k, v := range h.preAttrs {
		e.Attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		flattenAttr(e.Attrs, &e.TraceID, &e.SpanID, h.groups, a)
		return true
	})
	h.shipper.Send(e)
	return nil
}

// WithAttrs returns a handler whose records carry attrs, qualified by the
// groups open right now.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.preAttrs = make(map[string]string, len(h.preAttrs)+len(attrs))
	for k, v := range h.preAttrs {
		clone.preAttrs[k] = v
	}
	for _, a := range attrs {
		flattenAttr(clone.preAttrs, &clone.preTrace, &clone.preSpan, h.groups, a)
	}
	return &clone
}

// WithGroup returns a handler that nests subsequent attributes under name.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

// flattenAttr writes one attribute into dst as a dotted key scoped by groups,
// lifting the correlation ids (trace_id/span_id) into their dedicated fields
// so the server can link the record to its trace.
func flattenAttr(dst map[string]string, traceID, spanID *string, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := groups
		if a.Key != "" {
			sub = append(append([]string(nil), groups...), a.Key)
		}
		for _, ga := range a.Value.Group() {
			flattenAttr(dst, traceID, spanID, sub, ga)
		}
		return
	}
	key := a.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + a.Key
	}
	value := a.Value.String()
	switch key {
	case "trace_id":
		*traceID = value
	case "span_id":
		*spanID = value
	default:
		dst[key] = value
	}
}

// slogLevel maps a slog level onto the level names the ingest API recognizes.
func slogLevel(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

// multiHandler fans a record out to several handlers.
type multiHandler struct{ handlers []slog.Handler }

// MultiHandler returns a handler that writes to every given handler — the
// idiomatic way to keep logging to stdout while also shipping to InsightRecorder.
func MultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

var _ slog.Handler = (*multiHandler)(nil)

func (m *multiHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []string
	for _, h := range m.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("insight: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: out}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		out[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: out}
}
