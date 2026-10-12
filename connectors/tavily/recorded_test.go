package tavily_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
)

// TestRecordedCases replays cassettes recorded from the real API. The
// request each tool call sends (settings defaults applied, model input
// validated) must equal the cassette's, JSON-equal: the cassette is the
// golden request, and changes only by re-recording. What the model gets back
// is pinned by a golden file: the E10 output shape, or the error text for a
// rejected request.
func TestRecordedCases(t *testing.T) {
	t.Parallel()
	for _, c := range recordedCases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{} // keyless, as recorded
			if c.keyed {
				env["TAVILY_API_KEY"] = canary
			}
			r := newRig(t, rigOpts{settings: c.settings, env: env})
			want, res := cassette(t, c.name)
			if res.status != c.status {
				t.Fatalf("cassette status %d, case says %d", res.status, c.status)
			}
			r.up.queue(res)

			out, err := r.call(c.tool, c.input)

			reqs := r.up.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d upstream requests, want 1", len(reqs))
			}
			got := reqs[0]
			if got.method != want.Method || got.path != want.Path || got.query != want.Query || !jsonEqual(got.body, want.Body) {
				t.Errorf("request %s %s?%s %s\ndoes not match the cassette %s %s?%s %s",
					got.method, got.path, got.query, got.body, want.Method, want.Path, want.Query, want.Body)
			}
			switch {
			case c.status == 200 && err != nil:
				t.Fatalf("error %v", err)
			case c.status == 200:
				golden(t, c.name+".output.json", out)
			case err == nil:
				t.Fatalf("status %d gave no error", c.status)
			default:
				// The error text is what the model is shown.
				testkit.Golden(t, filepath.Join("testdata", "golden", c.name+".error.txt"), []byte(err.Error()+"\n"))
			}
		})
	}
}

// jsonEqual reports whether a and b are the same JSON value, ignoring key
// order and spacing.
func jsonEqual(a, b []byte) bool {
	var va, vb any
	return json.Unmarshal(a, &va) == nil && json.Unmarshal(b, &vb) == nil && reflect.DeepEqual(va, vb)
}

// golden compares JSON, indented, with a golden file.
func golden(t *testing.T, name string, raw []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		t.Fatalf("%s: %v: %q", name, err, raw)
	}
	buf.WriteByte('\n')
	testkit.Golden(t, filepath.Join("testdata", "golden", name), buf.Bytes())
}

// TestAnswerOnlyWhenAsked: an answer Tavily sends is passed on only when the
// settings turn include_answer on; otherwise the output has no answer field
// and the request does not ask for one.
func TestAnswerOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		settings string
		want     bool
	}{{`{}`, false}, {`{"search_defaults": {"include_answer": false}}`, false}, {`{"search_defaults": {"include_answer": true}}`, true}} {
		t.Run(c.settings, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: c.settings})
			r.up.queue(reply{status: 200, body: []byte(`{"answer": "Go 1.27", "results": []}`)})
			out, err := r.call("tavily_search", `{"query": "go release"}`)
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Answer *string }
			_ = json.Unmarshal(out, &got)
			if (got.Answer != nil) != c.want {
				t.Errorf("output %s: answer present %v, want %v", out, got.Answer != nil, c.want)
			}
			var req struct {
				IncludeAnswer *bool `json:"include_answer"`
			}
			_ = json.Unmarshal(r.up.requests()[0].body, &req)
			if (req.IncludeAnswer != nil && *req.IncludeAnswer) != c.want {
				t.Errorf("request %s: include_answer, want %v", r.up.requests()[0].body, c.want)
			}
		})
	}
}
