package tavily_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// connector describes Tavily to the T049 test kit. Its calls replay the
// cassettes recorded with default settings.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return tavily.Factory(tavily.WithBaseURL(e.BaseURL), tavily.WithHTTPClient(e.HTTPClient),
				tavily.WithLookupEnv(e.LookupEnv), tavily.WithClock(e.Clock), tavily.WithLogger(e.Logger))
		},
		KeyEnv:        "TAVILY_API_KEY",
		KeyOptional:   true, // auth auto: keyless without the key
		KeylessMarker: http.Header{"X-Tavily-Access-Mode": {"keyless"}},
		// Tavily's docs type answer, results[].raw_content and
		// results[].published_date as plain strings, and the real API sends
		// null for them: make openapi-specs corrects the cut with
		// testdata/openapi-overlay.json.
		OpenAPI: "testdata/openapi.json",
		Enable:  []toolsettest.CheckID{"classified"},
		Calls: []toolsettest.Call{
			{
				Name: "search_go_release", Tool: "tavily_search",
				Input:     json.RawMessage(`{"query": "latest Go programming language release", "time_range": "month"}`),
				Cassettes: []string{"search-go-release"},
			},
			{
				Name: "extract_partial_failure", Tool: "tavily_extract",
				Input:     json.RawMessage(`{"urls": ["https://go.dev/doc/devel/release", "https://this-domain-does-not-exist-agentkit.invalid/page"]}`),
				Cassettes: []string{"extract-partial-failure"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "blank_query", Tool: "tavily_search", Input: json.RawMessage(`{"query": "   "}`)},
			{Name: "domain_with_a_path", Tool: "tavily_search", Input: json.RawMessage(`{"query": "go", "include_domains": ["https://go.dev/doc"]}`)},
			{Name: "ftp_url", Tool: "tavily_extract", Input: json.RawMessage(`{"urls": ["ftp://example.com/file"]}`)},
			{Name: "url_with_credentials", Tool: "tavily_extract", Input: json.RawMessage(`{"urls": ["https://user:pass@example.com/"]}`)},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"detail": map[string]string{"error": message}})
			return b
		},
		// Each setting shows in the request body.
		SettingCalls: []toolsettest.Call{
			{
				Name: "search_depth_advanced", Tool: "tavily_search", Input: json.RawMessage(`{"query": "latest Go programming language release"}`),
				Settings: json.RawMessage(`{"search_defaults": {"search_depth": "advanced"}}`),
			},
			{
				Name: "search_topic_news", Tool: "tavily_search", Input: json.RawMessage(`{"query": "latest Go programming language release"}`),
				Settings: json.RawMessage(`{"search_defaults": {"topic": "news", "include_answer": true}}`),
			},
			{
				Name: "extract_depth_advanced", Tool: "tavily_extract", Input: json.RawMessage(`{"urls": ["https://go.dev/doc/devel/release"]}`),
				Settings: json.RawMessage(`{"extract_defaults": {"extract_depth": "advanced", "format": "text"}}`),
			},
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
		Truncation: &toolsettest.Truncation{
			Call: "search_go_release", Field: "/results/0/content", Limit: 100,
			Settings: json.RawMessage(`{"max_content_chars": {"search": 100}}`),
		},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "tavily_search")
}

func FuzzResponsesExtract(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "tavily_extract")
}
