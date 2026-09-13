package insight

import "time"

// Severity ranks how badly a bug affects users. P0 is the most severe.
type Severity string

// Severity levels accepted by the API.
const (
	P0 Severity = "P0" // critical: outage or data loss
	P1 Severity = "P1" // high: major feature broken
	P2 Severity = "P2" // medium: degraded experience
	P3 Severity = "P3" // low: cosmetic or minor
)

// Bug lifecycle states.
const (
	StatusTriage     = "Triage"
	StatusInProgress = "In progress"
	StatusFixed      = "Fixed"
)

// Bug is a captured bug report as stored by InsightRecorder.
type Bug struct {
	ID         string      `json:"id"`
	Ref        string      `json:"ref"` // human-facing reference, e.g. "BUG-2147"
	Title      string      `json:"title"`
	Severity   Severity    `json:"severity"`
	Status     string      `json:"status"`
	URL        string      `json:"url"`
	CapturedAt time.Time   `json:"captured_at"`
	Owner      string      `json:"owner"`
	SyncRef    string      `json:"sync_ref"` // external ticket, e.g. "BUG-123"
	Steps      []string    `json:"steps"`
	Console    []LogLine   `json:"console"`
	Network    []NetCall   `json:"network"`
	Meta       SessionMeta `json:"meta"`
	AISummary  AISummary   `json:"ai_summary"`
}

// NewBug is the payload for creating a bug. Only Title and Severity are
// required; the capture fields are optional context.
type NewBug struct {
	Title    string      `json:"title"`
	Severity Severity    `json:"severity"`
	URL      string      `json:"url,omitempty"`
	Steps    []string    `json:"steps,omitempty"`
	Console  []LogLine   `json:"console,omitempty"`
	Network  []NetCall   `json:"network,omitempty"`
	Meta     SessionMeta `json:"meta"`
}

// LogLine is one captured log entry attached to a bug.
type LogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`  // "info" | "warn" | "error"
	Source  string    `json:"source"` // originating file/module
	Message string    `json:"message"`
}

// NetCall is one captured network request/response attached to a bug.
type NetCall struct {
	Time    time.Time     `json:"time"`
	Method  string        `json:"method"`
	URL     string        `json:"url"`
	Status  string        `json:"status"`
	Size    string        `json:"size"`
	Timing  time.Duration `json:"timing"` // nanoseconds on the wire
	IsError bool          `json:"is_error"`
}

// SessionMeta describes the environment a bug was captured in. For a Go
// service the browser-shaped fields are repurposed: Build for the version,
// Flags for feature flags, Session for the request or trace id.
type SessionMeta struct {
	Browser  string `json:"browser,omitempty"` // e.g. "go1.22.3"
	OS       string `json:"os,omitempty"`      // e.g. "linux/amd64"
	Viewport string `json:"viewport,omitempty"`
	Build    string `json:"build,omitempty"`
	Flags    string `json:"flags,omitempty"`
	User     string `json:"user,omitempty"`
	Locale   string `json:"locale,omitempty"`
	Network  string `json:"network,omitempty"`
	Session  string `json:"session,omitempty"`
}

// AISummary is the generated triage context for a bug.
type AISummary struct {
	TLDR          string   `json:"tldr"`
	LikelyCause   string   `json:"likely_cause"`
	SuggestedFix  string   `json:"suggested_fix"`
	AffectedFiles []string `json:"affected_files"`
	Frequency     string   `json:"frequency"`
}

// LogRecord is an ingested log record as returned by queries (the canonical
// OpenTelemetry Logs Data Model shape).
type LogRecord struct {
	Timestamp         time.Time         `json:"timestamp"`
	ObservedTimestamp time.Time         `json:"observed_timestamp"`
	SeverityText      string            `json:"severity_text"`
	SeverityNumber    int               `json:"severity_number"`
	Body              string            `json:"body"`
	Attributes        map[string]string `json:"attributes"`
	Resource          map[string]string `json:"resource"`
	TraceID           string            `json:"trace_id"`
	SpanID            string            `json:"span_id"`
	Format            string            `json:"format"`
}

// LogAnalysis is the AI's verdict over a window of ingested logs.
type LogAnalysis struct {
	Incident          string   `json:"incident"` // label, or "No incident detected"
	Severity          string   `json:"severity"` // critical|high|medium|low|info
	Confidence        string   `json:"confidence"`
	Summary           string   `json:"summary"`
	Evidence          []string `json:"evidence"`
	RecommendedAction string   `json:"recommended_action"`
	Analyzed          int      `json:"analyzed"`
}

// Page is the pagination block returned by list endpoints.
type Page struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

// IngestResult reports how many log records were stored and how many were
// rejected as unparseable (rejected records are dead-lettered, never dropped).
type IngestResult struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}
