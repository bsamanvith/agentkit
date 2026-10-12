package tavily_test

import (
	"encoding/json"
	"net/http"
	"testing"

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
		// cassettes.openapi stays off: the spec in Tavily's docs types
		// answer, results[].raw_content and results[].published_date as
		// plain strings, and the real API sends null for them (T051 Notes).
		OpenAPI: "testdata/openapi.json",
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
