package evals

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

func TestShippedScenarios(t *testing.T) {
	t.Parallel()
	scenarios, err := LoadScenarios("connectors", "..")
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) < 15 {
		t.Errorf("%d scenarios, want at least 15", len(scenarios))
	}
	providers := map[string]int{}
	// Each check kind has a scenario with misleading descriptions, which
	// make eval-misleading runs to show the check can fail.
	proved := map[string]bool{}
	for _, s := range scenarios {
		for _, ts := range s.Toolsets {
			providers[ts]++
		}
		if len(s.Misleading) == 0 {
			continue
		}
		c := s.Checks
		if len(c.Called) > 0 || c.First != "" {
			proved["tool chosen"] = true
		}
		if len(c.Args) > 0 {
			proved["arguments"] = true
		}
		if c.MaxCalls != nil {
			proved["call count"] = true
		}
		if c.CitesResultURL {
			proved["citation"] = true
		}
		if c.After != nil {
			proved["after "+c.After.On] = true
		}
		if len(c.AnswerContains) > 0 {
			proved["answer"] = true
		}
		if c.CitesSource {
			proved["source citation"] = true
		}
	}
	for _, p := range []string{"tavily", "exa", "parallel"} {
		if providers[p] < 3 {
			t.Errorf("%s is offered in %d scenarios, want at least 3", p, providers[p])
		}
	}
	if providers[Knowledge] < 3 {
		t.Errorf("knowledge is offered in %d scenarios, want at least 3", providers[Knowledge])
	}
	for _, kind := range []string{"tool chosen", "arguments", "call count", "citation", "after error", "after truncation", "answer", "source citation"} {
		if !proved[kind] {
			t.Errorf("no scenario with misleading descriptions checks %s", kind)
		}
	}
}

func TestLoadScenariosRejects(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"unknown field":       `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{"max_calls":0},"extra":1}`,
		"unknown toolset":     `{"behavior":"b","prompt":"p","toolsets":["bing"],"checks":{"max_calls":0}}`,
		"no checks":           `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{}}`,
		"tool not offered":    `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{"called":["tavily_search"]}}`,
		"unknown endpoint":    `{"behavior":"b","prompt":"p","toolsets":["exa"],"upstream":{"exa":{"/extract":[{"status":500}]}},"checks":{"max_calls":0}}`,
		"missing cassette":    `{"behavior":"b","prompt":"p","toolsets":["exa"],"upstream":{"exa":{"/search":[{"cassette":"nope.http"}]}},"checks":{"max_calls":0}}`,
		"bad after":           `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{"after":{"tool":"exa_search","on":"timeout","next":["exa_search"]}}}`,
		"cites_source, no kb": `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{"cites_source":true}}`,
		"unknown agent":       `{"behavior":"b","prompt":"p","agent":"sales","toolsets":["knowledge"],"checks":{"max_calls":1}}`,
		"empty answer text":   `{"behavior":"b","prompt":"p","toolsets":["knowledge"],"checks":{"answer_contains":[" "]}}`,
		"misleading unknown":  `{"behavior":"b","prompt":"p","toolsets":["exa"],"checks":{"max_calls":0},"misleading":{"exa":{"exa_find":"x"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadScenarios(dir, dir); err == nil {
				t.Error("loaded")
			}
		})
	}
}

func TestReadTranscript(t *testing.T) {
	t.Parallel()
	events := []wire.Event{
		{Type: wire.TypeRunStart},
		{Type: wire.TypeToolCall, ToolUseID: "a", Name: "tavily__tavily_search", Input: json.RawMessage(`{"query":"go"}`)},
		{Type: wire.TypeToolCall, ToolUseID: "b", Name: "exa_search", Input: json.RawMessage(`{"query":"go"}`)},
		{Type: wire.TypeToolResult, ToolUseID: "b", Content: "rate limited", IsError: true},
		{Type: wire.TypeToolResult, ToolUseID: "a", Content: `{"results":[]}`},
		{Type: wire.TypeRunDone, Answer: "Done.", Usage: &llm.Usage{InputTokens: 10, CacheReadTokens: 5, OutputTokens: 3}},
	}
	got := ReadTranscript(events)
	want := Transcript{
		Calls: []Call{
			{Tool: "tavily_search", Toolset: "tavily", Input: json.RawMessage(`{"query":"go"}`), Result: `{"results":[]}`},
			{Tool: "exa_search", Toolset: "exa", Input: json.RawMessage(`{"query":"go"}`), Result: "rate limited", IsError: true},
		},
		Answer: "Done.", InputTokens: 15, OutputTokens: 3,
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("transcript\n got %s\nwant %s", gotJSON, wantJSON)
	}
	failed := ReadTranscript([]wire.Event{{Type: wire.TypeRunError, Error: &wire.Error{Code: "model_error", Message: "overloaded"}}})
	if failed.Error != "model_error: overloaded" {
		t.Errorf("error = %q", failed.Error)
	}
}

func call(tool, input, result string) Call {
	ts, _, _ := strings.Cut(tool, "_")
	return Call{Tool: tool, Toolset: ts, Input: json.RawMessage(input), Result: result}
}

func failed(c Call) Call { c.IsError = true; return c }

func intp(n int) *int { return &n }

func TestCheck(t *testing.T) {
	t.Parallel()
	search := call("tavily_search", `{"query":"go","include_domains":["go.dev"],"time_range":"week"}`,
		`{"results":[{"url":"https://go.dev/blog/go1.27","content":"Go 1.27 is out…[truncated 900 characters; call tavily_extract on this URL for the full page]"}]}`)
	extract := call("tavily_extract", `{"urls":["https://go.dev/blog/go1.27"]}`, `{"results":[{"url":"https://go.dev/blog/go1.27"}]}`)
	exa := call("exa_search", `{"query":"go","start_published_date":"2026-10-05T00:00:00Z"}`, `{"results":[]}`)
	kbSearch := call("knowledge_search", `{"query":"restocking fee"}`,
		`{"results":[{"source":"returns.md","title":"Returns and refunds","text":"Opened e-bikes carry a 12% restocking fee."}]}`)
	kbRead := call("knowledge_read", `{"source":"warranty.md"}`, `{"source":"warranty.md","title":"Warranty","text":"..."}`)
	cases := []struct {
		name   string
		checks Checks
		tr     Transcript
		fails  int
	}{
		{"called", Checks{Called: []string{"tavily_search"}}, Transcript{Calls: []Call{search}}, 0},
		{"not called", Checks{Called: []string{"tavily_extract"}}, Transcript{Calls: []Call{search}}, 1},
		{"not_called violated", Checks{NotCalled: []string{"tavily_search"}}, Transcript{Calls: []Call{search}}, 1},
		{"first", Checks{First: "tavily_extract"}, Transcript{Calls: []Call{extract, search}}, 0},
		{"first wrong", Checks{First: "tavily_extract"}, Transcript{Calls: []Call{search, extract}}, 1},
		{"first with no calls", Checks{First: "tavily_extract"}, Transcript{}, 1},
		{"max calls", Checks{MaxCalls: intp(1)}, Transcript{Calls: []Call{search}}, 0},
		{"too many calls", Checks{MaxCalls: intp(1)}, Transcript{Calls: []Call{search, extract}}, 1},
		{"no calls allowed", Checks{MaxCalls: intp(0)}, Transcript{Calls: []Call{search}}, 1},
		{"args contains", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "include_domains", Contains: "GO.dev"}}}, Transcript{Calls: []Call{search}}, 0},
		{"args contains missing", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "include_domains", Contains: "python.org"}}}, Transcript{Calls: []Call{search}}, 1},
		{"args one of", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "time_range", OneOf: []string{"day", "week"}}}}, Transcript{Calls: []Call{search}}, 0},
		{"args one of wrong", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "time_range", OneOf: []string{"day"}}}}, Transcript{Calls: []Call{search}}, 1},
		{"args unset", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "topic"}}}, Transcript{Calls: []Call{search}}, 1},
		{"args absent", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "topic", Absent: true}}}, Transcript{Calls: []Call{search}}, 0},
		{"args tool not called", Checks{Args: []ArgCheck{{Tool: "exa_search", Field: "query"}}}, Transcript{Calls: []Call{search}}, 1},
		{"args at least", Checks{Args: []ArgCheck{{Tool: "exa_search", Field: "start_published_date", AtLeast: "2026-10-01"}}}, Transcript{Calls: []Call{exa}}, 0},
		{"args too early", Checks{Args: []ArgCheck{{Tool: "exa_search", Field: "start_published_date", AtLeast: "2026-10-06"}}}, Transcript{Calls: []Call{exa}}, 1},
		{"args every", Checks{Args: []ArgCheck{{Tool: "tavily_search", Field: "include_domains", Every: true}}}, Transcript{Calls: []Call{search, call("tavily_search", `{"query":"x"}`, "")}}, 1},
		{"cites", Checks{CitesResultURL: true}, Transcript{Calls: []Call{search}, Answer: "See https://go.dev/blog/go1.27."}, 0},
		{"cites markdown", Checks{CitesResultURL: true}, Transcript{Calls: []Call{search}, Answer: "[Go blog](https://go.dev/blog/go1.27/)"}, 0},
		{"cites nothing", Checks{CitesResultURL: true}, Transcript{Calls: []Call{search}, Answer: "Go 1.27 is out."}, 1},
		{"cites an invented URL", Checks{CitesResultURL: true}, Transcript{Calls: []Call{search}, Answer: "https://go.dev/doc/go1.27"}, 1},
		{"after error", Checks{After: &AfterCheck{Tool: "tavily_search", On: "error", Next: []string{"exa_search"}}}, Transcript{Calls: []Call{failed(search), exa}}, 0},
		{"after error retried", Checks{After: &AfterCheck{Tool: "tavily_search", On: "error", Next: []string{"exa_search"}}}, Transcript{Calls: []Call{failed(search), search}}, 1},
		{"after error, nothing", Checks{After: &AfterCheck{Tool: "tavily_search", On: "error", Next: []string{"exa_search"}}}, Transcript{Calls: []Call{failed(search)}}, 1},
		{"no error happened", Checks{After: &AfterCheck{Tool: "tavily_search", On: "error", Next: []string{"exa_search"}}}, Transcript{Calls: []Call{search, exa}}, 1},
		{"after truncation", Checks{After: &AfterCheck{Tool: "tavily_search", On: "truncation", Next: []string{"tavily_extract"}, URLFromResult: true}}, Transcript{Calls: []Call{search, extract}}, 0},
		{
			"truncation, other URL",
			Checks{After: &AfterCheck{Tool: "tavily_search", On: "truncation", Next: []string{"tavily_extract"}, URLFromResult: true}},
			Transcript{Calls: []Call{search, call("tavily_extract", `{"urls":["https://example.com"]}`, "")}},
			1,
		},
		{"one provider", Checks{OneProvider: true}, Transcript{Calls: []Call{search, extract}}, 0},
		{"two providers", Checks{OneProvider: true}, Transcript{Calls: []Call{search, exa}}, 1},
		{"run failed", Checks{MaxCalls: intp(5)}, Transcript{Error: "model_error: overloaded"}, 1},
		{"answer contains", Checks{AnswerContains: []string{"12%", "Fee"}}, Transcript{Answer: "A 12% restocking fee."}, 0},
		{"answer lacks one", Checks{AnswerContains: []string{"12%", "45 days"}}, Transcript{Answer: "A 12% restocking fee."}, 1},
		{"cites a source", Checks{CitesSource: true}, Transcript{Calls: []Call{kbSearch}, Answer: "12% (see Returns.md)."}, 0},
		{"a stem is not a source", Checks{CitesSource: true}, Transcript{Calls: []Call{kbSearch}, Answer: "12%, per the returns policy."}, 1},
		{"a title is not a source", Checks{CitesSource: true}, Transcript{Calls: []Call{kbSearch}, Answer: "From Returns and Refunds: 12%."}, 1},
		{"cites a read", Checks{CitesSource: true}, Transcript{Calls: []Call{kbRead}, Answer: "See warranty.md."}, 0},
		{"cites no source", Checks{CitesSource: true}, Transcript{Calls: []Call{kbSearch}, Answer: "It is 12%."}, 1},
		{"cites another source", Checks{CitesSource: true}, Transcript{Calls: []Call{kbSearch}, Answer: "See shipping.md."}, 1},
		{"cites a failed search", Checks{CitesSource: true}, Transcript{Calls: []Call{failed(kbSearch)}, Answer: "See returns.md."}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Check(c.checks, c.tr); len(got) != c.fails {
				t.Errorf("failures %q, want %d", got, c.fails)
			}
		})
	}
}

func get(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`)) //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestUpstream(t *testing.T) {
	t.Parallel()
	s := Scenario{Name: "s", Toolsets: []string{"tavily", "exa"}, Upstream: map[string]map[string][]Response{
		"tavily": {"/search": {
			{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "120"}, Body: json.RawMessage(`{"detail":"slow down"}`)},
			{Body: json.RawMessage(`{"results":[]}`)},
		}},
	}}
	up := NewUpstream("..", []Scenario{s})
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)

	status, header, body := get(t, srv.URL+"/s/tavily/search")
	if status != http.StatusTooManyRequests || header.Get("Retry-After") != "120" || body != `{"detail":"slow down"}` {
		t.Errorf("first: %d %v %s", status, header, body)
	}
	for range 2 { // the last response repeats
		if status, _, body := get(t, srv.URL+"/s/tavily/search"); status != http.StatusOK || body != `{"results":[]}` {
			t.Errorf("then: %d %s", status, body)
		}
	}
	// An endpoint left out answers with its default cassette.
	if status, _, body := get(t, srv.URL+"/s/exa/search"); status != http.StatusOK || !strings.Contains(body, "go.dev") {
		t.Errorf("default: %d %s", status, body)
	}
	if got := up.Requests("s"); !slices.Equal(got, []string{"tavily/search", "tavily/search", "tavily/search", "exa/search"}) {
		t.Errorf("requests = %q", got)
	}
	up.Reset("s")
	if status, _, _ := get(t, srv.URL+"/s/tavily/search"); status != http.StatusTooManyRequests {
		t.Errorf("after Reset: %d, want the script from the start", status)
	}
	for _, path := range []string{"/other/tavily/search", "/s/parallel/v1/search", "/s/tavily/nope", "/s"} {
		if status, _, _ := get(t, srv.URL+path); status != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, status)
		}
	}
}

func TestDenyProxy(t *testing.T) {
	t.Parallel()
	p := &DenyProxy{}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	proxyURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get("http://api.tavily.com/search") //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
	if got := p.Seen(); len(got) != 1 || !strings.Contains(got[0], "api.tavily.com") {
		t.Errorf("seen = %q", got)
	}
}

func TestCompareAndLatest(t *testing.T) {
	t.Parallel()
	pass, fail := Run{Passed: true}, Run{}
	prev := Report{Date: "2026-10-21", Results: []Result{
		{Scenario: "a", Runs: []Run{pass, pass}},
		{Scenario: "b", Runs: []Run{pass, fail}},
		{Scenario: "c", Runs: []Run{fail}},
	}}
	cur := Report{Date: "2026-10-22", Results: []Result{
		{Scenario: "a", Runs: []Run{pass, fail}},
		{Scenario: "b", Runs: []Run{pass, pass}},
		{Scenario: "new", Runs: []Run{fail}},
	}}
	regs := Compare(prev, cur)
	if len(regs) != 1 || regs[0].Scenario != "a" || regs[0].Before != 1 || regs[0].After != 0.5 {
		t.Errorf("regressions = %+v, want a from 100%% to 50%%", regs)
	}

	dir := t.TempDir()
	for _, r := range []Report{prev, cur, cur, {Date: "2026-10-23", Misleading: true}} {
		if _, err := WriteReport(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	for i := 3; i <= 10; i++ { // -10 must sort after -9, not after -1
		if _, err := WriteReport(dir, cur); err != nil {
			t.Fatal(err)
		}
	}
	got, path, ok, err := LatestReport(dir, false)
	if err != nil || !ok || got.Date != "2026-10-22" || filepath.Base(path) != "2026-10-22-10.json" {
		t.Errorf("latest = %s %v %v %v", path, got.Date, ok, err)
	}
	if _, path, ok, _ := LatestReport(dir, true); !ok || filepath.Base(path) != "2026-10-23-misleading.json" {
		t.Errorf("latest misleading = %s %v", path, ok)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"summary"`) {
		t.Errorf("report file has no summary: %s", data)
	}
	if _, _, ok, err := LatestReport(t.TempDir(), false); ok || err != nil {
		t.Errorf("empty dir: ok %v, err %v", ok, err)
	}
}
