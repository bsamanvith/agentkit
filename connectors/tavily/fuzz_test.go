package tavily_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// fixedBody is a transport that answers every request with one status and
// body, without a network.
type fixedBody struct {
	status int
	body   []byte
}

func (f fixedBody) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: f.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(f.body)),
		Request:    req,
	}, nil
}

// fuzzDecode calls a tool with the canary key against an upstream that
// answers status and body, and checks: no panic; an error is ErrInvalidInput
// or ErrToolFailed; an output is the E10 shape within its size bound; the
// key appears nowhere.
func fuzzDecode(t *testing.T, name, input string, status int, body []byte) {
	if status < 200 || status > 599 || status == http.StatusTooManyRequests {
		status = 200 // a 429 would be retried after a wait on the real clock
	}
	tools, err := tavily.Factory(
		tavily.WithHTTPClient(&http.Client{Transport: fixedBody{status, body}}),
		tavily.WithBaseURL("http://tavily.test"),
		tavily.WithLookupEnv(func(string) (string, bool) { return canary, true }),
	).Build(t.Context(), json.RawMessage(`{"search_defaults": {"include_answer": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reg.Invoke(t.Context(), name, json.RawMessage(input))
	switch {
	case errors.Is(err, tool.ErrPanic):
		t.Fatalf("panic: %v", err)
	case err != nil && !isInvalidInput(err) && !isToolFailed(err):
		t.Fatalf("err = %v: neither invalid input nor a tool failure", err)
	case err != nil && strings.Contains(err.Error(), canary):
		t.Fatalf("the key is in the error %q", err)
	case err != nil:
		return
	}
	if err := checkShape(name, out); err != nil {
		t.Fatalf("output %s: %v", out, err)
	}
	if err := validOutput(outputSchemas(t, tools), name, out); err != nil {
		t.Fatalf("output %s does not validate against its output schema: %v", out, err)
	}
	if len(out) > maxOutputBytes(name) {
		t.Fatalf("output is %d bytes, over %d", len(out), maxOutputBytes(name))
	}
	if bytes.Contains(out, []byte(canary)) {
		t.Fatalf("the key is in the output %s", out)
	}
}

func FuzzDecodeSearch(f *testing.F) {
	for _, name := range []string{"search-go-release", "search-news-answer", "search-query-too-short", "raw-search-422-query-not-a-string", "docs-429"} {
		f.Add(200, fixture(f, name))
		f.Add(400, fixture(f, name))
	}
	f.Add(200, hostileSearch(30, 200, "é"))
	f.Add(422, []byte(`{"detail": [{"loc": [1, null, {"a": 1}], "msg": "`+canary+`"}]}`))
	f.Add(400, []byte(`{"detail": "`+canary+`"}`))
	f.Add(200, []byte(`{"results": null, "answer": null}`))
	f.Add(200, []byte(`{"results": [{"title": "\ud800", "url": 1}]}`))
	f.Add(200, []byte(`{"results": [{"published_date": "Tue, 11 Mar 2025 17:00:00 GMT"}]}`))
	f.Fuzz(func(t *testing.T, status int, body []byte) {
		fuzzDecode(t, "tavily_search", `{"query": "go release", "max_results": 20}`, status, body)
	})
}

func FuzzDecodeExtract(f *testing.F) {
	for _, name := range []string{"extract-partial-failure", "extract-query-text", "raw-extract-400-all-urls-invalid"} {
		f.Add(200, fixture(f, name))
		f.Add(400, fixture(f, name))
	}
	f.Add(200, hostileExtract(12, 300, "\x00"))
	f.Add(200, []byte(`{"results": [], "failed_results": null}`))
	f.Add(200, []byte(`{"results": [{"raw_content": null}], "failed_results": [{}]}`))
	f.Fuzz(func(t *testing.T, status int, body []byte) {
		urls := `{"urls": ["https://a.test", "https://b.test", "https://c.test", "https://d.test", "https://e.test",
			"https://f.test", "https://g.test", "https://h.test", "https://i.test", "https://j.test"]}`
		fuzzDecode(t, "tavily_extract", urls, status, body)
	})
}
