package insight

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
)

// ReportBug files a bug report. Title is required; Severity defaults to P2
// when empty. Pass [IdempotencyKey] to make a retry safe.
//
//	bug, err := c.ReportBug(ctx, insight.NewBug{
//		Title:    "checkout failed",
//		Severity: insight.P1,
//	}, insight.IdempotencyKey(orderID))
func (c *Client) ReportBug(ctx context.Context, b NewBug, opts ...CallOption) (*Bug, error) {
	if b.Title == "" {
		return nil, errors.New("insight: bug title is required")
	}
	if b.Severity == "" {
		b.Severity = P2
	}
	var out Bug
	if err := c.do(ctx, http.MethodPost, "/api/bugs", b, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetBug fetches one bug by id. Bugs belonging to another workspace report as
// not found ([IsNotFound]) — isolation is not distinguishable from absence.
func (c *Client) GetBug(ctx context.Context, id string) (*Bug, error) {
	if id == "" {
		return nil, errors.New("insight: bug id is required")
	}
	var out Bug
	if err := c.do(ctx, http.MethodGet, "/api/bugs/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateBug sets a bug's triage status and owner.
func (c *Client) UpdateBug(ctx context.Context, id, status, owner string) (*Bug, error) {
	if id == "" {
		return nil, errors.New("insight: bug id is required")
	}
	body := struct {
		Status string `json:"status"`
		Owner  string `json:"owner"`
	}{status, owner}
	var out Bug
	if err := c.do(ctx, http.MethodPatch, "/api/bugs/"+url.PathEscape(id), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBug permanently removes a bug and every artifact attached to it.
func (c *Client) DeleteBug(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("insight: bug id is required")
	}
	return c.do(ctx, http.MethodDelete, "/api/bugs/"+url.PathEscape(id), nil, nil)
}

// SendBug pushes a report to a configured tracker ("github", "linear",
// "jira", "notion", "asana", "clickup", "trello"), returning the bug with its
// sync reference set. Returns an [IsUnavailable] error when that integration
// is missing or disabled for the workspace.
func (c *Client) SendBug(ctx context.Context, id, destination string, opts ...CallOption) (*Bug, error) {
	if id == "" || destination == "" {
		return nil, errors.New("insight: bug id and destination are required")
	}
	body := struct {
		Destination string `json:"dest"`
	}{destination}
	var out Bug
	if err := c.do(ctx, http.MethodPost, "/api/bugs/"+url.PathEscape(id)+"/send", body, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// SummarizeBug asks the workspace's configured LLM to triage the bug and
// stores the result. Returns an [IsUnavailable] error when no AI provider is
// configured.
func (c *Client) SummarizeBug(ctx context.Context, id string) (*Bug, error) {
	if id == "" {
		return nil, errors.New("insight: bug id is required")
	}
	var out Bug
	if err := c.do(ctx, http.MethodPost, "/api/bugs/"+url.PathEscape(id)+"/summarize", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Stack returns the current goroutine's stack trace, skipping the innermost
// skip frames. Useful when building a bug report by hand.
func Stack(skip int) string {
	buf := make([]byte, 16<<10)
	n := runtime.Stack(buf, false)
	return trimFrames(string(buf[:n]), skip)
}

// trimFrames drops the first n stack frames (two lines each) after the
// goroutine header, so SDK plumbing does not dominate the report.
func trimFrames(stack string, n int) string {
	if n <= 0 {
		return stack
	}
	head, rest, ok := cut(stack, "\n")
	if !ok {
		return stack
	}
	for ; n > 0; n-- {
		_, next, ok := cut(rest, "\n")
		if !ok {
			break
		}
		_, next, ok = cut(next, "\n")
		if !ok {
			break
		}
		rest = next
	}
	return head + "\n" + rest
}

// cut is strings.Cut, inlined to keep this file's imports minimal.
func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// runtimeMeta describes the running process for a bug report.
func runtimeMeta(service, session string) SessionMeta {
	return SessionMeta{
		Browser: runtime.Version(),
		OS:      runtime.GOOS + "/" + runtime.GOARCH,
		Build:   service,
		Session: session,
		Flags:   "goroutines=" + strconv.Itoa(runtime.NumGoroutine()),
	}
}
