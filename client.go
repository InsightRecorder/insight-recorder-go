package insight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Version is this SDK's release, reported in the User-Agent.
const Version = "0.1.0"

const (
	defaultTimeout      = 15 * time.Second
	defaultMaxRetries   = 3
	defaultRetryBackoff = 200 * time.Millisecond
	maxRetryBackoff     = 5 * time.Second
	maxErrorBody        = 8 << 10 // cap what we read from an error response
)

// Client talks to a InsightRecorder deployment. It is safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	httpc      *http.Client
	userAgent  string
	maxRetries int
	backoff    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient supplies the underlying HTTP client (custom transport,
// proxy, TLS config…). Its Timeout wins over [WithTimeout].
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpc = h
		}
	}
}

// WithTimeout sets the per-request timeout (default 15s).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.httpc.Timeout = d
		}
	}
}

// WithUserAgent appends a caller identifier to the SDK's User-Agent.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua + " " + c.userAgent
		}
	}
}

// WithMaxRetries sets how many times a retryable failure (429, 5xx, network
// error) is re-attempted; 0 disables retrying. Default 3.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

// WithRetryBackoff sets the base delay for exponential backoff (default
// 200ms, doubling per attempt, capped at 5s). A server-sent Retry-After
// always wins when it is longer.
func WithRetryBackoff(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.backoff = d
		}
	}
}

// New returns a Client for the deployment at baseURL authenticating with
// apiKey. Prefer a long-lived API key ("crk_…") from Settings → API keys:
// user JWTs expire after an hour and are not meant for machines.
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("insight: baseURL is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("insight: invalid baseURL %q", baseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("insight: baseURL scheme %q must be http or https", u.Scheme)
	}
	if apiKey == "" {
		return nil, errors.New("insight: apiKey is required")
	}
	// Reject control characters and whitespace in the credential. Three of the
	// sibling SDKs sit on HTTP stacks that do not validate header values, so a
	// CRLF here would become header injection; the same check everywhere keeps
	// the guarantee uniform. It also catches the common accident — a trailing
	// newline from a copy-paste — as a clear error instead of a confusing 401.
	if strings.ContainsFunc(apiKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return nil, errors.New("insight: apiKey contains whitespace or control characters")
	}

	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpc:      &http.Client{Timeout: defaultTimeout},
		userAgent:  "insight-recorder-go/" + Version,
		maxRetries: defaultMaxRetries,
		backoff:    defaultRetryBackoff,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// CallOption customizes a single request.
type CallOption func(*http.Request)

// IdempotencyKey makes a resource-creating POST safe to retry: the first
// response is stored and replayed for later requests with the same key.
// Reusing a key with a different body returns a conflict ([IsConflict]).
func IdempotencyKey(key string) CallOption {
	return func(r *http.Request) {
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
	}
}

// header sets an arbitrary header on one request.
func header(k, v string) CallOption {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

// do performs an API call, retrying retryable failures with exponential
// backoff. body is JSON-encoded when non-nil; out is JSON-decoded when
// non-nil. rawBody, when set, is sent verbatim (used for NDJSON ingest).
func (c *Client) do(ctx context.Context, method, path string, body, out any, opts ...CallOption) error {
	var payload []byte
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case rawBody:
		payload, contentType = b.data, b.contentType
	default:
		enc, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("insight: encode request: %w", err)
		}
		payload = enc
	}

	endpoint := method + " " + path
	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("insight: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", contentType)
		}
		for _, opt := range opts {
			opt(req)
		}

		res, err := c.httpc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("insight: %s: %w", endpoint, err)
			if attempt >= c.maxRetries || ctx.Err() != nil {
				return lastErr
			}
			if werr := wait(ctx, c.backoffFor(attempt, 0)); werr != nil {
				return werr
			}
			continue
		}

		if res.StatusCode >= 200 && res.StatusCode < 300 {
			defer res.Body.Close()
			if out == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				return nil
			}
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				return fmt.Errorf("insight: %s: decode response: %w", endpoint, err)
			}
			return nil
		}

		apiErr := parseAPIError(res, endpoint)
		_ = res.Body.Close()
		if !retryable(res.StatusCode) || attempt >= c.maxRetries {
			return apiErr
		}
		lastErr = apiErr
		if werr := wait(ctx, c.backoffFor(attempt, apiErr.RetryAfter)); werr != nil {
			return werr
		}
	}
}

// rawBody carries a pre-encoded payload (NDJSON log batches).
type rawBody struct {
	data        []byte
	contentType string
}

// retryable reports whether a status is worth re-attempting.
func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoffFor returns the delay before the next attempt: exponential from the
// configured base, but never shorter than a server-sent Retry-After.
func (c *Client) backoffFor(attempt int, retryAfter time.Duration) time.Duration {
	d := c.backoff << attempt
	if d > maxRetryBackoff {
		d = maxRetryBackoff
	}
	if retryAfter > d {
		d = retryAfter
		if d > maxRetryBackoff {
			d = maxRetryBackoff
		}
	}
	return d
}

// wait sleeps for d unless ctx is cancelled first.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// parseAPIError builds an APIError from a non-2xx response.
func parseAPIError(res *http.Response, endpoint string) *APIError {
	apiErr := &APIError{StatusCode: res.StatusCode, Endpoint: endpoint}
	if secs, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && secs > 0 {
		apiErr.RetryAfter = time.Duration(secs) * time.Second
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error != "" {
		apiErr.Message = envelope.Error
	} else if msg := strings.TrimSpace(string(raw)); msg != "" {
		apiErr.Message = msg
	}
	return apiErr
}

// Health reports whether the deployment is reachable and serving. It needs no
// credentials, so it also works as a connectivity check for a wrong base URL.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil)
}
