//go:build live

package tavily_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// liveTools builds the toolset against the real API.
func liveTools(t *testing.T, settings string, env map[string]string, client *http.Client) tool.Registry {
	t.Helper()
	opts := []tavily.Option{tavily.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok })}
	if client != nil {
		opts = append(opts, tavily.WithHTTPClient(client))
	}
	tools, err := tavily.Factory(opts...).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings)})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestLiveKeyless runs both tools keyless against the real API.
func TestLiveKeyless(t *testing.T) {
	t.Parallel()
	liveSearchAndExtract(t, nil)
}

// TestLiveKeyed does the same with TAVILY_API_KEY.
func TestLiveKeyed(t *testing.T) {
	t.Parallel()
	key := os.Getenv("TAVILY_API_KEY")
	if key == "" {
		t.Skip("TAVILY_API_KEY is not set")
	}
	liveSearchAndExtract(t, map[string]string{"TAVILY_API_KEY": key})
}

func liveSearchAndExtract(t *testing.T, env map[string]string) {
	settings := `{"auth": "keyless"}`
	if env != nil {
		settings = `{"auth": "key"}`
	}
	reg := liveTools(t, settings, env, nil)

	out, err := reg.Invoke(t.Context(), "tavily_search", json.RawMessage(`{"query": "latest Go programming language release", "time_range": "month"}`))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var s struct {
		Results []struct{ Title, URL, PublishedDate, Content string }
	}
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Results {
		t.Logf("search result: %s (%s) %q", r.URL, r.Title, truncateForLog(r.Content))
	}
	if len(s.Results) == 0 || !strings.HasPrefix(s.Results[0].URL, "http") {
		t.Fatalf("search returned %s, want at least one result with a URL", out)
	}

	out, err = reg.Invoke(t.Context(), "tavily_extract", json.RawMessage(`{"urls": ["https://www.tavily.com"]}`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var e struct {
		Results []struct{ URL, Title, Content string }
		Failed  []struct{ URL, Error string }
	}
	if err := json.Unmarshal(out, &e); err != nil {
		t.Fatal(err)
	}
	if len(e.Results) != 1 || len(e.Results[0].Content) < 100 {
		t.Fatalf("extract returned %s, want the page's content", truncateForLog(string(out)))
	}
	t.Logf("extract: %s (%s), %d bytes: %q", e.Results[0].URL, e.Results[0].Title, len(e.Results[0].Content), truncateForLog(e.Results[0].Content))
}

func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// TestLiveRecord sends recordedCases to the real API and checks each gets
// the status it was recorded with. With AGENTKIT_RECORD_CASSETTES=1 it
// rewrites their cassettes (T049's format), scrubbed: no request headers,
// only Content-Type and Retry-After response headers, request IDs zeroed. A
// key in either body, or an output that fails the output schema, fails the
// recording. Review the diff, then run make
// golden.
func TestLiveRecord(t *testing.T) {
	t.Parallel()
	record := os.Getenv("AGENTKIT_RECORD_CASSETTES") == "1"
	const madeUpKey = "tvly-dev-0000agentkitfixture0000"
	for _, c := range recordedCases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{next: http.DefaultTransport}
			env := map[string]string{}
			if c.keyed {
				env["TAVILY_API_KEY"] = madeUpKey
			}
			reg := liveTools(t, settingsOr(c.settings), env, &http.Client{Transport: rec})
			out, err := reg.Invoke(t.Context(), c.tool, json.RawMessage(c.input))
			t.Logf("%s: status %d, err %v", c.name, rec.status, err)
			if rec.status != c.status {
				t.Fatalf("status %d, want %d: %s", rec.status, c.status, rec.resBody)
			}
			if c.status == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if c.status != http.StatusOK && err == nil {
				t.Fatal("no error")
			}
			if err == nil {
				// Keep only exchanges whose mapped output is what the
				// output schema promises (T049 §6).
				tools, berr := tavily.Factory().Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settingsOr(c.settings))})
				if berr != nil {
					t.Fatal(berr)
				}
				if verr := validOutput(outputSchemas(t, tools), c.tool, out); verr != nil {
					t.Fatalf("output does not validate against its output schema, not recording: %v", verr)
				}
			}
			if !record {
				return
			}
			req, res := rec.cassette()
			if bytes.Contains(req, []byte(madeUpKey)) || bytes.Contains(res, []byte(madeUpKey)) {
				t.Fatal("the key is in the exchange; not writing the cassette")
			}
			base := filepath.Join("testdata", "cassettes", c.name)
			if err := os.WriteFile(base+".req.json", req, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(base+".res.http", res, 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var requestID = regexp.MustCompile(`"request_id"\s*:\s*"[^"]*"`)

// indent re-indents a JSON body, keeping it as is if it is not JSON.
func indent(body []byte) []byte {
	var buf bytes.Buffer
	if json.Indent(&buf, body, "", "  ") != nil {
		return body
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// recorder keeps the last exchange.
type recorder struct {
	next              http.RoundTripper
	method, path, raw string
	reqBody           []byte
	status            int
	header            http.Header
	resBody           []byte
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		r.reqBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	r.method, r.path, r.raw = req.Method, req.URL.Path, req.URL.RawQuery
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, errors.Join(errors.New("read"), err)
	}
	r.status, r.header, r.resBody = resp.StatusCode, resp.Header, body
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// cassette renders the exchange as <name>.req.json and <name>.res.http.
func (r *recorder) cassette() (req, res []byte) {
	req, _ = json.MarshalIndent(struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Query  string          `json:"query,omitempty"`
		Body   json.RawMessage `json:"body"`
	}{r.method, r.path, r.raw, r.reqBody}, "", "  ")
	req = append(req, '\n')

	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\n", r.status, http.StatusText(r.status))
	for _, k := range []string{"Content-Type", "Retry-After"} {
		if v := r.header.Get(k); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	b.WriteString("\n")
	b.Write(indent(requestID.ReplaceAll(r.resBody, []byte(`"request_id":"00000000-0000-0000-0000-000000000000"`))))
	return req, b.Bytes()
}
