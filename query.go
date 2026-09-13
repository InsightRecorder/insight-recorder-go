package insight

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// BugFilter narrows a bug listing. The zero value lists the most recent bugs.
type BugFilter struct {
	Severity Severity // P0..P3
	Owner    string
	Query    string // word or phrase over title and ref (full-text, not substring)
	Sort     string // newest|oldest|severity
	Limit    int    // 1..200 (default 50)
	Offset   int
}

func (f BugFilter) values() url.Values {
	v := url.Values{}
	if f.Severity != "" {
		v.Set("filter", string(f.Severity))
	}
	if f.Owner != "" {
		v.Set("owner", f.Owner)
	}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.Sort != "" {
		v.Set("sort", f.Sort)
	}
	if f.Limit > 0 {
		v.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		v.Set("offset", strconv.Itoa(f.Offset))
	}
	return v
}

// ListBugs returns a page of bugs for the workspace. Console and network
// captures are redacted unless the credential holds the replay:view
// permission.
func (c *Client) ListBugs(ctx context.Context, f BugFilter) ([]Bug, Page, error) {
	var out struct {
		Data       []Bug `json:"data"`
		Pagination Page  `json:"pagination"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/bugs"+encodeQuery(f.values()), nil, &out); err != nil {
		return nil, Page{}, err
	}
	return out.Data, out.Pagination, nil
}

// LogFilter narrows a log query. The zero value returns the most recent
// records.
type LogFilter struct {
	TraceID  string // correlate a request across services
	Severity string // INFO, ERROR, …
	// Query is the log search DSL, not a substring match:
	//
	//	service:payments level:error "connection refused"
	//	http.status_code:>=500 NOT k8s.namespace:staging
	//	level:(error,fatal) EXISTS user.id
	//
	// Unknown names are read as attribute or resource paths, which is how
	// http.status_code works without being enumerated. OR works between values
	// of one field; OR across different fields is rejected with a message
	// rather than quietly reinterpreted as AND.
	//
	// Matching is by word and phrase (PostgreSQL full-text), so a partial token
	// does not match: "conn" will not find "connection". Search an id with its
	// own field — TraceID here — rather than as text.
	Query  string
	Format string // otlp|json|syslog|cef|leef|gelf
	// Since bounds the window, e.g. 15*time.Minute. Zero means "no lower
	// bound", which on a busy workspace means the newest Limit records.
	Since  time.Duration
	Limit  int // default 100
	Offset int
}

func (f LogFilter) values() url.Values {
	v := url.Values{}
	if f.TraceID != "" {
		v.Set("trace_id", f.TraceID)
	}
	if f.Severity != "" {
		v.Set("severity", f.Severity)
	}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.Since > 0 {
		v.Set("since", f.Since.String())
	}
	if f.Format != "" {
		v.Set("format", f.Format)
	}
	if f.Limit > 0 {
		v.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		v.Set("offset", strconv.Itoa(f.Offset))
	}
	return v
}

// ListLogs returns a page of ingested log records. Requires the logs:read
// permission.
func (c *Client) ListLogs(ctx context.Context, f LogFilter) ([]LogRecord, Page, error) {
	var out struct {
		Data       []LogRecord `json:"data"`
		Pagination Page        `json:"pagination"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/logs"+encodeQuery(f.values()), nil, &out); err != nil {
		return nil, Page{}, err
	}
	return out.Data, out.Pagination, nil
}

// AnalyzeLogs asks the workspace's LLM to identify the single most
// significant incident across a recent window of logs (DDoS, connection-pool
// exhaustion, auth abuse, error spikes). Requires logs:read and a configured
// AI provider ([IsUnavailable] otherwise).
func (c *Client) AnalyzeLogs(ctx context.Context) (*LogAnalysis, error) {
	var out LogAnalysis
	if err := c.do(ctx, http.MethodPost, "/api/logs/analyze", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// encodeQuery renders values as a leading-"?" query string, or "" when empty.
func encodeQuery(v url.Values) string {
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
