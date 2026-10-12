package parallel_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// connector describes Parallel to the T049 test kit. Its calls replay the
// four cassettes, each with the settings its mapping case needs.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return parallel.Factory(parallel.WithBaseURL(e.BaseURL), parallel.WithHTTPClient(e.HTTPClient),
				parallel.WithLookupEnv(e.LookupEnv), parallel.WithClock(e.Clock), parallel.WithLogger(e.Logger))
		},
		KeyEnv:  "PARALLEL_API_KEY",
		OpenAPI: "testdata/openapi.json",
		Enable:  []toolsettest.CheckID{"schema_honesty.dead_fields", "classified"},
		Calls: []toolsettest.Call{
			{
				Name: "search_defaults", Tool: "parallel_search",
				Input:     json.RawMessage(`{"query": "latest Go release"}`),
				Cassettes: []string{"search-defaults"},
			},
			{
				Name: "search_filters", Tool: "parallel_search",
				Settings: json.RawMessage(`{"search_defaults": {"mode": "basic", "allow_model_mode": true, "max_chars_per_result": 1500,
					"location": "GB", "fetch_policy": {"max_age_seconds": 3600}}}`),
				Input: json.RawMessage(`{"query": "Go 1.27 release notes",
					"objective": "Find what changed in the Go 1.27 release, from official sources.",
					"extra_queries": ["Go 1.27 new features", "golang 1.27 changelog"], "max_results": 2,
					"include_domains": ["go.dev", "github.com/golang"], "after_date": "2026-01-01", "mode": "advanced"}`),
				Cassettes: []string{"search-filters"},
			},
			{
				Name: "extract_excerpts", Tool: "parallel_extract",
				Input:     json.RawMessage(`{"urls": ["https://go.dev/doc/go1.27", "https://example.com/missing-page"], "objective": "What changed in the go command?"}`),
				Cassettes: []string{"extract-excerpts"},
			},
			{
				Name: "extract_full", Tool: "parallel_extract",
				Settings:  json.RawMessage(`{"extract_defaults": {"full_content": true, "fetch_policy": {"max_age_seconds": 600, "timeout_seconds": 30, "disable_cache_fallback": true}}}`),
				Input:     json.RawMessage(`{"urls": ["https://go.dev/doc/go1.27", "https://example.com/timeout"]}`),
				Cassettes: []string{"extract-full"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "blank_query", Tool: "parallel_search", Input: json.RawMessage(`{"query": "  \t "}`)},
			{Name: "five_extra_queries", Tool: "parallel_search", Input: json.RawMessage(`{"query": "q", "extra_queries": ["a", "b", "c", "d", "e"]}`)},
			{Name: "max_results_21", Tool: "parallel_search", Input: json.RawMessage(`{"query": "q", "max_results": 21}`)},
			{Name: "bad_date", Tool: "parallel_search", Input: json.RawMessage(`{"query": "q", "after_date": "2026-13-01"}`)},
			{Name: "include_and_exclude", Tool: "parallel_search", Input: json.RawMessage(`{"query": "q", "include_domains": ["a.com"], "exclude_domains": ["b.com"]}`)},
			{Name: "ftp_url", Tool: "parallel_extract", Input: json.RawMessage(`{"urls": ["ftp://example.com/file"]}`)},
			{Name: "eleven_urls", Tool: "parallel_extract", Input: json.RawMessage(`{"urls": ` + jsonString(repeatURL(11)) + `}`)},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"ref_id": "ref_conformance", "message": message}})
			return b
		},
		// Each setting shows in the request body.
		SettingCalls: []toolsettest.Call{
			{
				Name: "search_mode_basic", Tool: "parallel_search", Input: json.RawMessage(`{"query": "latest Go release"}`),
				Settings: json.RawMessage(`{"search_defaults": {"mode": "basic"}}`),
			},
			{
				Name: "search_location", Tool: "parallel_search", Input: json.RawMessage(`{"query": "latest Go release"}`),
				Settings: json.RawMessage(`{"search_defaults": {"location": "GB", "max_chars_per_result": 1500}}`),
			},
			{
				Name: "extract_fetch_policy", Tool: "parallel_extract", Input: json.RawMessage(`{"urls": ["https://go.dev/doc/go1.27"]}`),
				Settings: json.RawMessage(`{"extract_defaults": {"fetch_policy": {"max_age_seconds": 600}}}`),
			},
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
		Truncation: &toolsettest.Truncation{
			Call: "search_defaults", Field: "/results/0/excerpts/0", Limit: 100,
			Settings: json.RawMessage(`{"max_content_chars": {"search": 100}}`),
		},
	}
}

// Criteria covered by the shared suite: settings, missing key, the error
// matrix, the retry rule, body limits, canary, no network, MCP and
// concurrency (T049). The tests in parallel_test.go pin what is Parallel's
// own: the request mapping, its messages, Retry-After timing, truncation.
func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponsesSearch(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "parallel_search")
}

func FuzzResponsesExtract(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "parallel_extract")
}
