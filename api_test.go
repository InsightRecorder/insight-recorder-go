package insight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apiStub records the last request and replies with a canned body.
type apiStub struct {
	method, path, rawURI, query, idempotency string
	body                                     map[string]any
	reply                                    string
	status                                   int
	srv                                      *httptest.Server
}

func newAPIStub(t *testing.T, reply string) *apiStub {
	t.Helper()
	s := &apiStub{reply: reply, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.path, s.query = r.Method, r.URL.Path, r.URL.RawQuery
		s.rawURI = r.RequestURI
		s.idempotency = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&s.body)
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.reply))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func TestReportBug(t *testing.T) {
	stub := newAPIStub(t, `{"id":"01a0","ref":"BUG-7F3A2C","title":"boom","severity":"P1"}`)
	c := testClient(t, stub.srv)

	bug, err := c.ReportBug(context.Background(), NewBug{Title: "boom", Severity: P1},
		IdempotencyKey("order-42"))
	if err != nil {
		t.Fatalf("ReportBug: %v", err)
	}
	if bug.Ref != "BUG-7F3A2C" {
		t.Errorf("ref = %q", bug.Ref)
	}
	if stub.method != http.MethodPost || stub.path != "/api/bugs" {
		t.Errorf("called %s %s", stub.method, stub.path)
	}
	if stub.idempotency != "order-42" {
		t.Errorf("Idempotency-Key = %q", stub.idempotency)
	}
	if stub.body["title"] != "boom" || stub.body["severity"] != "P1" {
		t.Errorf("payload = %v", stub.body)
	}
}

func TestReportBug_DefaultsSeverityAndRequiresTitle(t *testing.T) {
	stub := newAPIStub(t, `{"id":"1"}`)
	c := testClient(t, stub.srv)

	if _, err := c.ReportBug(context.Background(), NewBug{}); err == nil {
		t.Error("empty title must be rejected client-side")
	}
	if _, err := c.ReportBug(context.Background(), NewBug{Title: "x"}); err != nil {
		t.Fatalf("ReportBug: %v", err)
	}
	if stub.body["severity"] != string(P2) {
		t.Errorf("severity default = %v, want P2", stub.body["severity"])
	}
}

func TestBugLifecycleCalls(t *testing.T) {
	stub := newAPIStub(t, `{"id":"abc","sync_ref":"FE-1"}`)
	c := testClient(t, stub.srv)
	ctx := context.Background()

	if _, err := c.GetBug(ctx, "abc"); err != nil || stub.method != http.MethodGet {
		t.Errorf("GetBug: %v (%s %s)", err, stub.method, stub.path)
	}
	if _, err := c.UpdateBug(ctx, "abc", StatusFixed, "eng@x.com"); err != nil || stub.method != http.MethodPatch {
		t.Errorf("UpdateBug: %v (%s)", err, stub.method)
	}
	if stub.body["status"] != StatusFixed {
		t.Errorf("status payload = %v", stub.body)
	}
	if _, err := c.SendBug(ctx, "abc", "linear"); err != nil || !strings.HasSuffix(stub.path, "/send") {
		t.Errorf("SendBug: %v (%s)", err, stub.path)
	}
	if stub.body["dest"] != "linear" {
		t.Errorf("dest payload = %v", stub.body)
	}
	if _, err := c.SummarizeBug(ctx, "abc"); err != nil || !strings.HasSuffix(stub.path, "/summarize") {
		t.Errorf("SummarizeBug: %v (%s)", err, stub.path)
	}
	if err := c.DeleteBug(ctx, "abc"); err != nil || stub.method != http.MethodDelete {
		t.Errorf("DeleteBug: %v (%s)", err, stub.method)
	}
	// Empty ids never reach the network.
	if _, err := c.GetBug(ctx, ""); err == nil {
		t.Error("empty id must be rejected")
	}
}

func TestBugIDsArePathEscaped(t *testing.T) {
	stub := newAPIStub(t, `{}`)
	c := testClient(t, stub.srv)
	if _, err := c.GetBug(context.Background(), "a/../../etc/passwd"); err != nil {
		t.Fatalf("GetBug: %v", err)
	}
	// The server decodes r.URL.Path, so assert on the raw request URI: the id
	// must have travelled percent-encoded, never as real path separators.
	if !strings.Contains(stub.rawURI, "%2F") {
		t.Errorf("bug id was not path-escaped on the wire: %q", stub.rawURI)
	}
}

func TestListBugs_EncodesFilter(t *testing.T) {
	stub := newAPIStub(t, `{"data":[{"id":"1","title":"a"}],"pagination":{"limit":50,"offset":0,"total":1}}`)
	c := testClient(t, stub.srv)

	bugs, page, err := c.ListBugs(context.Background(), BugFilter{
		Severity: P0, Owner: "me@x.com", Query: "checkout", Sort: "newest", Limit: 50,
	})
	if err != nil {
		t.Fatalf("ListBugs: %v", err)
	}
	if len(bugs) != 1 || page.Total != 1 {
		t.Errorf("bugs=%d page=%+v", len(bugs), page)
	}
	for _, want := range []string{"filter=P0", "owner=me%40x.com", "q=checkout", "sort=newest", "limit=50"} {
		if !strings.Contains(stub.query, want) {
			t.Errorf("query %q missing %q", stub.query, want)
		}
	}
}

func TestListLogs_AndAnalyze(t *testing.T) {
	stub := newAPIStub(t, `{"data":[{"body":"boom","severity_text":"ERROR"}],"pagination":{"total":1}}`)
	c := testClient(t, stub.srv)

	recs, _, err := c.ListLogs(context.Background(), LogFilter{TraceID: "abc", Severity: "ERROR", Limit: 10})
	if err != nil || len(recs) != 1 || recs[0].Body != "boom" {
		t.Fatalf("ListLogs = (%v, %v)", recs, err)
	}
	if !strings.Contains(stub.query, "trace_id=abc") {
		t.Errorf("query = %q", stub.query)
	}

	stub.reply = `{"incident":"DDoS","severity":"high","analyzed":300}`
	an, err := c.AnalyzeLogs(context.Background())
	if err != nil || an.Incident != "DDoS" || an.Analyzed != 300 {
		t.Fatalf("AnalyzeLogs = (%+v, %v)", an, err)
	}
	if stub.path != "/api/logs/analyze" {
		t.Errorf("path = %q", stub.path)
	}
}

func TestEmptyFilterSendsNoQuery(t *testing.T) {
	stub := newAPIStub(t, `{"data":[],"pagination":{}}`)
	c := testClient(t, stub.srv)
	if _, _, err := c.ListBugs(context.Background(), BugFilter{}); err != nil {
		t.Fatal(err)
	}
	if stub.query != "" {
		t.Errorf("zero filter should send no query, got %q", stub.query)
	}
}
