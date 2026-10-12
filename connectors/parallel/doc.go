// Package parallel is the Parallel Web Systems connector: web search and page
// extraction through Parallel's REST API (POST /v1/search and /v1/extract on
// https://api.parallel.ai, key in the x-api-key header).
//
// [Factory] returns a [toolset.Factory] of type "parallel" with two tools:
//
//   - parallel_search: a natural-language objective plus keyword queries,
//     answered with LLM-ready excerpts per result;
//   - parallel_extract: the relevant excerpts, or the full text, of 1 to 10
//     pages.
//
// Both return the output shape shared by every search connector (E10), so an
// agent can switch providers without changing prompts:
//
//	search:  {"results": [{"title", "url", "published_date"?, "content"}]}
//	extract: {"results": [{"url", "title"?, "content"}], "failed": [{"url", "error"}]}
//
// The content is third-party web text: treat it as data, not instructions.
//
// # Settings
//
// All optional. Unknown fields are refused, and so is a literal "api_key":
// config files get committed, so the key comes from the environment variable
// named in api_key_env.
//
//	{
//	  "api_key_env": "PARALLEL_API_KEY",
//	  "base_url": "https://api.parallel.ai",
//	  "timeout": "30s",
//	  "max_content_chars": {"search": 4000, "extract": 20000},
//	  "search_defaults": {
//	    "mode": "fast",
//	    "allow_model_mode": false,
//	    "max_chars_per_result": 0,
//	    "location": "us",
//	    "fetch_policy": {"max_age_seconds": 86400, "timeout_seconds": 10, "disable_cache_fallback": false}
//	  },
//	  "extract_defaults": {"full_content": false, "fetch_policy": {}}
//	}
//
// The key is required: when the variable is unset or empty, Build fails naming
// it, so an optional toolset is skipped and reported. base_url must be https
// unless its host is loopback (a local replay server). timeout bounds each
// HTTP attempt. max_content_chars caps each result's content, in characters;
// longer content is cut at a character boundary and ends with a marker that
// says how much was cut and how to get more ("…[truncated 12,400 characters;
// call parallel_extract on this URL for more of the page]").
// search_defaults.mode is one of turbo, fast, basic and advanced; with
// allow_model_mode the model may pick it per call, otherwise the tool has no
// mode input. max_chars_per_result, location and fetch_policy are passed to
// Parallel as they are (0 or empty means Parallel's default).
// extract_defaults.full_content returns each page's full text instead of its
// excerpts.
//
// # Errors and retries
//
// A request Parallel rejects (400, 422) is [tool.ErrInvalidInput] with
// Parallel's message, at most 300 characters, so the model can fix its input.
// A rejected key (401, 403), a rate limit, a server error, a transport error
// or an unreadable response is [tool.ErrToolFailed]. The only retry is one on
// 429, after Retry-After (1 s if absent, by the injected clock) when that is
// at most 10 s; there is none on 5xx, since a paid call may have run.
// Response bodies are read through an 8 MiB limit, and the key is replaced by
// "[redacted]" in any text taken from a response.
package parallel
