package exa_test

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// connector describes Exa to the T049 test kit. Its calls replay the
// cassettes of the cases that use default settings (searchCases,
// extractCases).
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return exa.Factory(exa.WithBaseURL(e.BaseURL), exa.WithHTTPClient(e.HTTPClient),
				exa.WithLookupEnv(e.LookupEnv), exa.WithClock(e.Clock), exa.WithLogger(e.Logger))
		},
		KeyEnv: "EXA_API_KEY",
		Calls: []toolsettest.Call{
			{
				Name: "search_defaults", Tool: "exa_search",
				Input:     json.RawMessage(searchCases["search-defaults"].input),
				Cassettes: []string{"search-defaults"},
			},
			{
				Name: "contents_query", Tool: "exa_extract",
				Input:     json.RawMessage(extractCases["contents-query"].input),
				Cassettes: []string{"contents-query"},
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
	}
}

// TestConformance is skipped until toolsettest accepts "synthetic" in a
// cassette's .req.json (T049 specifies it; the first cut's CassetteRequest
// rejects it as an unknown field, and every Exa cassette is synthetic). Then
// remove the skip, add the FuzzResponses targets for both tools, and delete the
// hand-written tests the suite replaces (T047 Notes, "Toward T049").
func TestConformance(t *testing.T) {
	t.Parallel()
	t.Skip(`blocked on toolsettest: cassettes marked "synthetic" fail to load (T047 Notes)`)
	toolsettest.RunConnector(t, connector())
}
