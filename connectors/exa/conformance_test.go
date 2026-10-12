package exa_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// connector describes Exa to the T049 test kit. Its calls replay cassettes
// of the mapping cases (searchCases, extractCases). The kit takes one
// configuration, so Settings is the union of the three cases' settings, which
// do not interact: model_chooses_type adds the type input and leaves a call
// without one unchanged, and extract_defaults touch only exa_extract. Text
// mode and the highlights cap change the search request, so they stay in
// TestSearchRequestAndMapping.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return exa.Factory(exa.WithBaseURL(e.BaseURL), exa.WithHTTPClient(e.HTTPClient),
				exa.WithLookupEnv(e.LookupEnv), exa.WithClock(e.Clock), exa.WithLogger(e.Logger))
		},
		Settings: json.RawMessage(`{"search_defaults": {"model_chooses_type": true},
			"extract_defaults": {"max_characters": 5000, "max_age_hours": 24}}`),
		KeyEnv:  "EXA_API_KEY",
		OpenAPI: "testdata/openapi.json",
		Enable: []toolsettest.CheckID{
			"schema_honesty.dead_fields", "cassettes.openapi", "settings.honored", "errors.no_retry_5xx", "errors.retry_wait_ctx",
			"errors.attempt_timeout", "limits.rune_boundary",
		},
		Calls: []toolsettest.Call{
			{
				Name: "search_defaults", Tool: "exa_search",
				Input:     json.RawMessage(searchCases["search-defaults"].input),
				Cassettes: []string{"search-defaults"},
			},
			{
				Name: "search_deep", Tool: "exa_search",
				Input:     json.RawMessage(searchCases["search-deep"].input),
				Cassettes: []string{"search-deep"},
			},
			{
				Name: "contents_partial", Tool: "exa_extract",
				Input:     json.RawMessage(extractCases["contents-partial"].input),
				Cassettes: []string{"contents-partial"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "blank_query", Tool: "exa_search", Input: json.RawMessage(`{"query": "   "}`)},
			{Name: "max_results_over_100", Tool: "exa_search", Input: json.RawMessage(`{"query": "go", "max_results": 101}`)},
			{Name: "bad_date", Tool: "exa_search", Input: json.RawMessage(`{"query": "go", "start_published_date": "yesterday"}`)},
			{Name: "ftp_url", Tool: "exa_extract", Input: json.RawMessage(`{"urls": ["ftp://example.com/file"]}`)},
			{Name: "eleven_urls", Tool: "exa_extract", Input: json.RawMessage(`{"urls": ["https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5", "https://a.example/6", "https://a.example/7", "https://a.example/8", "https://a.example/9", "https://a.example/10", "https://a.example/11"]}`)},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]string{"error": message})
			return b
		},
		// Each setting shows in the request body. Settings merge field by
		// field, so each keeps model_chooses_type from Settings.
		SettingCalls: []toolsettest.Call{
			{
				Name: "search_type_fast", Tool: "exa_search", Input: json.RawMessage(searchCases["search-defaults"].input),
				Settings: json.RawMessage(`{"search_defaults": {"model_chooses_type": true, "type": "fast"}}`),
			},
			{
				Name: "search_contents_text", Tool: "exa_search", Input: json.RawMessage(searchCases["search-defaults"].input),
				Settings: json.RawMessage(`{"search_defaults": {"model_chooses_type": true, "contents": "text", "category": "news"}}`),
			},
			{
				Name: "extract_max_age", Tool: "exa_extract", Input: json.RawMessage(extractCases["contents-partial"].input),
				Settings: json.RawMessage(`{"extract_defaults": {"max_characters": 5000, "max_age_hours": 1}}`),
			},
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
		Truncation: &toolsettest.Truncation{
			Call: "search_defaults", Field: "/results/0/highlights/0", Limit: 100,
			Settings: json.RawMessage(`{"max_content_chars": {"search": 100}}`),
		},
	}
}

// Criteria covered by the shared suite: settings, missing key, the error
// matrix, the retry rule, body limits, canary, no network, MCP, concurrency
// and schema honesty (T049).
func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "exa_search")
}

func FuzzResponsesExtract(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "exa_extract")
}
