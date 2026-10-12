package exa_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// searchCases and extractCases are the mapping cases: each is answered by
// the cassette of the same name, and its output is golden. The live test
// (live_test.go) makes the same calls to record the cassettes.
var (
	searchCases = map[string]struct{ settings, input string }{
		"search-highlights": {
			input: `{"query": "  retrieval precision for RAG ", "max_results": 3,
				"include_domains": ["arxiv.org", "*.thoughtworks.com", "example.org/blog"],
				"exclude_domains": ["pinterest.com"],
				"start_published_date": "2025-01-01", "end_published_date": "2026-06-30",
				"category": "publication"}`,
		},
		"search-defaults": {
			input: `{"query": "retrieval precision for RAG"}`,
		},
		"search-text": {
			settings: `{"search_defaults": {"type": "fast", "contents": "text", "category": "news"}}`,
			input:    `{"query": "latest Go release", "start_published_date": "2026-08-01T09:30:00+02:00"}`,
		},
		"search-highlights-capped": {
			settings: `{"search_defaults": {"max_characters": 800}}`,
			input:    `{"query": "retrieval precision for RAG", "max_results": 20}`,
		},
		"search-deep": {
			settings: `{"search_defaults": {"model_chooses_type": true}}`,
			input:    `{"query": "What is the latest Go release?", "type": "deep", "max_results": 1}`,
		},
	}
	extractCases = map[string]struct{ settings, input string }{
		"contents-partial": {
			settings: `{"extract_defaults": {"max_characters": 5000, "max_age_hours": 24}}`,
			input: `{"urls": ["https://go.dev/doc/devel/release", "https://example.com/missing-page",
				"https://slow.example.net/", "https://unmentioned.example.com/a", "https://go.dev/doc/devel/release"]}`,
		},
		"contents-query": {
			input: `{"urls": ["https://exa.ai/blog/dynamic-highlights"], "query": "token efficiency"}`,
		},
	}
)

// TestDefinitions pins what the model and the policies see of each tool, its
// schemas and its metadata, under each settings that changes them.
func TestDefinitions(t *testing.T) {
	t.Parallel()
	for name, settings := range map[string]string{
		"definitions":                    `{}`,
		"definitions-model-chooses-type": `{"search_defaults": {"model_chooses_type": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, settings)
			var defs []tool.Definition
			for _, name := range []string{"exa_search", "exa_extract"} {
				defs = append(defs, r.tools[name].Definition())
			}
			b, err := json.Marshal(defs)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name, b)
		})
	}
}

// Criterion: search request. The request body (the cassette's, matched as
// JSON) carries the query, numResults, type, contents from settings and the
// filters; the response maps to the shared search shape (golden), in
// highlights and text modes and for a synthesizing type whose answer becomes
// "answer".
func TestSearchRequestAndMapping(t *testing.T) {
	t.Parallel()
	for name, tc := range searchCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			r.up.replay(t, name)
			out, err := r.call(t.Context(), "exa_search", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name+".output", out)
		})
	}
}

// Criterion: extract maps the pages that worked and the per-URL failures in
// statuses, including a URL Exa says nothing about; with a query the content
// is the page's highlights for it.
func TestExtractMapsSuccessesAndFailures(t *testing.T) {
	t.Parallel()
	want := map[string]struct{ results, failed int }{
		"contents-partial": {1, 3},
		"contents-query":   {1, 0},
	}
	for name, tc := range extractCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			r.up.replay(t, name)
			out, err := r.call(t.Context(), "exa_extract", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			var got extractShape
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if w := want[name]; len(got.Results) != w.results || len(got.Failed) != w.failed {
				t.Errorf("%d results and %d failed, want %d and %d: %s", len(got.Results), len(got.Failed), w.results, w.failed, out)
			}
			golden(t, name+".output", out)
		})
	}
	t.Run("a failed URL's result is dropped", func(t *testing.T) {
		t.Parallel()
		// Exa reports a status error for a page and still lists an (empty)
		// result for it: the page is a failure, not a result.
		r := newRig(t, "")
		r.up.push(ok([]byte(`{"results": [{"id": "https://a.example/", "url": "https://a.example/", "title": "", "text": ""},
			{"id": "https://b.example/", "url": "https://b.example/", "title": "B", "text": "page b"}],
			"statuses": [{"id": "https://a.example/", "status": "error", "error": {"tag": "CRAWL_NOT_FOUND", "httpStatusCode": 404}},
			{"id": "https://b.example/", "status": "success"}]}`)))
		out, err := r.call(t.Context(), "exa_extract", `{"urls": ["https://a.example/", "https://b.example/"]}`)
		if err != nil {
			t.Fatal(err)
		}
		var got extractShape
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if res := only(t, got.Results); res.URL != "https://b.example/" || len(got.Failed) != 1 || got.Failed[0].URL != "https://a.example/" {
			t.Errorf("output %s, want b in results and a in failed only", out)
		}
	})
}

// Criterion: settings validation. A missing key fails Build naming the
// variable; a literal api_key, unknown fields, bad enums and out-of-range
// values fail with ErrInvalidSettings.
func TestBuildRejectsSettings(t *testing.T) {
	t.Parallel()
	t.Run("missing key names the variable", func(t *testing.T) {
		t.Parallel()
		for name, env := range map[string]map[string]string{
			"unset": {},
			"empty": {"MY_EXA_KEY": "  "},
		} {
			f := exa.Factory(exa.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }))
			_, err := f.Build(t.Context(), json.RawMessage(`{"api_key_env": "MY_EXA_KEY"}`))
			if !errors.Is(err, exa.ErrMissingKey) || !strings.Contains(err.Error(), "MY_EXA_KEY") {
				t.Errorf("%s: Build = %v, want ErrMissingKey naming MY_EXA_KEY", name, err)
			}
		}
	})
	// Not in RunConnector, which always passes WithLookupEnv.
	t.Run("no LookupEnv reads nothing", func(t *testing.T) {
		t.Parallel()
		_, err := exa.Factory().Build(t.Context(), nil)
		if !errors.Is(err, exa.ErrMissingKey) || !strings.Contains(err.Error(), "EXA_API_KEY") {
			t.Errorf("Build = %v, want ErrMissingKey naming EXA_API_KEY", err)
		}
	})
	t.Run("defaults build", func(t *testing.T) {
		t.Parallel()
		for _, s := range []string{``, `null`, `{}`} {
			f := exa.Factory(exa.WithLookupEnv(func(k string) (string, bool) { return canaryKey, k == keyEnv }))
			tools, err := f.Build(t.Context(), json.RawMessage(s))
			if err != nil || len(tools) != 2 {
				t.Errorf("Build(%q) = %d tools, %v", s, len(tools), err)
			}
		}
	})
	t.Run("base_url points the tools at another server", func(t *testing.T) {
		t.Parallel()
		up := newUpstream(t)
		up.push(ok(cassetteBody(t, "search-highlights")))
		f := exa.Factory(
			exa.WithBaseURL("https://exa.invalid"),
			exa.WithLookupEnv(func(k string) (string, bool) { return canaryKey, k == keyEnv }),
		)
		tools, err := f.Build(t.Context(), json.RawMessage(`{"base_url": "`+up.srv.URL+`/"}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tools[0].Invoke(t.Context(), json.RawMessage(`{"query": "q"}`)); err != nil {
			t.Fatal(err)
		}
		if reqs := up.requests(); len(reqs) != 1 || reqs[0].path != "/search" {
			t.Errorf("requests = %+v, want one to /search on base_url", reqs)
		}
		for _, u := range []string{"https://exa.example/v1", "http://localhost:1", "http://[::1]:1"} {
			if _, err := f.Build(t.Context(), json.RawMessage(`{"base_url": "`+u+`"}`)); err != nil {
				t.Errorf("base_url %s: %v", u, err)
			}
		}
		if _, err := exa.Factory(exa.WithBaseURL("http://api.exa.ai"), exa.WithLookupEnv(func(string) (string, bool) { return canaryKey, true })).Build(t.Context(), nil); err == nil {
			t.Error("WithBaseURL over plain http to a remote host built")
		}
		// T049's suite passes WithBaseURL("") to mean the default.
		if _, err := exa.Factory(exa.WithBaseURL(""), exa.WithLookupEnv(func(string) (string, bool) { return canaryKey, true })).Build(t.Context(), nil); err != nil {
			t.Errorf(`WithBaseURL(""): %v, want the default`, err)
		}
	})

	bad := map[string]string{
		"literal API_KEY":         `{"API_KEY": "sk-literal-value-123"}`,
		"unknown nested field":    `{"search_defaults": {"type": "auto", "livecrawl": "always"}}`,
		"bad env name":            `{"api_key_env": "EXA-KEY"}`,
		"bad type":                `{"search_defaults": {"type": "neural"}}`,
		"bad contents":            `{"search_defaults": {"contents": "summary"}}`,
		"bad category":            `{"search_defaults": {"category": "github"}}`,
		"negative max_characters": `{"search_defaults": {"max_characters": -1}}`,
		"zero timeout string":     `{"timeout": "-1s"}`,
		"timeout too long":        `{"timeout": "1h"}`,
		"timeout not a string":    `{"timeout": 30}`,
		"max_age_hours too big":   `{"extract_defaults": {"max_age_hours": 721}}`,
		"max_age_hours too small": `{"extract_defaults": {"max_age_hours": -2}}`,
		"content cap negative":    `{"max_content_chars": {"search": -5}}`,
		"content cap not object":  `{"max_content_chars": 4000}`,
		"trailing data":           `{} {}`,
		"base_url not absolute":   `{"base_url": "api.exa.ai"}`,
		"base_url credentials":    `{"base_url": "https://user:pw@api.exa.ai"}`,
		"base_url scheme":         `{"base_url": "ftp://127.0.0.1/"}`,
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := exa.Factory(exa.WithLookupEnv(func(string) (string, bool) { return canaryKey, true }))
			_, err := f.Build(t.Context(), json.RawMessage(s))
			if !errors.Is(err, toolset.ErrInvalidSettings) {
				t.Fatalf("Build(%s) = %v, want ErrInvalidSettings", s, err)
			}
			if strings.Contains(err.Error(), "sk-literal-value-123") {
				t.Errorf("error echoes the literal key: %v", err)
			}
			if strings.Contains(strings.ToLower(name), "api_key") && !strings.Contains(err.Error(), "api_key_env") {
				t.Errorf("error %q does not point at api_key_env", err)
			}
		})
	}
}

// Criterion: input validation. Bad input is ErrInvalidInput and no request
// is sent (the rig's upstream fails any request it does not expect).
func TestInvalidInputSendsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ settings, tool, input string }{
		"empty query":            {``, "exa_search", `{"query": ""}`},
		"blank query":            {``, "exa_search", `{"query": " \n\t"}`},
		"missing query":          {``, "exa_search", `{}`},
		"max_results 21":         {``, "exa_search", `{"query": "q", "max_results": 21}`},
		"max_results negative":   {``, "exa_search", `{"query": "q", "max_results": -1}`},
		"bad date":               {``, "exa_search", `{"query": "q", "start_published_date": "last week"}`},
		"bad end date":           {``, "exa_search", `{"query": "q", "end_published_date": "2026-13-01"}`},
		"end before start":       {``, "exa_search", `{"query": "q", "start_published_date": "2026-02-01", "end_published_date": "2026-01-31"}`},
		"unknown category":       {``, "exa_search", `{"query": "q", "category": "github"}`},
		"people with dates":      {``, "exa_search", `{"query": "q", "category": "people", "start_published_date": "2026-01-01"}`},
		"company with excludes":  {``, "exa_search", `{"query": "q", "category": "company", "exclude_domains": ["x.com"]}`},
		"domain with scheme":     {``, "exa_search", `{"query": "q", "include_domains": ["https://example.com"]}`},
		"empty domain":           {``, "exa_search", `{"query": "q", "exclude_domains": [""]}`},
		"type when fixed":        {``, "exa_search", `{"query": "q", "type": "deep"}`},
		"unknown type":           {`{"search_defaults": {"model_chooses_type": true}}`, "exa_search", `{"query": "q", "type": "neural"}`},
		"unknown field":          {``, "exa_search", `{"query": "q", "numResults": 3}`},
		"not JSON":               {``, "exa_search", `{"query":`},
		"no urls":                {``, "exa_extract", `{"urls": []}`},
		"missing urls":           {``, "exa_extract", `{"query": "q"}`},
		"eleven urls":            {``, "exa_extract", `{"urls": [` + strings.Repeat(`"https://a.example/",`, 10) + `"https://b.example/"]}`},
		"ftp url":                {``, "exa_extract", `{"urls": ["ftp://example.com/file"]}`},
		"relative url":           {``, "exa_extract", `{"urls": ["/docs/page"]}`},
		"no host":                {``, "exa_extract", `{"urls": ["https:///path"]}`},
		"url with credentials":   {``, "exa_extract", `{"urls": ["https://user:pw@example.com/"]}`},
		"url too long":           {``, "exa_extract", `{"urls": ["https://example.com/` + strings.Repeat("a", 2100) + `"]}`},
		"one bad url among good": {``, "exa_extract", `{"urls": ["https://example.com/", "javascript:alert(1)"]}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			_, err := r.call(t.Context(), tc.tool, tc.input)
			if !errors.Is(err, tool.ErrInvalidInput) || errors.Is(err, tool.ErrToolFailed) {
				t.Errorf("err = %v, want ErrInvalidInput only", err)
			}
			if n := len(r.up.requests()); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}

// Criterion: errors. Each upstream status maps to its class, with one
// request: no retry except on 429.
func TestUpstreamErrors(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("numResults is way too large; ", 50)
	cases := map[string]struct {
		resp      response
		want      error
		wantInMsg string
	}{
		"400 carries Exa's message": {
			resp: response{status: 400, body: []byte(`{"requestId": "3f2e1d0c9b8a76543210fedcba987654", "error": "numResults must be less than or equal to 100", "tag": "INVALID_REQUEST"}`)}, want: tool.ErrInvalidInput,
			wantInMsg: "numResults must be less than or equal to 100",
		},
		"422 carries Exa's message": {
			resp: response{status: 422, body: []byte(`{"requestId":"r","error":"query too long","tag":"INVALID_REQUEST"}`)}, want: tool.ErrInvalidInput,
			wantInMsg: "query too long",
		},
		"400 without a JSON body":  {resp: response{status: 400, body: []byte("<html>bad</html>")}, want: tool.ErrInvalidInput},
		"400 message capped":       {resp: response{status: 400, body: mustJSON(t, map[string]string{"error": long, "tag": "INVALID_REQUEST"})}, want: tool.ErrInvalidInput, wantInMsg: "…[truncated]"},
		"401 rejects the key":      {resp: response{status: 401, body: []byte(`{"error":"Invalid API key provided: 5f0c"}`)}, want: exa.ErrKeyRejected},
		"403 rejects the key":      {resp: response{status: 403, body: []byte(`{"error":"forbidden"}`)}, want: exa.ErrKeyRejected},
		"402 is a failure":         {resp: response{status: 402, body: []byte(`{"error":"out of credits"}`)}, want: exa.ErrUpstream},
		"500 is status only":       {resp: response{status: 500, body: []byte(`{"error":"internal detail"}`)}, want: exa.ErrUpstream},
		"503 is not retried":       {resp: response{status: 503, header: map[string]string{"Retry-After": "1"}}, want: exa.ErrUpstream},
		"200 with a broken body":   {resp: ok([]byte(`{"results": [`)), want: exa.ErrUpstream},
		"200 with the wrong shape": {resp: ok([]byte(`{"results": {"title": 1}}`)), want: exa.ErrUpstream},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "")
			r.up.push(tc.resp)
			_, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !errors.Is(tc.want, tool.ErrInvalidInput) && !errors.Is(err, tool.ErrToolFailed) {
				t.Errorf("err = %v, want ErrToolFailed", err)
			}
			if n := len(r.up.requests()); n != 1 {
				t.Errorf("%d requests, want 1 (no retry)", n)
			}
			msg := err.Error()
			if tc.wantInMsg != "" && !strings.Contains(msg, tc.wantInMsg) {
				t.Errorf("error %q does not carry Exa's message %q", msg, tc.wantInMsg)
			}
			// What reaches the model from an error body is at most 300
			// characters; failures other than 400/422 echo nothing.
			if utf8.RuneCountInString(msg) > 300+200 {
				t.Errorf("error of %d characters is not capped", utf8.RuneCountInString(msg))
			}
			if strings.Contains(msg, "exa_extract") {
				t.Errorf("error %q carries the content truncation hint", msg)
			}
			for _, echoed := range []string{"Invalid API key provided", "forbidden", "internal detail", "out of credits"} {
				if strings.Contains(msg, echoed) {
					t.Errorf("error %q echoes the response body", msg)
				}
			}
		})
	}
}

// Criterion: a 429 is retried once, after Retry-After on the injected clock.
func TestRateLimitRetry(t *testing.T) {
	t.Parallel()
	limited := func(retryAfter string) response {
		return response{status: 429, header: map[string]string{"Retry-After": retryAfter}, body: []byte(`{"error":"rate limit","tag":"RATE_LIMIT_ERROR"}`)}
	}
	cases := map[string]struct {
		responses []response
		wait      time.Duration // the wait the client must arm; 0: none
		want      error         // nil: success
		requests  int
	}{
		"then success":           {[]response{limited("3"), ok(cassetteBody(t, "search-highlights"))}, 3 * time.Second, nil, 2},
		"twice fails":            {[]response{limited("2"), limited("2")}, 2 * time.Second, exa.ErrRateLimited, 2},
		"HTTP date":              {[]response{limited(start.Add(7 * time.Second).Format(http.TimeFormat)), ok(cassetteBody(t, "search-highlights"))}, 7 * time.Second, nil, 2},
		"no header waits 1s":     {[]response{{status: 429}, ok(cassetteBody(t, "search-highlights"))}, time.Second, nil, 2},
		"10s is honored":         {[]response{limited("10"), ok(cassetteBody(t, "search-highlights"))}, 10 * time.Second, nil, 2},
		"over 10s is not waited": {[]response{limited("11")}, 0, exa.ErrRateLimited, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "")
			r.up.push(tc.responses...)
			done := make(chan error, 1)
			go func() {
				_, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
				done <- err
			}()
			if tc.wait > 0 {
				waitForTimer(t, r)
				// Just before the wait ends the timer must still be pending:
				// a client that waited less would have fired and retried.
				r.clock.Advance(tc.wait - time.Millisecond)
				waitForTimer(t, r)
				if n := len(r.up.requests()); n != 1 {
					t.Fatalf("%d requests before Retry-After elapsed, want 1", n)
				}
				r.clock.Advance(time.Millisecond)
			}
			err := waitDone(t, done)
			if (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if n := len(r.up.requests()); n != tc.requests {
				t.Errorf("%d requests, want %d", n, tc.requests)
			}
		})
	}
}

// Criterion: a cancelled ctx stops the retry wait at once, without a second
// request.
func TestCancelStopsRetryWait(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	r.up.push(response{status: 429, header: map[string]string{"Retry-After": "5"}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := r.call(ctx, "exa_search", `{"query": "q"}`)
		done <- err
	}()
	waitForTimer(t, r)
	cancel()
	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := len(r.up.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// waitForTimer waits until the client has armed its retry timer. A correct
// client arms it at once; the deadline only turns a client that never does
// into a failure instead of a hang.
func waitForTimer(t *testing.T, r *rig) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := r.clock.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("no retry timer armed: %v", err)
	}
}

// only returns the one result, failing the test (not panicking, which would
// hide every other test's result) when there is not exactly one.
func only[T any](t *testing.T, results []T) T {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("%d results, want 1", len(results))
	}
	return results[0]
}

// waitDone returns the call's error. The real-time deadline turns a client
// stuck on a timer the test never advances (an extra retry, an unbounded
// Retry-After, a wait that ignores ctx) into a failure instead of a hang.
func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the call is still waiting: an unexpected retry timer, or ctx ignored")
		return nil
	}
}

// Criterion: limits. Content over the cap is cut at a rune boundary with the
// marker, metadata is capped too, and a body over 8 MB fails cleanly.
func TestLimits(t *testing.T) {
	t.Parallel()
	t.Run("truncation at a rune boundary", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, `{"max_content_chars": {"search": 10, "extract": 7}, "search_defaults": {"contents": "text"}}`)
		text := "ééééé€€€€€🙂🙂🙂" // 13 runes, 2-4 bytes each
		title := strings.Repeat("ü", 1500)
		r.up.push(ok(mustJSON(t, map[string]any{"results": []any{map[string]any{"url": "https://a.example/", "title": title, "text": text}}})))
		out, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
		if err != nil {
			t.Fatal(err)
		}
		checkShape(t, "exa_search", out, 10)
		var got searchShape
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if want := "ééééé€€€€€…[truncated 3 characters; call exa_extract on this URL for the full page]"; only(t, got.Results).Content != want {
			t.Errorf("content = %q, want %q", only(t, got.Results).Content, want)
		}
		if title := only(t, got.Results).Title; utf8.RuneCountInString(title) != 1000+utf8.RuneCountInString("…[truncated]") || !strings.HasSuffix(title, "ü…[truncated]") {
			t.Errorf("title of %d characters is not capped with the short marker", utf8.RuneCountInString(title))
		}

		r.up.push(ok(mustJSON(t, map[string]any{"results": []any{map[string]any{"id": "https://a.example/", "url": "https://a.example/", "text": text}}})))
		out, err = r.call(t.Context(), "exa_extract", `{"urls": ["https://a.example/"]}`)
		if err != nil {
			t.Fatal(err)
		}
		checkShape(t, "exa_extract", out, 7)
		var ex extractShape
		if err := json.Unmarshal(out, &ex); err != nil {
			t.Fatal(err)
		}
		if want := "ééééé€€…[truncated 6 characters; call exa_extract again with a query to get the passages that matter]"; only(t, ex.Results).Content != want {
			t.Errorf("extract content = %q, want %q", only(t, ex.Results).Content, want)
		}
	})
	t.Run("the marker counts what was cut", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, `{"search_defaults": {"contents": "text", "max_characters": 20000}}`)
		r.up.push(ok(mustJSON(t, map[string]any{"results": []any{map[string]any{"url": "https://a.example/", "text": strings.Repeat("x", 16400)}}})))
		out, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
		if err != nil {
			t.Fatal(err)
		}
		var got searchShape
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if want := strings.Repeat("x", 4000) + "…[truncated 12,400 characters; call exa_extract on this URL for the full page]"; only(t, got.Results).Content != want {
			c := only(t, got.Results).Content
			t.Errorf("content of %d bytes ends %q", len(c), c[max(len(c)-120, 0):])
		}
	})
	t.Run("content at the cap is kept whole", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, `{"max_content_chars": {"search": 13}, "search_defaults": {"contents": "text"}}`)
		text := "ééééé€€€€€🙂🙂🙂"
		r.up.push(ok(mustJSON(t, map[string]any{"results": []any{map[string]any{"url": "https://a.example/", "text": text}}})))
		out, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
		if err != nil {
			t.Fatal(err)
		}
		var got searchShape
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if only(t, got.Results).Content != text {
			t.Errorf("content = %q, want it whole", only(t, got.Results).Content)
		}
	})
	t.Run("body over 8 MB", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "")
		const limit = 8 << 20
		for _, size := range []int{limit, limit + 1} {
			// A valid search response of exactly size bytes.
			prefix, suffix := `{"results":[{"url":"https://a.example/","highlights":["`, `"]}]}`
			body := prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix
			r.up.push(ok([]byte(body)))
			_, err := r.call(t.Context(), "exa_search", `{"query": "q"}`)
			switch {
			case size == limit && err != nil:
				t.Errorf("body of exactly 8 MB: %v", err)
			case size > limit && (!errors.Is(err, exa.ErrResponseTooLarge) || !errors.Is(err, tool.ErrToolFailed)):
				t.Errorf("body over 8 MB: err = %v, want ErrResponseTooLarge", err)
			}
		}
	})
}

// Criterion: canary, beyond RunConnector's (which echoes the key raw in
// 400, 401 and 500 bodies): the key appears in no output, error or log line
// when Exa echoes it JSON-escaped, or in any field of a 200's results.
func TestKeyNeverLeaks(t *testing.T) {
	t.Parallel()
	escaped := strings.ReplaceAll(canaryKey, "a", `a`) // the same string, encoded differently
	echo := func(k string) string {
		return `{"results": [{"id": "https://a.example/` + k + `", "url": "https://a.example/` + k + `",
			"title": "key ` + k + `", "author": "` + k + `", "publishedDate": "` + k + `",
			"text": "your key is ` + k + `", "highlights": ["key: ` + k + `"]}],
			"statuses": [{"id": "https://b.example/` + k + `", "status": "error", "error": {"tag": "` + k + `", "httpStatusCode": 500}}],
			"output": {"content": "answer ` + k + `"}}`
	}
	cases := map[string]struct {
		settings, tool, input string
		resp                  response
	}{
		"400 echoes it escaped":       {``, "exa_search", `{"query": "q"}`, response{status: 400, body: []byte(`{"error": "bad key ` + escaped + `"}`)}},
		"search results, highlights":  {`{"search_defaults": {"model_chooses_type": true}}`, "exa_search", `{"query": "q", "type": "deep"}`, ok([]byte(echo(canaryKey)))},
		"search results, text":        {`{"search_defaults": {"contents": "text"}}`, "exa_search", `{"query": "q"}`, ok([]byte(echo(escaped)))},
		"extract results and failure": {``, "exa_extract", `{"urls": ["https://a.example/"]}`, ok([]byte(echo(canaryKey)))},
		"extract focused":             {``, "exa_extract", `{"urls": ["https://a.example/"], "query": "q"}`, ok([]byte(echo(escaped)))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			r.up.push(tc.resp)
			out, err := r.call(t.Context(), tc.tool, tc.input) // the rig checks output, error and log
			if err == nil && !strings.Contains(string(out), "[redacted]") {
				t.Errorf("output %s: want the echoed key replaced with [redacted]", out)
			}
		})
	}
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
