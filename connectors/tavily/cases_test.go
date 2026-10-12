package tavily_test

import "net/http"

// recordedCase is a tool call recorded from the real API as a cassette
// (TestLiveRecord, -tags live, AGENTKIT_RECORD_CASSETTES=1):
// testdata/cassettes/<name>.req.json and <name>.res.http. The unit tests
// replay it, check the request sent equals the recorded one, and pin the
// output (testdata/golden/<name>.output.json or .error.txt).
type recordedCase struct {
	name     string
	tool     string
	settings string
	keyed    bool // send a key; the recording used a made-up one
	input    string
	status   int // the status Tavily answered with
}

var recordedCases = []recordedCase{
	{
		name:  "search-go-release",
		tool:  "tavily_search",
		input: `{"query": "latest Go programming language release", "time_range": "month"}`,
		// Defaults: max_results 5, search_depth basic, include_published_date.
		status: http.StatusOK,
	},
	{
		name: "search-news-answer",
		tool: "tavily_search",
		settings: `{"search_defaults": {"include_answer": true, "model_chooses_depth": true, "chunks_per_source": 2},
			"max_content_chars": {"search": 300}}`,
		input: `{"query": "Go 1.27 release", "topic": "news", "max_results": 3, "search_depth": "advanced",
			"exclude_domains": ["reddit.com"]}`,
		status: http.StatusOK,
	},
	{
		name:   "extract-partial-failure",
		tool:   "tavily_extract",
		input:  `{"urls": ["https://go.dev/doc/devel/release", "https://this-domain-does-not-exist-agentkit.invalid/page"]}`,
		status: http.StatusOK,
	},
	{
		name:     "extract-query-text",
		tool:     "tavily_extract",
		settings: `{"extract_defaults": {"format": "text"}}`,
		input:    `{"urls": ["https://www.tavily.com"], "query": "pricing and free credits"}`,
		status:   http.StatusOK,
	},
	{
		name:   "search-query-too-short",
		tool:   "tavily_search",
		input:  `{"query": "x"}`,
		status: http.StatusBadRequest,
	},
	{
		name:     "search-bad-key",
		tool:     "tavily_search",
		settings: `{"auth": "key"}`,
		keyed:    true,
		input:    `{"query": "go release"}`,
		status:   http.StatusUnauthorized,
	},
}
