// Package tavily is the toolset factory for Tavily (https://tavily.com): web
// search and page extraction for agents, over Tavily's REST API with plain
// net/http.
//
// It works without an account: in keyless mode every request carries
// "X-Tavily-Access-Mode: keyless" (free, rate-limited, same responses). With
// a key it sends "Authorization: Bearer <key>" instead. Every request carries
// exactly one of the two.
//
// # Tools
//
// tavily_search (POST /search) takes:
//
//	{"query": "...",                  required, at most 2,000 characters
//	 "max_results": 5,                1..20, default 5
//	 "topic": "general",              general | news | finance
//	 "time_range": "week",            day | week | month | year
//	 "include_domains": ["go.dev"],   at most 50 each
//	 "exclude_domains": ["..."],
//	 "search_depth": "basic"}         basic | advanced | fast | ultra-fast; only
//	                                  when settings let the model choose
//
// and returns
//
//	{"results": [{"title", "url", "published_date"?, "score"?, "content"}], "answer"?}
//
// tavily_extract (POST /extract) takes {"urls": [...], "query"?}: 1 to 10
// absolute http or https URLs, and an optional query that focuses the
// extraction on the parts of each page relevant to it. It returns
//
//	{"results": [{"url", "title"?, "content"}], "failed": [{"url", "error"}]}
//
// where failed lists the URLs Tavily could not extract; the others are still
// returned. These are the output shapes shared by every search connector, so
// an agent can switch providers without changing its prompts.
//
// Results are third-party web text and can contain prompt injection. The tool
// descriptions tell the model to treat them as data, not instructions; nothing
// is filtered.
//
// # Settings
//
// All optional:
//
//	{"auth": "auto",                     auto | key | keyless
//	 "api_key_env": "TAVILY_API_KEY",    the environment variable holding the key
//	 "base_url": "https://api.tavily.com",
//	 "timeout": "30s",                   per request; default 30s, 60s for advanced extraction
//	 "max_content_chars": {"search": 4000, "extract": 20000},
//	 "search_defaults": {"search_depth": "basic", "topic": "general",
//	                     "chunks_per_source": 3, "include_answer": false,
//	                     "model_chooses_depth": false},
//	 "extract_defaults": {"extract_depth": "basic", "format": "markdown"}}
//
// auto uses the key when the variable is set and not empty, else keyless;
// key requires it, so Build fails without it; keyless never sends it. A
// literal "api_key" is refused: config files get committed. A topic given by
// the model overrides the default one. With model_chooses_depth the search
// tool takes search_depth; otherwise the depth is fixed by the settings and
// the input has no such field.
//
// base_url overrides [WithBaseURL] for the toolset, so a config can point it
// at a local replay server. It must be https, or http to localhost or a
// loopback IP: the key goes wherever it points. Build sends no request.
//
// # Errors
//
// Input that fails validation, and a 400 or 422 from Tavily, give
// [tool.ErrInvalidInput]; Tavily's own message is kept, at most 300
// characters, so the model can fix its input. Everything else gives
// [tool.ErrToolFailed]: 401 and 403 ("tavily rejected the API key"), 429
// ("rate limited", with a hint to set the key in keyless mode), other
// statuses with the status only. A 429 is retried once, after its
// Retry-After (whole seconds or an HTTP date; 1 s when absent; no retry
// above 10 s or past the context's deadline). Nothing else is retried: a
// paid call may have run.
//
// The key never leaves this package except in the Authorization header:
// text taken from a response has it replaced with "[redacted]", and logs
// name the mode and the variable, never the value. Response bodies are read
// up to 8 MB; each content is cut at max_content_chars (at a rune boundary,
// marked "…[truncated]"), and titles, URLs and messages have caps of their
// own, so a tool's output is bounded whatever Tavily sends.
package tavily
