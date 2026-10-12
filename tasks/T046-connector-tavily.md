---
id: T046
title: "connectors/tavily: search and extract, keyless or keyed"
epic: E10
status: review
depends_on: [T026, T036]
---

# T046: `connectors/tavily`

## Context

First real connector, wanted by the owner. Tavily's search and extract work without an account (keyless, rate-limited) or with an API key. Decisions, shared output shape, limits and error rules: [E10](E10-search-connectors.md).

API reference:
- `https://docs.tavily.com/documentation/api-reference/endpoint/search.md`
- `https://docs.tavily.com/documentation/api-reference/endpoint/extract.md`
- keyless mode: `https://docs.tavily.com/documentation/keyless.md`

Fetch them with `curl` (in Claude sessions, WebFetch may fail on DNS, but `curl` through the proxy works).

## Scope

- **Factory:** `tavily.Factory(opts...) toolset.Factory` with `Type() == "tavily"`.
  - Options: `WithHTTPClient`, `WithBaseURL` (tests), `WithLookupEnv`, `WithClock`, `WithLogger`.
- **Settings:**
  - `auth`: `auto` | `key` | `keyless`;
  - `api_key_env`, default `TAVILY_API_KEY`;
  - `timeout`, default 30 s; extract `advanced` 60 s;
  - `max_content_chars`;
  - `search_defaults`: `search_depth` (default `basic`), `topic`, `chunks_per_source`;
  - `extract_defaults`: `extract_depth` (default `basic`), `format` (default `markdown`).
  - Unknown fields are refused, `api_key` included.
- **`tavily_search`:**
  - inputs: `query`, `max_results` (1–20, default 5), `topic` (`general`|`news`|`finance`), `time_range` (`day`|`week`|`month`|`year`), `include_domains`, `exclude_domains`, `search_depth` (`basic`|`advanced`|`fast`|`ultra-fast`, only if settings allow the model to choose; else fixed);
  - POST `/search`, mapped to E10's search shape (`content` from `results[].content`; `answer` only if settings turn on `include_answer`).
- **`tavily_extract`:**
  - inputs: `urls` (1–10), optional `query`;
  - POST `/extract`; `results[].raw_content` becomes `content`, and `failed_results` becomes `failed`.
- **Auth headers:** exactly one of `Authorization: Bearer <key>` or `X-Tavily-Access-Mode: keyless` on every request.
- **Wiring:**
  - add the factory to `cmd/agentkit/catalog.go`;
  - add `deploy/local/agent/search.json` (demo + `tavily` in `auto` mode, `optional: true`, the agent using both; system prompt says to cite URLs);
  - add a QUICKSTART section: `AGENTKIT_CONFIG=deploy/local/agent/search.json scripts/try-agent.sh`, and that it works without a Tavily key.

## Out of scope

- `crawl`, `map`, `research` (key-only endpoints; add when asked). Per-tenant keys (T044).

## Acceptance criteria

**Test kit (added 2026-10-12, [T049](T049-connector-test-kit.md)).**
- `toolsettest.RunConnector` runs the generic checks: settings, missing key, the error matrix (400/401/429/5xx/garbage/oversized/hang), the retry rule, the canary, no network, MCP and concurrency. Criteria below that it covers are ticked by "passes `RunConnector`"; don't hand-write those tests.
- Write cassettes in T049's format.
- Accept a `base_url` setting (`https` unless loopback).
- Make no request in `Build`.
- Add the three-line `FuzzResponses` target.
- If T049 is not on `main` yet, build the connector and its specific tests first, then adopt the suite when it lands.

Status of this block (T049 is not on `main` yet, so the connector ships with its own generic tests; see Notes, "Adopting T049"):
- [ ] passes `toolsettest.RunConnector` — waits for T049
- [x] cassettes in T049's format, recorded from the real API (none synthetic) — `testdata/cassettes/*.req.json` + `*.res.http`, written by `TestLiveRecord` with `AGENTKIT_RECORD_CASSETTES=1`; replayed and matched by `TestRecordedCases`
- [x] `base_url` setting, `https` unless localhost or a loopback IP — `TestBaseURLSetting`, `TestSettingsRejected/base_url_*`
- [x] no request in `Build` — `TestAuthModes` (zero upstream requests after `Build`, every mode); `TestE2EServeTavilyOptional` starts the server offline
- [ ] the three-line `FuzzResponses` target — waits for T049 (`FuzzDecodeSearch` and `FuzzDecodeExtract` until then)


- [x] **Search request:** sends the documented JSON (golden request body) with settings defaults applied and model inputs validated; the response (recorded fixture) maps to E10's search shape exactly (golden output).
  - `TestRecordedCases/search-go-release` and `/search-news-answer` (the request sent must JSON-equal the recorded cassette request, defaults `max_results` 5, `search_depth` basic and `include_published_date` applied; output pinned by `testdata/golden/*.output.json`); `TestInvalidInput` (validation); `TestDefinitions` (the schemas the model sees, golden)
- [x] **Extract** maps `results` and `failed_results`, with partial failures kept (golden).
  - `TestRecordedCases/extract-partial-failure` (a real page plus an `.invalid` domain: one result, one `failed`), `/extract-query-text` (`format` text, `query`); `TestUpstreamErrors/400_extract,_recorded` (an extract 400 listing failed URLs)
- [x] **Auth modes** (table test):
  - `auto` with the variable set sends the bearer header; `auto` without it sends the keyless header;
  - `key` without the variable fails `Build` with a message naming the variable;
  - `keyless` never sends `Authorization`, even when the variable is set;
  - never both headers.
  - `TestAuthModes` (6 cases: auto with/without/blank key, another variable, key, keyless with the key set); `TestSettingsRejected/key_mode_without_the_variable` (names `MY_TAVILY_KEY`); and after every test `rig.check` checks each request had exactly one auth mode, the right one per `expectedKey` (an independent restatement of the rule)
- [x] **Settings validation:** a literal `api_key` in settings, unknown fields and bad enums each fail `Build` with a clear message; an `optional` toolset that fails is skipped and shows in `/readyz` as degraded (existing mechanism).
  - `TestSettingsRejected` (25 cases, each message names the field or variable and never holds the canary); `TestOptionalToolsetSkipped`; `cmd/agentkit.TestE2EServeTavilyOptional` (`/readyz` degraded naming `tavily-keyed` and its variable, while `/mcp/tavily` serves); `cmd/agentkit.TestSearchConfigBuilds`
- [x] **Input validation:** an empty query, `max_results` out of range, an unsupported enum, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent.
  - `TestInvalidInput` (25 cases, each `ErrInvalidInput` and not `ErrToolFailed`, zero upstream requests)
- [x] **Errors:**
  - 400 gives `ErrInvalidInput` with Tavily's message (capped);
  - 401 gives "rejected the API key";
  - 429 in keyless mode retries once after `Retry-After` (fake clock), then fails with the "set TAVILY_API_KEY" hint;
  - 500 gives the status only, with no retry;
  - a cancelled `ctx` stops a retry wait.
  - `TestUpstreamErrors` (400/422/extract 400 recorded, 400 without body, 401 recorded, 403, 500, 502 HTML, 432, 200 not JSON, 200 wrong types, "400 message capped", transport error, timeout; each error class checked with `errors.Is`, and exactly one request); `TestRateLimit` (fake clock: keyless and keyed limited twice, then ok, no or unparseable `Retry-After`; over 10 s no retry; deadline before the wait ends no retry; cancel during the wait gives `context.Canceled`)
- [x] **Limits:**
  - content over `max_content_chars` is cut at a rune boundary with the marker;
  - a response over the body limit fails cleanly;
  - total output stays bounded.
  - `TestContentTruncated` (ASCII, 2- and 4-byte runes, at the limit, custom limit, plus answer, extract content and title markers); `TestBodyLimit` (over 8 MB at 200 and 400, exactly 8 MB passes); `TestOutputBounded` (hostile bodies stay under a computed byte bound with the requested item counts)
- [x] **Canary:** a key value appears in no tool output, error, log line (debug included) or MCP response, including when the upstream echoes it in an error body.
  - `rig.check` after every connector test (outputs, errors, debug logs, request bodies); `TestKeyRedacted` (key echoed in 400, 422, extract 400, search and extract results, and JSON-escaped); `FuzzDecodeSearch`/`FuzzDecodeExtract` (canary in every fuzzed call); `cmd/agentkit.TestMCPTavily` (every MCP response body, with the upstream echoing the key)
- [x] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeExtract` (hostile upstream bodies) never panic and always return the E10 shape or an error.
  - `FuzzDecodeSearch`, `FuzzDecodeExtract`: no panic, error is `ErrInvalidInput` or `ErrToolFailed`, output passes the strict E10 shape and the declared output schema, stays under the byte bound, holds no key; 60 s each in `make fuzz FUZZTIME=60s`, clean
- [x] **Over MCP:** `/mcp/tavily` lists exactly `tavily_search` and `tavily_extract` with input and output schemas, and a call through the MCP client against the fake upstream returns the mapped result.
  - `cmd/agentkit.TestMCPTavily` (the binary's MCP server and the SDK client: exactly the two tools, both schemas, a search whose structured content equals `search-go-release.output.json`, two failing calls as tool errors, no key in any response)
- [ ] **Live (`-tags live`), keyless:**
  - a search for a current topic returns at least one result with a URL;
  - extracting `https://www.tavily.com` returns content;
  - with `TAVILY_API_KEY` set, the same works keyed.
  - not ticked: keyless passed (`TestLiveKeyless`, `TestLiveRecord`, 2026-10-12, below); keyed not run, `TAVILY_API_KEY` is not set in this session
- [ ] **Live end to end:** `agentkit serve` with `search.json` answers "What is the latest Go release? Cite the source." with a `tool_call` to `tavily_search` and a URL in the answer, against real Claude (`ANTHROPIC_KEY`).
  - not run: this session has no Anthropic key (`ANTHROPIC_KEY` and `ANTHROPIC_API_KEY` unset, no `ant`), so `cmd/agentkit.TestLiveServeSearch` skips. Substitute check, below: `agentkit serve` with `search.json`, keyless, `tools/call tavily_search` over `/mcp/tavily` returned real results
- [x] **Mutation list** recorded (at least: keyless header sent with a key, `failed_results` dropped, truncation splitting a rune, retry on 500, key not redacted from an echoed error, `max_results` default ignored).
  - below; every plant caught by a named test

## Notes

Status is `review`, not `done`. Three things are still open: adopting `RunConnector` and `FuzzResponses` when T049 lands; the keyed live run (needs `TAVILY_API_KEY`); and the live end to end with real Claude, which needs an Anthropic key this session did not have. Everything else is in place and on `main`.

### Tools

| Tool | Input | Output |
|---|---|---|
| `tavily_search` | `query` (1–2000 chars, required); `max_results` (1–20, default 5); `topic` (`general`, `news`, `finance`); `time_range` (`day`, `week`, `month`, `year`); `include_domains`, `exclude_domains` (each at most 50 bare domains); `search_depth` only with `model_chooses_depth` | `{"results": [{"title", "url", "published_date"?, "author"?, "score"?, "content"}], "answer"?}` |
| `tavily_extract` | `urls` (1–10 absolute http/https URLs, no user info, at most 2048 chars); optional `query` | `{"results": [{"url", "title"?, "content"}], "failed": [{"url", "error"}]}` |

Unknown input fields are refused. The exact schemas are pinned in `testdata/golden/definitions*.json`. `results` and `failed` are always arrays, never null. The schema still says `["null","array"]` because `tool.Func` derives it from Go slices.

### Decisions

- **Settings:** `auth`, `api_key_env`, `base_url`, `timeout`, `max_content_chars` (`{"search", "extract"}`, each 100..200000), `search_defaults` (`search_depth`, `topic`, `chunks_per_source` 1..3, `include_answer`, `model_chooses_depth`) and `extract_defaults` (`extract_depth`, `format`).
  - The `search_depth` input exists only when `model_chooses_depth` is on. That makes a second input type, so a model that sends it otherwise is told the field is unknown.
- **Keys:**
  - A variable that is set but blank counts as unset.
  - A literal `api_key`, `apikey` or `key` field is refused with a message that points at `api_key_env` and does not echo the value.
  - An `api_key_env` that does not look like a variable name is also refused without being echoed.
- **`base_url`:**
  - The setting wins over `WithBaseURL`, which wins over the default.
  - It must be https, or http to `localhost` or a loopback IP (no DNS lookup). User info, query and fragment are refused, and the value is never echoed. A trailing `/` is dropped.
  - The key goes wherever `base_url` points, hence the https rule.
- **Request:** `max_results` and `include_published_date: true` are always sent. `include_answer` is sent only when on. `chunks_per_source` is dropped for `ultra-fast`.
- **Output:**
  - `published_date` is normalised to RFC 3339 UTC when it parses (Tavily sends RFC 1123, e.g. `Thu, 24 Sep 2026 00:00:00 GMT`); otherwise the cleaned text is kept.
  - Extract results carry `title`: Tavily returns it, though the docs do not list it.
  - `answer` appears only when `include_answer` is on, even if Tavily sends one.
  - Results are capped at the requested count. Failures are capped at the number of URLs.
- **Truncation** follows E10 as updated on 2026-10-12, which replaces the bare `…[truncated]` in the original brief:
  - A cut `content` ends `…[truncated 12,400 characters; call tavily_extract on this URL for the full page]`.
  - An extracted page ends `…; call tavily_extract again with a query to get the parts most relevant to it]`.
  - An answer ends `…; search again with a narrower query]`.
  - Short fields (title 300, url 2048, date 64, messages 300) keep `…[truncated]`.
  - Every cut is at a rune boundary.
- **Errors** (lowercase, revive `error-strings`):
  - 400 and 422 give `tavily rejected the request (status N): <message>` as `ErrInvalidInput`.
  - 401 and 403 give `tavily rejected the API key (status N)`.
  - 429 gives `tavily rate limited the request`, plus `; set TAVILY_API_KEY for higher limits` when keyless.
  - Anything else gives `tavily returned status N`.
  - A timeout gives `tavily did not answer within 30s`; no connection gives `could not reach tavily`.
  - The message is parsed from `{"detail": {"error"}}`, from 422's `{"detail": [{"loc", "msg"}]}` as `body.query: msg`, from `{"detail": "..."}`, and from an extract 400's `detail.failed_results` (up to 3).
- **Retry:**
  - Only a 429 is retried, once. `Retry-After` is read as whole seconds, defaulting to 1 s when it is missing or unparseable.
  - A wait over 10 s fails at once, and so does a wait that would end past the `ctx` deadline.
  - The wait is on the injected clock and stops when `ctx` is cancelled.
- **`tool.Func` wraps every function error as `ErrToolFailed`.**
  - A small wrapper (`checkedTool`) turns the package's input errors into `ErrInvalidInput`.
  - The same wrapper adds `enum`, `minimum`, `maximum`, `default` and length limits to the input schema.
  - Follow-up: let `tool.Func` recognise `ErrInvalidInput` from the function, so connectors need no wrapper.
- **Startup log:** `tavily toolset auth` at info, with `mode` (`key` or `keyless`) and `api_key_env`. `newCatalog` takes no logger, so `cmd/agentkit/catalog.go` gives the factory a JSON logger on stderr.
  - Follow-up, outside this task's files: pass serve's logger through `buildToolsets` → `newCatalog`.
- **Exposure:** `/mcp/` has no auth until T040. QUICKSTART says not to put a keyed toolset on a public address before then. `search.json` is for loopback.

### Fixtures

- **Cassettes** in `testdata/cassettes/`, all recorded from the real API on 2026-10-12, keyless; none are synthetic.
  - They are `search-go-release`, `search-news-answer`, `extract-partial-failure`, `extract-query-text`, `search-query-too-short` (400) and `search-bad-key` (401, sent with a made-up key).
  - `TestLiveRecord` writes them with `AGENTKIT_RECORD_CASSETTES=1`. Request headers are dropped and only `Content-Type` and `Retry-After` are kept; `request_id` is zeroed.
  - A recording is refused if the key is in either body, or if the mapped output fails the output schema.
  - The cassette request is the golden request: `TestRecordedCases` requires the sent request to be JSON-equal to it. The old `golden/*.request.json` files are gone. A change to the request shape therefore means re-recording, not `make golden`.
- **`testdata/responses/`** holds responses with no exchange the tool could make. They are used by the error tests and as fuzz seeds.
  - `raw-search-422-query-not-a-string` and `raw-extract-400-all-urls-invalid` were captured with curl from the real API.
  - `docs-429` is Tavily's documentation example and was never recorded. T049's error matrix scripts its own 429.

### Live results (2026-10-12)

- **`TestLiveKeyless`**, keyless:
  - `tavily_search {"query": "latest Go programming language release", "time_range": "month"}` returned 5 results. They were endoflife.date/go, igmguru.com (what is Golang), Wikipedia's V language page, a Facebook post and simplilearn.com, all with URLs.
  - `tavily_extract {"urls": ["https://www.tavily.com"]}` returned the page (title "Tavily", about 11 KB of markdown).
- **`TestLiveRecord`**: all 6 cases got their recorded status (200 ×4, 400 "Query is too short. Min query length is 2 characters.", 401 for the made-up key). The re-recorded cassettes replay to the same goldens.
- **`TestLiveKeyed`**: skipped, `TAVILY_API_KEY` not set.
- **`cmd/agentkit.TestLiveServeSearch`**: skipped, no Anthropic key in this session.
  - It starts `agentkit serve` with `search.json`, keyless, forwards the proxy variables and asks for the latest stable Go release with a cited URL.
  - It expects a successful `tavily_search` call, a finished run with a URL in the answer, and a `"mode":"keyless"` log line.
- **Manual substitute** for `try-agent.sh --demo`, which needs an Anthropic key:
  - The built binary ran `serve -config deploy/local/agent/search.json` with `AGENTKIT_COMPONENTS=mcp-server` and no `TAVILY_API_KEY`.
  - `/readyz` was ready, and the log said `"mode":"keyless"`.
  - An MCP `tools/call tavily_search {"query": "latest Go release", "max_results": 3}` on `/mcp/tavily` returned go.dev/doc/go1.26 ("Go 1.26 Release Notes", score 0.90) and the Go 1.25 notes.

### Mutations

Each plant was made in a copy of the tree, run with `go test -count=1 -timeout=150s ./connectors/tavily/`, then restored. All 19 were caught by a failing test (no compile errors counted, no survivors). An earlier pass had one survivor (an `answer` passed on when not asked for); `TestAnswerOnlyWhenAsked` was added for it. Two plants needed fixes to the tests themselves:

- **Retry on 500:** at first it hung until the test timeout. `rig.call` now gives each call a real 15 s deadline, so the test fails by name instead.
- **`max_results` default ignored:** at first it was caught only by an index panic. The truncation tests now check the result count first.

| Plant | Caught by |
|---|---|
| Keyless header sent with a key (both headers on keyed requests) | `TestAuthModes` (3 keyed cases), `rig.check` in every keyed test |
| `failed_results` dropped | `TestRecordedCases/extract-partial-failure`, `TestOutputBounded/extract` |
| Truncation splitting a rune (cut by bytes) | `TestContentTruncated` (2-byte, 4-byte, custom limit), `TestRecordedCases/search-news-answer` |
| Retry on 500 (and other 5xx) | `TestUpstreamErrors/500`, `/502_html` (the retry waits on the fake clock; the call's 15 s deadline ends it) |
| Key not redacted from text taken from a response | `TestKeyRedacted`, `TestOutputBounded`, `FuzzDecodeSearch`/`FuzzDecodeExtract` seeds |
| `max_results` default ignored (0 sent) | `TestRecordedCases` (request differs from the cassette), `TestContentTruncated`, `ExampleFactory` |
| Results not capped at `max_results` | `TestOutputBounded/search` |
| Content marker without how to get more | `TestContentTruncated`, `TestRecordedCases` |
| Content marker counts the whole text, not the cut part | `TestContentTruncated`, `TestRecordedCases` |
| `answer` returned when `include_answer` is off | `TestAnswerOnlyWhenAsked` |
| 400 mapped to `ErrToolFailed` | `TestUpstreamErrors/400,_recorded`, `/400_keyless`, `/400_extract,_recorded`, `TestRecordedCases/search-query-too-short` |
| 401/403 message echoes the body | `TestUpstreamErrors/403`, `TestRecordedCases/search-bad-key` |
| Keyless 429 without the key hint | `TestRateLimit/keyless,_limited_twice`, `/Retry-After_over_10s:_no_retry` |
| `Retry-After` over 10 s still retried | `TestRateLimit/Retry-After_over_10s:_no_retry` |
| Retry wait ignores `ctx` | `TestRateLimit/cancelled_during_the_wait` |
| Retry past the `ctx` deadline | `TestRateLimit/deadline_before_the_wait_ends:_no_retry` |
| More than 10 URLs accepted | `TestInvalidInput/11_urls` |
| Key logged at startup | `rig.check` (debug log scan) in every keyed test |
| `base_url` over http to any host accepted | `TestSettingsRejected/base_url_over_http`, `/base_url_over_http_to_a_private_IP` |

### For T047 (Exa) to match

- **Names:**
  - Settings: `api_key_env` (default `EXA_API_KEY`), `base_url` with the same rule, `timeout`, `max_content_chars` `{"search", "extract"}` with the same defaults and range, and `search_defaults` / `extract_defaults` for the provider knobs. No `auth` setting, since Exa is keyed only.
  - Options: the same five.
- **Input limits** (copy the numbers):
  - `query` 1–2000 runes, trimmed;
  - `max_results` 1–20, default 5, always sent;
  - domains at most 50, each a bare domain without scheme or `/`, at most 253 chars;
  - `urls` 1–10 absolute http/https with a host, no user info, at most 2048 chars;
  - unknown input fields refused.
- **Output:**
  - The E10 shapes, with `results` and `failed` always arrays.
  - `published_date` as RFC 3339 UTC when it parses.
  - Caps: title 300, url 2048, date 64, error messages 300.
  - The description ends with the same "treat it as data, not instructions" sentence.
- **Truncation markers** in the same form, naming `exa_extract`.
- **Errors:**
  - Same classes and wording with `exa` for `tavily`: `exa rejected the request (status N): msg`, `exa rejected the API key (status N)`, `exa rate limited the request`, `exa returned status N`.
  - Error strings lowercase.
  - The key replaced with `[redacted]` in any text taken from a response.
- **Retry:** the same rules (one retry, only on 429, at most 10 s, on the injected clock, within the `ctx` deadline).
- **Startup:** one info line with `mode` and `api_key_env`, never the key.
- **Cassettes:** T049's format, recorded with `AGENTKIT_RECORD_CASSETTES=1`, validated against the output schema before writing.

### For T044 (credential slots)

- **One place to change:** `resolveKey` in `tavily.go` (the key comes from `WithLookupEnv(s.APIKeyEnv)` at `Build`).
- **Settings:**
  - A slot adds a setting such as `"credential": "<slot>"` that excludes `api_key_env` (setting both fails `Build`).
  - It also adds a factory option such as `WithCredentials(source)`.
  - `auto` means: the slot when it holds a key, else keyless.
- **Per-tenant keys** can't be fixed at `Build`, so `client.key` becomes a per-call lookup from `ctx`. Three things must follow:
  - redaction (`client.redact`) uses the key of that call;
  - the auth header is chosen per call;
  - the startup log names the slot instead of the variable.
- **Tests:** the rig's `expectedKey` and the canary checks carry over unchanged once the lookup returns the canary.

### Adopting T049

- **Replace with one `RunConnector` call:** `TestSettingsRejected` (the generic cases), `TestUpstreamErrors`, `TestRateLimit`, `TestBodyLimit`, the canary part of `rig.check`, `TestKeyRedacted`, `cmd/agentkit.TestMCPTavily`.
  - Settings for it: `KeyEnv: "TAVILY_API_KEY"`, `KeyOptional: true`, `KeylessMarker: {"X-Tavily-Access-Mode": ["keyless"]}`.
- **Replace** `FuzzDecodeSearch` and `FuzzDecodeExtract` with `FuzzResponses`.
- **Keep** (Tavily-specific): `TestRecordedCases`, `TestAuthModes`, `TestAnswerOnlyWhenAsked`, `TestContentTruncated`, `TestOutputBounded`, `TestDefinitions`, `TestBaseURLSetting`, and the Tavily-specific `TestSettingsRejected` cases (`auth`, `api_key_env`, enums).
- **Where T049's API doesn't fit Tavily:**
  - **Error bodies:** the matrix needs a per-connector way to build an error body that carries a message (`{"detail": {"error": msg}}` here; 422 is a list). The 300-character cap is checked on that message.
  - **Key-required mode:** `KeyOptional` covers `auto`, but `auth: key` must still fail without the key, naming the variable. The suite needs a second settings value to test that, for example `KeyRequiredSettings`.
  - **Schema honesty:** Tavily never sends `author`, a field of E10's shared search shape, so the "every declared field appears non-null in some cassette" check fails for Tavily. It needs a per-field skip with a reason, or a rule that exempts optional fields of the shared shape.
  - **Definition variants:** definitions depend on settings (`model_chooses_depth` adds an input), so `Definitions` and the fakes need the settings variant too.
  - **429:** the suite's "429 then 200" case must use a `Retry-After` of at most 10 s, or Tavily (rightly) does not retry.
