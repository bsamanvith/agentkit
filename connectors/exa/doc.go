// Package exa is the Exa connector: web search and page extraction through
// Exa's API (https://exa.ai), as a [toolset.Factory] of type "exa".
//
// It provides two tools:
//
//   - exa_search: POST /search. Results carry the page's highlights (the
//     default) or its text, as chosen in settings.
//   - exa_extract: POST /contents for 1 to 10 known URLs. Per-URL failures
//     are returned in "failed" next to the pages that worked.
//
// Both return the output shape shared by every search connector (E10), so an
// agent can switch providers without changing prompts:
//
//	search:  {"results": [{"title", "url", "published_date"?, "author"?, "content"}], "answer"?}
//	extract: {"results": [{"url", "title"?, "content"}], "failed": [{"url", "error"}]}
//
// Content comes from third-party web pages and may contain prompt injection;
// the tool descriptions tell the model to treat it as data.
//
// Exa needs an API key, sent in the x-api-key header. Settings never hold the
// key: they name the environment variable it is read from (default
// EXA_API_KEY), looked up through [WithLookupEnv] when the toolset is built.
// A missing variable fails the build, so an optional toolset is skipped.
//
// Settings (all optional):
//
//	{
//	  "api_key_env": "EXA_API_KEY",
//	  "base_url": "https://api.exa.ai",
//	  "timeout": "30s",
//	  "max_content_chars": {"search": 4000, "extract": 20000},
//	  "search_defaults": {
//	    "type": "auto",
//	    "model_chooses_type": false,
//	    "contents": "highlights",
//	    "max_characters": 0,
//	    "category": ""
//	  },
//	  "extract_defaults": {"max_characters": 20000, "max_age_hours": null}
//	}
//
// base_url points the toolset at another server, such as a local replay of
// recorded responses; it must be https, or http to a loopback host.
// max_content_chars caps each result's content in characters; a cut content
// ends with a marker that says how much was cut and how to get more, for
// example "…[truncated 12,400 characters; call exa_extract on this URL for
// the full page]". Building the toolset makes no request.
//
// search_defaults.type is one of instant, fast, auto, deep-lite, deep and
// deep-reasoning; with model_chooses_type the model may pick another per
// call. contents is "highlights" or "text"; max_characters, if set, is sent
// as that mode's maxCharacters (text mode defaults to the search content
// cap). category is one of Exa's categories: company, publication, news,
// personal site, financial report, people. extract_defaults.max_age_hours is
// Exa's freshness control (-1 always uses the cache, 0 always fetches,
// otherwise the oldest cached copy accepted, up to 720).
//
// Errors follow E10: a 400 or 422 is [tool.ErrInvalidInput] with Exa's own
// message, a rejected key, a rate limit (after one retry honoring
// Retry-After up to 10 s), a 5xx or a transport error is
// [tool.ErrToolFailed]. The key is replaced with "[redacted]" in every string
// taken from a response.
package exa
