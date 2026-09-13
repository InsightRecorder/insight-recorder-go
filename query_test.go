package insight

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The DSL and the window have to reach the wire, or the documentation above
// LogFilter is a promise the client does not keep.
func TestLogFilter_SendsTheDSLAndTheWindow(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[],"total":0}`))
	}))
	defer srv.Close()

	c := testClient(t, srv)
	_, _, err := c.ListLogs(context.Background(), LogFilter{
		Query: `service:payments status:>=500 "connection refused"`,
		Since: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}

	values, _ := url.ParseQuery(got)
	if values.Get("q") != `service:payments status:>=500 "connection refused"` {
		t.Errorf("q = %q — the DSL must reach the server verbatim", values.Get("q"))
	}
	if values.Get("since") != "15m0s" {
		t.Errorf("since = %q, want a duration the server can parse", values.Get("since"))
	}
}
