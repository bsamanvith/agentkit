package toolsettest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest/internal/toy"
)

// oneCassette writes a cassette directory holding search_go: POST /v1/search
// {"q": "go", "limit": 2}.
func oneCassette(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"search_go.req.json": `{"method": "POST", "path": "/v1/search", "query": {"v": ["1"]}, "body": {"q": "go", "limit": 2}}`,
		"search_go.res.http": "HTTP/1.1 200 OK\nContent-Type: application/json\n\n{\"ok\": true}",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func post(t *testing.T, c *http.Client, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test", "yes")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestUpstreamMatchesJSONBodies: a body matches its cassette whatever the key
// order and spacing, and every request is recorded, headers included.
func TestUpstreamMatchesJSONBodies(t *testing.T) {
	t.Parallel()
	u := toolsettest.NewUpstream(t, oneCassette(t))
	status, body := post(t, u.Client(), u.URL()+"/v1/search?v=1", `{ "limit":2,"q" : "go" }`)
	if status != http.StatusOK || body != `{"ok": true}` {
		t.Fatalf("got %d %s, want the cassette's 200", status, body)
	}
	reqs := u.Requests()
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Cassette != "search_go" || r.Method != http.MethodPost || r.Path != "/v1/search" || r.Query.Get("v") != "1" ||
		r.Header.Get("X-Test") != "yes" || string(r.Body) != `{ "limit":2,"q" : "go" }` {
		t.Errorf("recorded %+v", r)
	}
	if r.Header.Get("X-Toolsettest-Client") != "" {
		t.Errorf("the recorded headers keep the kit's own marker: %v", r.Header)
	}
}

// TestUpstreamMismatchDiff: a request no cassette matches fails the test with
// a diff against the closest cassette.
func TestUpstreamMismatchDiff(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	var status int
	spy.Run(func(tb testing.TB) {
		u := toolsettest.NewUpstream(tb, oneCassette(t))
		status, _ = post(t, u.Client(), u.URL()+"/v1/search?v=1", `{"q": "rust", "limit": 2}`)
	})
	if status != http.StatusTeapot {
		t.Errorf("status %d, want 418", status)
	}
	if !spy.HasErr("[upstream] no cassette", "closest cassette search_go (-cassette +request)", `-`, `"go"`, `+`, `"rust"`) {
		t.Errorf("errors %q, want a diff of the body against search_go", spy.Errs())
	}
}

// TestUpstreamRequiresTheHermeticClient: a request that did not come
// through Upstream.Client fails the test.
func TestUpstreamRequiresTheHermeticClient(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) {
		u := toolsettest.NewUpstream(tb, oneCassette(t))
		post(t, &http.Client{}, u.URL()+"/v1/search?v=1", `{"q": "go", "limit": 2}`)
	})
	if !spy.HasErr("[hermetic]", "did not come through Env.HTTPClient") {
		t.Errorf("errors %q, want a [hermetic] failure", spy.Errs())
	}
}

// TestHermeticClientRefusesOtherHosts: the client reaches the upstream only.
func TestHermeticClientRefusesOtherHosts(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	var got string
	spy.Run(func(tb testing.TB) {
		u := toolsettest.NewUpstream(tb, "")
		_, got = post(t, u.Client(), "https://api.tavily.com/search", `{}`)
	})
	if !strings.Contains(got, "hermetic client refused") {
		t.Errorf("request to another host = %q, want it refused", got)
	}
	if !spy.HasErr("[hermetic] refused POST https://api.tavily.com/search") {
		t.Errorf("errors %q, want the refusal to fail the test", spy.Errs())
	}
}

func toyDefs(t *testing.T) []tool.Definition {
	t.Helper()
	return toolsettest.Definitions(t, newToy(toy.BugNone), json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`))
}

var hit = map[string]any{"results": []any{map[string]any{"title": "Go", "url": "https://go.dev", "content": "Go."}}}

// TestFakeChecksInput: input the real schema rejects gives ErrInvalidInput,
// and the script never sees it.
func TestFakeChecksInput(t *testing.T) {
	t.Parallel()
	f := toolsettest.NewFake(t, toyDefs(t), toolsettest.Output("toy_search", hit))
	for _, in := range []string{`{}`, `{"query": 7}`, `{"query": "go", "max_results": "x"}`, `{"query": "go", "extra": 1}`, `not json`} {
		if _, err := f.Invoke(t.Context(), "toy_search", json.RawMessage(in)); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("toy_search(%s) = %v, want tool.ErrInvalidInput", in, err)
		}
	}
	if out, err := f.Invoke(t.Context(), "toy_search", json.RawMessage(`{"query": "go"}`)); err != nil || !strings.Contains(string(out), "go.dev") {
		t.Errorf("toy_search(valid) = %s, %v", out, err)
	}
}

// TestFakeChecksOutput: a script returning output the real output schema
// rejects fails the test.
func TestFakeChecksOutput(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	var err error
	spy.Run(func(tb testing.TB) {
		f := toolsettest.NewFake(tb, toyDefs(t), toolsettest.Output("toy_search", map[string]any{"results": "none"}))
		_, err = f.Invoke(t.Context(), "toy_search", json.RawMessage(`{"query": "go"}`))
	})
	if !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("err = %v, want tool.ErrToolFailed", err)
	}
	if !spy.HasErr("the script for toy_search returned output the real output schema rejects") {
		t.Errorf("errors %q, want the bad output to fail the test", spy.Errs())
	}
}

// TestFakeUnscriptedCall: a call with no script fails the test.
func TestFakeUnscriptedCall(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	var err error
	spy.Run(func(tb testing.TB) {
		f := toolsettest.NewFake(tb, toyDefs(t), toolsettest.Output("toy_search", hit))
		_, err = f.Invoke(t.Context(), "toy_get", json.RawMessage(`{"id": "go1"}`))
	})
	if err == nil || !spy.HasErr("unscripted call of toy_get") {
		t.Errorf("err = %v, errors %q: want an unscripted call to fail the test", err, spy.Errs())
	}
}

// TestFakeSequenceAndCalls: a Sequence answers calls in turn, a call past
// its end fails the test, and Calls records every call.
func TestFakeSequenceAndCalls(t *testing.T) {
	t.Parallel()
	spy := toolsettest.NewSpy(t)
	var f *toolsettest.Fake
	var errs [3]error
	spy.Run(func(tb testing.TB) {
		f = toolsettest.NewFake(tb, toyDefs(t), toolsettest.Sequence(
			toolsettest.Output("toy_get", map[string]any{"id": "go1", "title": "Go", "content": "Go."}),
			toolsettest.Error("toy_get", tool.ErrToolFailed),
		))
		for i := range errs {
			_, errs[i] = f.Invoke(t.Context(), "toy_get", json.RawMessage(fmt.Sprintf(`{"id": "go%d"}`, i)))
		}
	})
	if errs[0] != nil || !errors.Is(errs[1], tool.ErrToolFailed) || !errors.Is(errs[1], toolsettest.ErrScripted) || errs[2] == nil {
		t.Errorf("errors %v, want nil, a scripted ErrToolFailed, then a failure", errs)
	}
	if !spy.HasErr("call 3 of toy_get, but its Sequence has 2 step(s)") {
		t.Errorf("errors %q, want the third call to fail the test", spy.Errs())
	}
	calls := f.Calls()
	if len(calls) != 3 || calls[0].Tool != "toy_get" || string(calls[2].Input) != `{"id": "go2"}` || calls[0].Err != nil || calls[1].Err == nil {
		t.Errorf("Calls() = %+v", calls)
	}
}

// fakeToy is the toy as a FakeFactory, scripted from its golden files: the
// search answers by query, the get replays its golden.
func fakeToy(t *testing.T) *toolsettest.FakeToolset {
	t.Helper()
	golden := func(name string) json.RawMessage {
		b, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return toolsettest.FakeFactory(t, toy.Type, toyDefs(t),
		toolsettest.OutputFunc("toy_search", func(_ context.Context, input json.RawMessage) (any, error) {
			var in toy.SearchInput
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.MaxResults > 20 {
				return nil, &tool.Error{Tool: "toy_search", Kind: tool.ErrInvalidInput, Err: errors.New("max_results over 20")}
			}
			if in.Query == "go" {
				return golden("search_go"), nil
			}
			return golden("search_nothing"), nil
		}),
		toolsettest.Golden("toy_get", filepath.Join("testdata", "golden", "get_go1.json")),
	)
}

// TestFakeRunsTheSuite: a FakeFactory standing for the toy passes the parts
// of the suite that apply to a fake.
func TestFakeRunsTheSuite(t *testing.T) {
	t.Parallel()
	ff := fakeToy(t)
	c := toyConnector(toy.BugNone)
	c.New = func(toolsettest.Env) toolset.Factory { return ff }
	c.Invalid = c.Invalid[:1] // max_results_too_big; bad_id needs the real connector's check
	c.Invalid = append(c.Invalid, toolsettest.Call{Name: "no_query", Tool: "toy_search", Input: json.RawMessage(`{}`)})
	toolsettest.RunFake(t, c)
}
