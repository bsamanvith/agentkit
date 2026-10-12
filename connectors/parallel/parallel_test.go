package parallel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestSearch: the request body matches the documented /v1/search JSON (the
// cassette's request: objective, search_queries and mode at the top,
// max_results, source_policy, location, fetch_policy and excerpt_settings in
// advanced_settings), and the response maps to E10's search shape (golden).
func TestSearch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, settings, input string
	}{
		{
			// The defaults: objective is the query, mode fast, 5 results.
			name:  "search-defaults",
			input: `{"query": "latest Go release"}`,
		},
		{
			// Every input and setting placed; the model picks the mode. The
			// response has a result without a URL (dropped) and more results
			// than asked for (cut to max_results).
			name: "search-filters",
			settings: `{"search_defaults": {"mode": "basic", "allow_model_mode": true, "max_chars_per_result": 1500,
				"location": "GB", "fetch_policy": {"max_age_seconds": 3600}}}`,
			input: `{"query": "Go 1.27 release notes",
				"objective": "Find what changed in the Go 1.27 release, from official sources.",
				"extra_queries": ["Go 1.27 new features", "golang 1.27 changelog"], "max_results": 2,
				"include_domains": ["go.dev", "github.com/golang"], "after_date": "2026-01-01", "mode": "advanced"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			r.up.script(reply{cassette: tc.name})
			out, err := r.call(t.Context(), "parallel_search", tc.input)
			must(t, err)
			golden(t, tc.name, out)
			if n := len(r.up.requests()); n != 1 {
				t.Errorf("%d requests, want 1", n)
			}
		})
	}
}

// TestExtract: /v1/extract gets the options inside advanced_settings, and
// results and errors map to results and failed, in excerpts mode and in
// full-content mode (golden).
func TestExtract(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, settings, input string
	}{
		{
			name:  "extract-excerpts",
			input: `{"urls": ["https://go.dev/doc/go1.27", "https://example.com/missing-page"], "objective": "What changed in the go command?"}`,
		},
		{
			name:     "extract-full",
			settings: `{"extract_defaults": {"full_content": true, "fetch_policy": {"max_age_seconds": 600, "timeout_seconds": 30, "disable_cache_fallback": true}}}`,
			input:    `{"urls": ["https://go.dev/doc/go1.27", "https://example.com/timeout"]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.settings)
			r.up.script(reply{cassette: tc.name})
			out, err := r.call(t.Context(), "parallel_extract", tc.input)
			must(t, err)
			golden(t, tc.name, out)
		})
	}
}

// TestExtractOptionsInsideAdvancedSettings pins what /v1 moved: full_content,
// fetch_policy and excerpt_settings are never top-level (a 422 from
// Parallel), and the configured ones are under advanced_settings.
func TestExtractOptionsInsideAdvancedSettings(t *testing.T) {
	t.Parallel()
	r := newRig(t, `{"extract_defaults": {"full_content": true, "fetch_policy": {"max_age_seconds": 900}}}`)
	r.up.script(reply{status: 200, body: `{"results": [], "errors": []}`})
	_, err := r.call(t.Context(), "parallel_extract", `{"urls": ["https://go.dev/"]}`)
	must(t, err)
	var body struct {
		Top      map[string]json.RawMessage
		Advanced map[string]json.RawMessage `json:"advanced_settings"`
	}
	raw := r.up.requests()[0].body
	must(t, json.Unmarshal(raw, &body.Top))
	must(t, json.Unmarshal(raw, &body))
	for _, moved := range []string{"full_content", "fetch_policy", "excerpt_settings", "excerpts"} {
		if _, ok := body.Top[moved]; ok {
			t.Errorf("%q sent top-level: %s", moved, raw)
		}
	}
	if string(body.Advanced["full_content"]) != "true" || !jsonEqual(body.Advanced["fetch_policy"], []byte(`{"max_age_seconds": 900}`)) {
		t.Errorf("advanced_settings = %s, want full_content true and the fetch_policy", raw)
	}
}

// TestModeChoice: without allow_model_mode the tool has no mode input and
// sends the configured mode; with it, the model's mode is sent.
func TestModeChoice(t *testing.T) {
	t.Parallel()
	fixed := newRig(t, `{"search_defaults": {"mode": "turbo"}}`)
	var schema struct{ Properties map[string]any }
	must(t, json.Unmarshal(fixed.tools["parallel_search"].Definition().InputSchema, &schema))
	if _, ok := schema.Properties["mode"]; ok {
		t.Errorf("mode is an input although settings fix it: %v", schema.Properties)
	}
	if _, err := fixed.call(t.Context(), "parallel_search", `{"query": "q", "mode": "advanced"}`); !isClass(err, tool.ErrInvalidInput) {
		t.Errorf("mode given when fixed: err = %v, want ErrInvalidInput", err)
	}
	fixed.up.script(reply{status: 200, body: `{"results": []}`})
	_, err := fixed.call(t.Context(), "parallel_search", `{"query": "q"}`)
	must(t, err)

	chosen := newRig(t, `{"search_defaults": {"allow_model_mode": true}}`)
	chosen.up.script(reply{status: 200, body: `{"results": []}`}, reply{status: 200, body: `{"results": []}`})
	_, err = chosen.call(t.Context(), "parallel_search", `{"query": "q", "mode": "advanced"}`)
	must(t, err)
	_, err = chosen.call(t.Context(), "parallel_search", `{"query": "q"}`)
	must(t, err)

	for _, tc := range []struct {
		r    *rig
		want []string
	}{{fixed, []string{"turbo"}}, {chosen, []string{"advanced", "fast"}}} {
		reqs := tc.r.up.requests()
		for i, want := range tc.want {
			var b struct{ Mode string }
			must(t, json.Unmarshal(reqs[i].body, &b))
			if b.Mode != want {
				t.Errorf("request mode = %q, want %q", b.Mode, want)
			}
		}
	}
}

// TestSettingsValidation: bad settings fail Build with ErrInvalidSettings, a
// missing key fails naming the variable, and good ones build without a
// request.
func TestSettingsValidation(t *testing.T) {
	t.Parallel()
	env := lookup(map[string]string{"PARALLEL_API_KEY": canary, "OTHER_KEY": canary, "BLANK_KEY": "  "})
	build := func(settings string, env func(string) (string, bool)) error {
		_, err := parallel.Factory(parallel.WithLookupEnv(env), parallel.WithHTTPClient(noNetwork(t))).
			Build(t.Context(), json.RawMessage(settings))
		return err
	}
	for _, tc := range []struct{ name, settings string }{
		{"literal api_key", `{"api_key": "pk-123"}`},
		{"literal API_KEY", `{"API_KEY": "pk-123"}`},
		{"unknown field", `{"search_default": {}}`},
		{"unknown nested field", `{"search_defaults": {"depth": "deep"}}`},
		{"bad mode", `{"search_defaults": {"mode": "ultra"}}`},
		{"bad location", `{"search_defaults": {"location": "uk-en"}}`},
		{"negative max_chars_per_result", `{"search_defaults": {"max_chars_per_result": -1}}`},
		{"max_age_seconds under 600", `{"extract_defaults": {"fetch_policy": {"max_age_seconds": 60}}}`},
		{"bad timeout", `{"timeout": "soon"}`},
		{"negative timeout", `{"timeout": "-1s"}`},
		{"negative content cap", `{"max_content_chars": {"search": -5}}`},
		{"bad env name", `{"api_key_env": "MY KEY"}`},
		{"http base_url off loopback", `{"base_url": "http://api.parallel.ai"}`},
		{"base_url with credentials", `{"base_url": "https://user:pw@api.parallel.ai"}`},
		{"base_url not a URL", `{"base_url": "api.parallel.ai"}`},
		{"trailing data", `{} {}`},
	} {
		if err := build(tc.settings, env); !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("%s: Build err = %v, want ErrInvalidSettings", tc.name, err)
		}
	}
	if err := build(`{"api_key": "pk-123"}`, env); err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Errorf("literal api_key: err = %v, want a pointer to api_key_env", err)
	}

	for _, tc := range []struct{ name, settings, variable string }{
		{"default variable unset", `{}`, "PARALLEL_API_KEY"},
		{"named variable unset", `{"api_key_env": "MISSING_KEY"}`, "MISSING_KEY"},
		{"variable blank", `{"api_key_env": "BLANK_KEY"}`, "BLANK_KEY"},
	} {
		err := build(tc.settings, lookup(map[string]string{"BLANK_KEY": " "}))
		if err == nil || !strings.Contains(err.Error(), tc.variable) {
			t.Errorf("%s: Build err = %v, want one naming %s", tc.name, err, tc.variable)
		}
	}

	for _, settings := range []string{
		``, `{}`, `null`,
		`{"api_key_env": "OTHER_KEY", "timeout": "5s", "max_content_chars": {"search": 100, "extract": 200}}`,
		`{"base_url": "http://127.0.0.1:9999/"}`, `{"base_url": "http://localhost:8080"}`, `{"base_url": "https://proxy.example.com/parallel"}`,
		`{"search_defaults": {"mode": "advanced", "location": "us", "fetch_policy": {"max_age_seconds": 600}}, "extract_defaults": {"full_content": true}}`,
	} {
		if err := build(settings, env); err != nil {
			t.Errorf("settings %s: Build err = %v, want success", settings, err)
		}
	}
}

// noNetwork is an HTTP client that fails the test on any request: Build must
// not make one.
func noNetwork(t *testing.T) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected request to %s", r.URL)
		return nil, errors.New("no network in this test")
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestOptionalToolsetSkipped: through toolset.Build, a missing key skips an
// optional toolset with a problem naming the variable, and fails a required
// one. (/readyz showing it degraded: cmd/agentkit TestE2EServeParallelWithoutKey.)
func TestOptionalToolsetSkipped(t *testing.T) {
	t.Parallel()
	catalog, err := toolset.NewCatalog(toolset.Demo(), parallel.Factory(parallel.WithLookupEnv(lookup(nil))))
	must(t, err)
	regs, problems, err := toolset.Build(t.Context(), catalog, []toolset.Spec{
		{Name: "demo", Type: "demo"},
		{Name: "search", Type: "parallel", Optional: true},
	})
	must(t, err)
	if _, ok := regs["search"]; ok || len(problems) != 1 || problems[0].Toolset != "search" ||
		!strings.Contains(problems[0].Error(), "PARALLEL_API_KEY") {
		t.Errorf("problems = %v, regs = %v; want search skipped, naming PARALLEL_API_KEY", problems, regs)
	}
	if _, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{Name: "search", Type: "parallel"}}); err == nil {
		t.Error("a required toolset without its key built")
	}
}

// TestInputValidation: bad input is ErrInvalidInput (not a tool failure) and
// sends no request (the rig's upstream fails on an unscripted request).
func TestInputValidation(t *testing.T) {
	t.Parallel()
	r := newRig(t, `{"search_defaults": {"allow_model_mode": true}}`)
	long := strings.Repeat("x", 1001)
	for _, tc := range []struct{ name, tool, input string }{
		{"no query", "parallel_search", `{}`},
		{"empty query", "parallel_search", `{"query": ""}`},
		{"blank query", "parallel_search", `{"query": "  \t "}`},
		{"query too long", "parallel_search", `{"query": "` + long + `"}`},
		{"5 extra queries", "parallel_search", `{"query": "q", "extra_queries": ["a", "b", "c", "d", "e"]}`},
		{"empty extra query", "parallel_search", `{"query": "q", "extra_queries": ["a", " "]}`},
		{"max_results 0 is the default, -1 is not", "parallel_search", `{"query": "q", "max_results": -1}`},
		{"max_results 21", "parallel_search", `{"query": "q", "max_results": 21}`},
		{"max_results a string", "parallel_search", `{"query": "q", "max_results": "5"}`},
		{"bad date", "parallel_search", `{"query": "q", "after_date": "2026-13-01"}`},
		{"date with time", "parallel_search", `{"query": "q", "after_date": "2026-01-01T00:00:00Z"}`},
		{"include and exclude", "parallel_search", `{"query": "q", "include_domains": ["a.com"], "exclude_domains": ["b.com"]}`},
		{"domain with scheme", "parallel_search", `{"query": "q", "include_domains": ["https://a.com"]}`},
		{"empty domain", "parallel_search", `{"query": "q", "exclude_domains": [""]}`},
		{"bad mode", "parallel_search", `{"query": "q", "mode": "ultra"}`},
		{"objective too long", "parallel_search", `{"query": "q", "objective": "` + strings.Repeat("y", 5001) + `"}`},
		{"unknown field", "parallel_search", `{"query": "q", "depth": 3}`},
		{"no urls", "parallel_extract", `{"urls": []}`},
		{"urls missing", "parallel_extract", `{}`},
		{"11 urls", "parallel_extract", `{"urls": ` + jsonString(repeatURL(11)) + `}`},
		{"ftp url", "parallel_extract", `{"urls": ["ftp://example.com/file"]}`},
		{"relative url", "parallel_extract", `{"urls": ["/docs"]}`},
		{"url without host", "parallel_extract", `{"urls": ["https:///path"]}`},
		{"javascript url", "parallel_extract", `{"urls": ["javascript:alert(1)"]}`},
	} {
		if _, err := r.call(t.Context(), tc.tool, tc.input); !isClass(err, tool.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want only ErrInvalidInput", tc.name, err)
		}
	}
	if n := len(r.up.requests()); n != 0 {
		t.Errorf("%d requests sent for invalid input, want none", n)
	}
	// The boundaries are accepted.
	r.up.script(reply{status: 200, body: `{"results": []}`}, reply{status: 200, body: `{"results": [], "errors": []}`})
	_, err := r.call(t.Context(), "parallel_search", `{"query": "q", "extra_queries": ["a", "b", "c", "d"], "max_results": 20}`)
	must(t, err)
	_, err = r.call(t.Context(), "parallel_extract", `{"urls": `+jsonString(repeatURL(10))+`}`)
	must(t, err)
}

func repeatURL(n int) []string {
	urls := make([]string, n)
	for i := range urls {
		urls[i] = "https://example.com/" + strings.Repeat("p", i+1)
	}
	return urls
}

// TestUpstreamErrors: each status maps to its class, with Parallel's message
// for 400/422 (capped at 300 characters), the status only otherwise, and one
// request: no retry except on 429.
func TestUpstreamErrors(t *testing.T) {
	t.Parallel()
	longMsg := strings.Repeat("é", 299) + "XYZ-past-the-cap"
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		class    error
		contains string
		excludes string
	}{
		{"400 with Parallel's message", 400, `{"type": "error", "error": {"ref_id": "r1", "message": "search_queries must not be empty"}}`, tool.ErrInvalidInput, "search_queries must not be empty", ""},
		{"422 FastAPI detail", 422, `{"detail": [{"loc": ["body", "mode"], "msg": "Input should be 'turbo', 'fast', 'basic' or 'advanced'"}]}`, tool.ErrInvalidInput, "Input should be", ""},
		{"422 message capped", 422, `{"type": "error", "error": {"ref_id": "r2", "message": "` + longMsg + `"}}`, tool.ErrInvalidInput, strings.Repeat("é", 299) + "X…", "YZ-past"},
		{"400 not JSON", 400, `<html>bad request</html>`, tool.ErrInvalidInput, "HTTP 400", "html"},
		{"401", 401, `{"type": "error", "error": {"ref_id": "r3", "message": "invalid key pk-something"}}`, tool.ErrToolFailed, "rejected the API key", "pk-something"},
		{"403", 403, `{}`, tool.ErrToolFailed, "rejected the API key", ""},
		{"500", 500, `{"type": "error", "error": {"ref_id": "r4", "message": "internal detail"}}`, tool.ErrToolFailed, "HTTP 500", "internal detail"},
		{"502", 502, `bad gateway`, tool.ErrToolFailed, "HTTP 502", ""},
		{"503", 503, ``, tool.ErrToolFailed, "HTTP 503", ""},
		{"200 not JSON", 200, `{"results": [`, tool.ErrToolFailed, "", ""},
		{"200 wrong types", 200, `{"results": "none"}`, tool.ErrToolFailed, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, "")
			r.up.script(reply{status: tc.status, body: tc.body})
			_, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
			if !isClass(err, tc.class) {
				t.Fatalf("err = %v, want only %v", err, tc.class)
			}
			if !strings.Contains(err.Error(), tc.contains) || (tc.excludes != "" && strings.Contains(err.Error(), tc.excludes)) {
				t.Errorf("err = %q, want it to contain %q and not %q", err, tc.contains, tc.excludes)
			}
			if n := len(r.up.requests()); n != 1 {
				t.Errorf("%d requests, want 1 (no retry)", n)
			}
		})
	}
}

// TestRateLimit: a 429 is retried once, after Retry-After on the fake clock;
// a second 429 fails; a Retry-After over 10 s fails at once; a cancelled ctx
// stops the wait.
func TestRateLimit(t *testing.T) {
	t.Parallel()
	waitCtx := func(t *testing.T) context.Context {
		// Only a broken implementation (no timer armed, or one that already
		// fired) makes this wait run out; a correct one returns at once.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		t.Cleanup(cancel)
		return ctx
	}
	type result struct {
		out json.RawMessage
		err error
	}
	start := func(r *rig, ctx context.Context) chan result {
		done := make(chan result, 1)
		go func() {
			out, err := r.call(ctx, "parallel_search", `{"query": "q"}`)
			done <- result{out, err}
		}()
		return done
	}

	t.Run("retried after Retry-After", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "")
		r.up.script(reply{status: 429, header: map[string]string{"Retry-After": "3"}, body: `{}`},
			reply{status: 200, body: `{"results": []}`})
		done := start(r, t.Context())
		must(t, r.clk.WaitForTimers(waitCtx(t), 1))
		r.clk.Advance(3*time.Second - time.Millisecond)
		// Still waiting: the timer is for the full Retry-After.
		must(t, r.clk.WaitForTimers(waitCtx(t), 1))
		if n := len(r.up.requests()); n != 1 {
			t.Fatalf("%d requests before Retry-After passed, want 1", n)
		}
		r.clk.Advance(time.Millisecond)
		res := <-done
		must(t, res.err)
		if n := len(r.up.requests()); n != 2 {
			t.Errorf("%d requests, want 2", n)
		}
	})
	t.Run("second 429 fails", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "")
		r.up.script(reply{status: 429, body: `{}`}, reply{status: 429, header: map[string]string{"Retry-After": "1"}, body: `{}`})
		done := start(r, t.Context())
		must(t, r.clk.WaitForTimers(waitCtx(t), 1))
		r.clk.Advance(time.Second) // no Retry-After: one second
		res := <-done
		if !isClass(res.err, tool.ErrToolFailed) || !strings.Contains(res.err.Error(), "rate limited") {
			t.Errorf("err = %v, want rate limited (ErrToolFailed)", res.err)
		}
		if n := len(r.up.requests()); n != 2 {
			t.Errorf("%d requests, want 2", n)
		}
	})
	t.Run("long Retry-After fails at once", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "")
		r.up.script(reply{status: 429, header: map[string]string{"Retry-After": "60"}, body: `{}`})
		_, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
		if !isClass(err, tool.ErrToolFailed) {
			t.Errorf("err = %v, want ErrToolFailed", err)
		}
		if n := len(r.up.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})
	t.Run("cancel stops the wait", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "")
		r.up.script(reply{status: 429, header: map[string]string{"Retry-After": "5"}, body: `{}`})
		ctx, cancel := context.WithCancel(t.Context())
		done := start(r, ctx)
		must(t, r.clk.WaitForTimers(waitCtx(t), 1))
		cancel()
		res := <-done
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", res.err)
		}
		if n := len(r.up.requests()); n != 1 {
			t.Errorf("%d requests, want 1", n)
		}
	})
}

// TestHangingUpstream: a request that never answers ends with the context's
// error when ctx expires.
func TestHangingUpstream(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	r.up.script(reply{hang: true})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := r.call(ctx, "parallel_search", `{"query": "q"}`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

// TestTruncation: content over max_content_chars is cut at a character
// boundary and marked (the rig checks the count; this checks the bytes).
func TestTruncation(t *testing.T) {
	t.Parallel()
	r := newRig(t, `{"max_content_chars": {"search": 7, "extract": 5}, "extract_defaults": {"full_content": true}}`)
	r.up.script(
		reply{status: 200, body: `{"results": [{"url": "https://a.example/", "title": "t", "excerpts": ["héllo🙂w", "orld"]},
			{"url": "https://b.example/", "title": "short", "excerpts": ["1234567"]}]}`},
		reply{status: 200, body: `{"results": [{"url": "https://a.example/", "excerpts": ["ignored"], "full_content": "日本語のテキストです"}], "errors": []}`},
	)
	out, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
	must(t, err)
	var s struct{ Results []struct{ Content string } }
	must(t, json.Unmarshal(out, &s))
	if got := []string{s.Results[0].Content, s.Results[1].Content}; got[0] != "héllo🙂w…[truncated 7 characters; call parallel_extract on this URL for more of the page]" || got[1] != "1234567" {
		t.Errorf("search contents = %q, want the first cut after 7 characters and the second whole", got)
	}
	out, err = r.call(t.Context(), "parallel_extract", `{"urls": ["https://a.example/"]}`)
	must(t, err)
	var e struct{ Results []struct{ Content string } }
	must(t, json.Unmarshal(out, &e))
	if got := e.Results[0].Content; got != "日本語のテ…[truncated 5 characters; the rest of the page is not available through this tool]" || !utf8.ValidString(got) {
		t.Errorf("extract content = %q, want 5 characters of the full content and the marker", got)
	}
}

// TestBodyLimit: a response over 8 MiB fails, and the connector reads at most
// one byte past the limit.
func TestBodyLimit(t *testing.T) {
	t.Parallel()
	const limit = 8 << 20
	for _, status := range []int{200, 422} {
		var read countingReader
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(&read), Request: r,
			}, nil
		})}
		r := newRig(t, "", withClient(client))
		_, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
		if !isClass(err, tool.ErrToolFailed) {
			t.Errorf("HTTP %d: err = %v, want ErrToolFailed", status, err)
		}
		if n := read.n.Load(); n > limit+1 {
			t.Errorf("HTTP %d: read %d bytes, want at most %d", status, n, limit+1)
		}
	}
}

// TestCanaryEchoed: when the upstream echoes the key in an error, a result
// or a warning, it is redacted from every output, error and log line (the
// rig checks all of them after every test; here the upstream does the
// echoing).
func TestCanaryEchoed(t *testing.T) {
	t.Parallel()
	r := newRig(t, `{"extract_defaults": {"full_content": true}}`)
	r.up.script(
		reply{status: 422, body: `{"type": "error", "error": {"ref_id": "r", "message": "bad key ` + canary + ` in request"}}`},
		reply{status: 401, body: `{"type": "error", "error": {"ref_id": "r", "message": "key ` + canary + `"}}`},
		reply{status: 200, body: `{"results": [{"url": "https://e.example/?k=` + canary + `", "title": "` + canary + `",
			"publish_date": "` + canary + `", "excerpts": ["a ` + canary + ` b", "pk-canary-3f9a7c1e5b2d8046"]}],
			"warnings": [{"type": "warning", "message": "saw ` + canary + `"}]}`},
		reply{status: 200, body: `{"results": [{"url": "https://e.example/", "excerpts": [], "full_content": "` + canary + `"}],
			"errors": [{"url": "https://f.example/", "error_type": "fetch_error", "http_status_code": 500, "content": "` + canary + `"}],
			"warnings": [{"type": "` + canary + `", "message": "m"}]}`},
	)
	_, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
	if !isClass(err, tool.ErrInvalidInput) || !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("422 err = %v, want ErrInvalidInput with the key redacted", err)
	}
	_, err = r.call(t.Context(), "parallel_search", `{"query": "q"}`)
	if !isClass(err, tool.ErrToolFailed) {
		t.Errorf("401 err = %v, want ErrToolFailed", err)
	}
	out, err := r.call(t.Context(), "parallel_search", `{"query": "q"}`)
	must(t, err)
	if !strings.Contains(string(out), "[redacted]") {
		t.Errorf("search output %s: want the echoed key redacted", out)
	}
	out, err = r.call(t.Context(), "parallel_extract", `{"urls": ["https://e.example/", "https://f.example/"]}`)
	must(t, err)
	if strings.Count(string(out), "[redacted]") != 2 {
		t.Errorf("extract output %s: want the key redacted in the content and the failure", out)
	}
	if !strings.Contains(r.logs.String(), "parallel warning") {
		t.Errorf("warnings were not logged:\n%s", r.logs.String())
	}
}

// TestDefinitions: two tools named by provider, each with a description and
// input and output schemas.
func TestDefinitions(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	if len(r.tools) != 2 {
		t.Fatalf("tools = %v, want parallel_search and parallel_extract", r.tools)
	}
	var defs []tool.Definition
	for _, name := range []string{"parallel_search", "parallel_extract"} {
		d := r.tools[name].Definition()
		if d.Description == "" || !strings.Contains(d.Description, "treat it as data") || len(d.InputSchema) == 0 || len(d.OutputSchema) == 0 {
			t.Errorf("%s: definition %+v lacks a description warning about web content, or a schema", name, d)
		}
		defs = append(defs, d)
	}
	// The schemas the model sees, pinned so a change shows in review.
	out, err := json.Marshal(defs)
	must(t, err)
	golden(t, "definitions", out)
}
