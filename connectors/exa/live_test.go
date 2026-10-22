//go:build live

package exa_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// recordEnv, set to 1 with -tags live, rewrites testdata/cassettes from the
// real API: each mapping case's request and response, without "synthetic"
// (T049's variable). Review the diff, then run `make golden`.
const recordEnv = "AGENTKIT_RECORD_CASSETTES"

// Criterion: live. Against the real Exa API with EXA_API_KEY, the mapping
// cases' calls (searchCases, extractCases) succeed with the shared shape; a
// search returns results with URLs and content, and extracting a known page
// returns its text while an unreachable URL is reported in failed.
func TestLive(t *testing.T) {
	key := os.Getenv("EXA_API_KEY")
	if key == "" {
		t.Skip("EXA_API_KEY not set")
	}
	record := os.Getenv(recordEnv) == "1"
	call := func(t *testing.T, name, toolName, settings, input string) json.RawMessage {
		rec := &recorder{base: http.DefaultTransport}
		f := exa.Factory(exa.WithLookupEnv(os.LookupEnv), exa.WithHTTPClient(&http.Client{Transport: rec}))
		tools, err := f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings)})
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools {
			if tl.Definition().Name != toolName {
				continue
			}
			out, err := tl.Invoke(t.Context(), json.RawMessage(input))
			if err != nil {
				t.Fatalf("%s: %v", toolName, err)
			}
			if strings.Contains(string(out), key) {
				t.Fatalf("%s output contains the key", toolName)
			}
			checkShape(t, toolName, out, 0)
			if record && !t.Failed() {
				rec.save(t, name, key)
			}
			return out
		}
		t.Fatalf("no tool %s", toolName)
		return nil
	}

	for name, tc := range searchCases {
		t.Run(name, func(t *testing.T) {
			var got searchShape
			if err := json.Unmarshal(call(t, name, "exa_search", tc.settings, tc.input), &got); err != nil {
				t.Fatal(err)
			}
			// The narrowly filtered cases may legitimately find nothing.
			if (name == "search-defaults" || name == "search-text") && len(got.Results) == 0 {
				t.Fatal("no results")
			}
			for _, r := range got.Results {
				if !strings.HasPrefix(r.URL, "http") || r.Content == "" {
					t.Errorf("result %+v without a URL or content", r)
				}
			}
		})
	}
	for name, tc := range extractCases {
		t.Run(name, func(t *testing.T) {
			var got extractShape
			if err := json.Unmarshal(call(t, name, "exa_extract", tc.settings, tc.input), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Results) == 0 || got.Results[0].Content == "" {
				t.Fatalf("results = %+v, want the page's content", got.Results)
			}
			if name == "contents-partial" {
				if !strings.Contains(got.Results[0].Content, "Go") {
					t.Errorf("results = %+v, want the release history page's text", got.Results)
				}
				if len(got.Failed) == 0 {
					t.Errorf("failed is empty, want the unreachable URLs")
				}
			}
		})
	}
}

// recorder keeps the one 200 exchange of a call, to save as a cassette.
type recorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	req  cassetteReq
	resp []byte
	n    int
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	resp, err := r.base.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err // rare failures stay synthetic (T049)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.req = cassetteReq{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery, Body: reqBody}
	r.resp = body
	return resp, nil
}

// save writes the exchange as testdata/cassettes/<name>.*, refusing one
// that carries the key.
func (r *recorder) save(t *testing.T, name, key string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n != 1 {
		t.Fatalf("recorded %d exchanges, want 1", r.n)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, r.resp, "", "  "); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	res := fmt.Sprintf("HTTP/1.1 200 OK\nContent-Type: application/json\n\n%s\n", pretty.Bytes())
	if strings.Contains(res, key) || strings.Contains(string(r.req.Body), key) || strings.Contains(r.req.Query, key) {
		t.Fatal("the exchange contains the key; not recording it")
	}
	writeCassetteReq(t, name, r.req)
	if err := os.WriteFile(cassettePath(name, ".res.http"), []byte(res), 0o644); err != nil {
		t.Fatal(err)
	}
}
