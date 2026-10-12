package toolsettest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// standIn serves the toy API as the "real" one, with edit applied to every
// JSON response of path (nil: unchanged) and extra headers a cassette must
// not keep.
func standIn(t *testing.T, key, path string, edit func(map[string]any)) string {
	t.Helper()
	api := toy.NewServer(key)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=toolsettest")
		w.Header().Set("X-Account-Id", "acct-123")
		if edit == nil || r.URL.Path != path {
			api.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, r)
		var v map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Errorf("stand-in: %v", err)
		}
		edit(v)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.Code)
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// editHits applies fn to every hit of a toy search response.
func editHits(fn func(hit map[string]any)) func(map[string]any) {
	return func(v map[string]any) {
		hits, _ := v["hits"].([]any)
		for _, h := range hits {
			fn(h.(map[string]any))
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cassetteFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestRecordWritesReplayableCassettes: recording the toy from its reference
// server writes the committed cassettes (bar the date), keeps no request
// header and only the allowed response headers, and replays to the live
// output (Record checks that itself).
func TestRecordWritesReplayableCassettes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	toolsettest.Record(t, toyConnector(toy.BugNone), toolsettest.Recording{
		BaseURL: standIn(t, toyKey, "", nil), LookupEnv: toyEnv, Dir: dir,
	})
	today := time.Now().UTC().Format(time.DateOnly)
	for _, name := range []string{"search_go", "search_nothing", "get_go1"} {
		req := readFile(t, filepath.Join(dir, name+".req.json"))
		var got, want toolsettest.CassetteRequest
		if err := json.Unmarshal([]byte(req), &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join("testdata", "cassettes", name+".req.json"))), &want); err != nil {
			t.Fatal(err)
		}
		if got.RecordedAt != today {
			t.Errorf("%s: recorded_at %q, want %q", name, got.RecordedAt, today)
		}
		got.RecordedAt, want.RecordedAt = "", ""
		if g, w := mustJSON(t, got), mustJSON(t, want); g != w {
			t.Errorf("%s.req.json = %s, want %s", name, g, w)
		}
		for _, leak := range []string{toyKey, "Authorization", "Bearer", "header"} {
			if strings.Contains(req, leak) {
				t.Errorf("%s.req.json holds %q:\n%s", name, leak, req)
			}
		}
		res := readFile(t, filepath.Join(dir, name+".res.http"))
		if want := readFile(t, filepath.Join("testdata", "cassettes", name+".res.http")); res != want {
			t.Errorf("%s.res.http =\n%s\nwant\n%s", name, res, want)
		}
		for _, h := range []string{"Set-Cookie", "X-Account-Id", "X-Request-Id", "Date"} {
			if strings.Contains(res, h+":") {
				t.Errorf("%s.res.http keeps the %s header:\n%s", name, h, res)
			}
		}
		if !strings.Contains(res, "Content-Type: application/json") {
			t.Errorf("%s.res.http lost Content-Type:\n%s", name, res)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRecordRefusesTheKey: a key in the request path, query or a response
// body fails the recording and nothing is written.
func TestRecordRefusesTheKey(t *testing.T) {
	t.Parallel()
	const pathKey = "abc123key456" // a valid toy id, so it can be a path segment
	for _, tc := range []struct {
		name string
		c    toolsettest.Connector
		key  string
		url  func(t *testing.T) string
		want string
	}{
		{
			name: "query", c: toyConnector(toy.BugKeyInQuery), key: toyKey,
			url:  func(t *testing.T) string { return standIn(t, toyKey, "", nil) },
			want: "refusing to write cassette get_go1: the key is in the request query",
		},
		{
			name: "response_body", c: toyConnector(toy.BugNone), key: toyKey,
			url: func(t *testing.T) string {
				return standIn(t, toyKey, "/v1/search", editHits(func(h map[string]any) { h["snippet"] = "your key is " + toyKey }))
			},
			want: "refusing to write cassette search_go: the key is in the response body",
		},
		{
			name: "path", key: pathKey,
			c: func() toolsettest.Connector {
				c := toyConnector(toy.BugNone)
				c.Calls = []toolsettest.Call{{Name: "get_key", Tool: "toy_get", Input: json.RawMessage(`{"id": "` + pathKey + `"}`), Cassettes: []string{"get_key"}}}
				return c
			}(),
			url: func(t *testing.T) string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id": "x", "name": "x", "text": "x"}`))
				}))
				t.Cleanup(srv.Close)
				return srv.URL
			},
			want: "refusing to write cassette get_key: the key is in the request path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			lookup := func(name string) (string, bool) { return tc.key, name == "TOY_API_KEY" }
			spy := toolsettest.NewSpy(t)
			spy.Run(func(tb testing.TB) {
				toolsettest.Record(tb, tc.c, toolsettest.Recording{BaseURL: tc.url(t), LookupEnv: lookup, Dir: dir})
			})
			if !spy.HasErr(tc.want) {
				t.Errorf("errors %q, want one containing %q", spy.Errs(), tc.want)
			}
			if files := cassetteFiles(t, dir); len(files) != 0 {
				t.Errorf("wrote %v, want nothing", files)
			}
		})
	}
}

// TestRecordRefusesSecretShapes: a response holding a key-shaped string (not
// this connector's key) is refused too.
func TestRecordRefusesSecretShapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	url := standIn(t, toyKey, "/v1/search", editHits(func(h map[string]any) { h["debug"] = "tvly-prod-0123456789abcdef" }))
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		toolsettest.Record(tb, toyConnector(toy.BugNone), toolsettest.Recording{BaseURL: url, LookupEnv: toyEnv, Dir: dir})
	})
	if !spy.HasErr("refusing to write cassette search_go: it holds a Tavily API key") {
		t.Errorf("errors %q, want a refusal naming the Tavily key shape", spy.Errs())
	}
	if files := cassetteFiles(t, dir); len(files) != 0 {
		t.Errorf("wrote %v, want nothing", files)
	}
}

// TestRecordScrub: Recording.Scrub rewrites response bodies before the
// connector and the cassette see them, so the cassette replays to the same
// output and holds no scrubbed data.
func TestRecordScrub(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	email := regexp.MustCompile(`[a-z]+@example\.com`)
	url := standIn(t, toyKey, "/v1/search", editHits(func(h map[string]any) { h["snippet"] = "Ask alice@example.com." }))
	toolsettest.Record(t, toyConnector(toy.BugNone), toolsettest.Recording{
		BaseURL: url, LookupEnv: toyEnv, Dir: dir,
		Scrub: func(b []byte) []byte { return email.ReplaceAll(b, []byte("someone@example.invalid")) },
	})
	res := readFile(t, filepath.Join(dir, "search_go.res.http"))
	if strings.Contains(res, "alice") || !strings.Contains(res, "someone@example.invalid") {
		t.Errorf("search_go.res.http is not scrubbed:\n%s", res)
	}
}

// strictGet wraps the toy so toy_get declares a required output field the
// connector never fills: every toy_get output fails the output schema.
type strictGet struct{ toolset.Factory }

func (f strictGet) Build(ctx context.Context, settings json.RawMessage) ([]tool.Tool, error) {
	tools, err := f.Factory.Build(ctx, settings)
	for i, tl := range tools {
		if tl.Definition().Name == "toy_get" {
			tools[i] = requiresScore{tl}
		}
	}
	return tools, err
}

type requiresScore struct{ tool.Tool }

func (r requiresScore) Definition() tool.Definition {
	d := r.Tool.Definition()
	var s map[string]any
	_ = json.Unmarshal(d.OutputSchema, &s)
	s["properties"].(map[string]any)["score"] = map[string]any{"type": "number"}
	s["required"] = append(s["required"].([]any), "score")
	d.OutputSchema, _ = json.Marshal(s)
	return d
}

// TestRecordDropsOutputFailingSchema: an exchange whose mapped output fails
// the output schema is not written, and the recording fails.
func TestRecordDropsOutputFailingSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := toyConnector(toy.BugNone)
	c.New = func(e toolsettest.Env) toolset.Factory { return strictGet{newToy(toy.BugNone)(e)} }
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		toolsettest.Record(tb, c, toolsettest.Recording{BaseURL: standIn(t, toyKey, "", nil), LookupEnv: toyEnv, Dir: dir})
	})
	if errs := spy.Errs(); len(errs) != 1 || !strings.Contains(errs[0], "get_go1: the output from the real API fails the output schema") {
		t.Errorf("errors %q, want exactly one, about get_go1's output", errs)
	}
	files := strings.Join(cassetteFiles(t, dir), " ")
	if strings.Contains(files, "get_go1") || !strings.Contains(files, "search_go.req.json") {
		t.Errorf("wrote %s, want the search cassettes and not get_go1", files)
	}
}

// TestDriftUnchanged: an API that still answers as recorded shows no drift.
func TestDriftUnchanged(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		toolsettest.Drift(tb, toyConnector(toy.BugNone), toolsettest.Recording{BaseURL: standIn(t, toyKey, "", nil), LookupEnv: toyEnv})
	})
	if errs := spy.Errs(); len(errs) > 0 {
		t.Errorf("drift reported against an unchanged API: %q", errs)
	}
}

// TestDriftReportsChangedFields: a field that changed type and one that was
// removed are both reported.
func TestDriftReportsChangedFields(t *testing.T) {
	t.Parallel()
	url := standIn(t, toyKey, "/v1/search", func(v map[string]any) {
		v["took_ms"] = "3ms"
		editHits(func(h map[string]any) { delete(h, "snippet") })(v)
	})
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		toolsettest.Drift(tb, toyConnector(toy.BugNone), toolsettest.Recording{BaseURL: url, LookupEnv: toyEnv})
	})
	if !spy.HasErr("[drift] cassette search_go", "retyped $.took_ms: number → string", "removed $.hits[].snippet") {
		t.Errorf("errors %q, want search_go's retyped took_ms and removed snippet", spy.Errs())
	}
	if spy.HasErr("cassette get_go1") {
		t.Errorf("errors %q: get_go1 did not change", spy.Errs())
	}
}

// TestDriftWarnsOldCassettes: a cassette recorded over 90 days ago is
// flagged in the log, without failing.
func TestDriftWarnsOldCassettes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range cassetteFiles(t, filepath.Join("testdata", "cassettes")) {
		b := []byte(readFile(t, filepath.Join("testdata", "cassettes", name)))
		if name == "get_go1.req.json" {
			b = regexp.MustCompile(`"recorded_at": "[0-9-]+"`).ReplaceAll(b, []byte(`"recorded_at": "2020-01-01"`))
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := toyConnector(toy.BugNone)
	c.Cassettes = dir
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		toolsettest.Drift(tb, c, toolsettest.Recording{BaseURL: standIn(t, toyKey, "", nil), LookupEnv: toyEnv})
	})
	if log := spy.LogText(); !strings.Contains(log, "cassette get_go1 was recorded 2020-01-01, over 90 days ago") || strings.Contains(log, "search_go") {
		t.Errorf("log %q, want a warning about get_go1 only", log)
	}
	if errs := spy.Errs(); len(errs) > 0 {
		t.Errorf("errors %q, want none", errs)
	}
}
