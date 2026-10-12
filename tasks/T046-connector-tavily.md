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

Status of this block (T049's first cut landed on `main` after the connector did; adopted 2026-10-12, see Notes, "Adopting T049"):
- [x] passes `toolsettest.RunConnector` — `TestConformance` (`conformance_test.go`: two calls, `search_go_release` and `extract_partial_failure`, from the recorded cassettes; four invalid inputs; `KeyOptional` with the keyless header; Tavily's error body). Its goldens `testdata/golden/search_go_release.json` and `extract_partial_failure.json` are identical to the recorded `*.output.json`
- [x] cassettes in T049's format, recorded from the real API (none synthetic) — `testdata/cassettes/*.req.json` + `*.res.http`, written by `TestLiveRecord` with `AGENTKIT_RECORD_CASSETTES=1`; replayed and matched by `TestRecordedCases`
- [x] `base_url` setting, `https` unless localhost or a loopback IP — `TestBaseURLSetting`, `TestSettingsRejected/base_url_*`
- [x] no request in `Build` — `TestAuthModes` (zero upstream requests after `Build`, every mode); `TestE2EServeTavilyOptional` starts the server offline
- [x] the three-line `FuzzResponses` target — `FuzzResponses` (`tavily_search`) and `FuzzResponsesExtract` (`tavily_extract`): the kit fuzzes one tool per target


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
  - `TestConformance/settings/*` (unknown field, literal `api_key`, http `base_url`, `base_url` routing); `TestSettingsRejected` (22 Tavily cases: `auth`, `api_key_env`, every enum and range, `base_url` forms; each message names the field or variable and never holds the canary); `TestOptionalToolsetSkipped`; `cmd/agentkit.TestE2EServeTavilyOptional` (`/readyz` degraded naming `tavily-keyed` and its variable, while `/mcp/tavily` serves); `cmd/agentkit.TestSearchConfigBuilds`
- [x] **Input validation:** an empty query, `max_results` out of range, an unsupported enum, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent.
  - `TestInvalidInput` (25 cases, each `ErrInvalidInput` and not `ErrToolFailed`, zero upstream requests); `TestConformance/invalid/*` (4 of them, through the kit)
- [x] **Errors:**
  - 400 gives `ErrInvalidInput` with Tavily's message (capped);
  - 401 gives "rejected the API key";
  - 429 in keyless mode retries once after `Retry-After` (fake clock), then fails with the "set TAVILY_API_KEY" hint;
  - 500 gives the status only, with no retry;
  - a cancelled `ctx` stops a retry wait.
  - `TestConformance/errors/*` (400 and 422 with the message capped at 300, 401, 403, 500, 502, 503 without retry, `429_retry_after`, `429_then_200`, malformed JSON, oversized body, hang, connection refused);
  - `TestUpstreamErrors` (Tavily's own bodies: 400/422/extract 400 recorded, 400 keyless, 400 without body, 401 recorded, 432 plan limit, 200 wrong types, the `timeout` setting; each class checked with `errors.Is`, exactly one request);
  - `TestRateLimit` (fake clock: the keyless hint, then ok at the 10 s edge, no or unparseable `Retry-After`; over 10 s no retry; deadline before the wait ends no retry; cancel during the wait gives `context.Canceled`)
- [x] **Limits:**
  - content over `max_content_chars` is cut at a rune boundary with the marker;
  - a response over the body limit fails cleanly;
  - total output stays bounded.
  - `TestContentTruncated` (ASCII, 2- and 4-byte runes, at the limit, custom limit, plus answer, extract content and title markers); `TestConformance/errors/oversized_body` (fails, and reads at most limit+1 bytes); `TestBodyLimit` (a 400 over 8 MB, exactly 8 MB passes); `TestOutputBounded` (hostile bodies stay under a computed byte bound with the requested item counts)
- [x] **Canary:** a key value appears in no tool output, error, log line (debug included) or MCP response, including when the upstream echoes it in an error body.
  - `TestConformance/canary` (key echoed in 400, 401 and 500, through the tools and over MCP, plus logs) and the kit's check after every subtest; `rig.check` after every Tavily test (outputs, errors, debug logs, request bodies, and the auth mode); `TestKeyRedacted` (key echoed in 422, extract 400, search and extract results, and JSON-escaped); `cmd/agentkit.TestMCPTavily` (every MCP response body of the binary's server, with the upstream echoing the key)
- [x] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeExtract` (hostile upstream bodies) never panic and always return the E10 shape or an error.
  - Replaced, as the test-kit block asks, by `FuzzResponses` and `FuzzResponsesExtract` (seeded from the cassettes): no panic, error is `ErrInvalidInput` or `ErrToolFailed`, output passes the declared output schema; 60 s each, clean. The two properties the old targets also fuzzed (byte bound, no key) are now fixed-input tests: `TestOutputBounded` and `TestKeyRedacted`
- [x] **Over MCP:** `/mcp/tavily` lists exactly `tavily_search` and `tavily_extract` with input and output schemas, and a call through the MCP client against the fake upstream returns the mapped result.
  - `TestConformance/mcp` (listed definitions equal the tools', structured and text content equal the golden, an invalid call is an error result); `cmd/agentkit.TestMCPTavily` (the binary's MCP server and the SDK client: exactly the two tools, both schemas, a search whose structured content equals `search-go-release.output.json`, two failing calls as tool errors, no key in any response)
- [ ] **Live (`-tags live`), keyless:**
  - a search for a current topic returns at least one result with a URL;
  - extracting `https://www.tavily.com` returns content;
  - with `TAVILY_API_KEY` set, the same works keyed.
  - not ticked: keyless passed (`TestLiveKeyless`, `TestLiveRecord`, 2026-10-12, below); keyed not run, `TAVILY_API_KEY` is not set in this session
- [ ] **Live end to end:** `agentkit serve` with `search.json` answers "What is the latest Go release? Cite the source." with a `tool_call` to `tavily_search` and a URL in the answer, against real Claude (`ANTHROPIC_KEY`).
  - not run: this session has no Anthropic key (`ANTHROPIC_KEY` and `ANTHROPIC_API_KEY` unset, no `ant`), so `cmd/agentkit.TestLiveServeSearch` skips. Substitute check, below: `agentkit serve` with `search.json`, keyless, `tools/call tavily_search` over `/mcp/tavily` returned real results
- [x] **Mutation list** recorded (at least: keyless header sent with a key, `failed_results` dropped, truncation splitting a rune, retry on 500, key not redacted from an echoed error, `max_results` default ignored).
  - below: 21 plants, all caught; each is named by a failing test, and retry on 500 by `TestConformance/errors/500` when run alone (a T049 hang hides the name in a full run)

## Notes

Status is `review`, not `done`. Two things are still open: the keyed live run (needs `TAVILY_API_KEY`) and the live end to end with real Claude, which needs an Anthropic key this session did not have. T049's suite is adopted. Everything else is in place and on `main`.

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

Each plant was made in a copy of the tree, run with `go test -count=1 -timeout=150s ./connectors/tavily/`, then restored. The list was re-run on 2026-10-12 after `RunConnector` replaced the generic tests, with two body-limit plants added. All 21 fail the package (no compile errors counted, no survivors).

The first pass had one survivor: an `answer` passed on when not asked for. `TestAnswerOnlyWhenAsked` was added for it.

**Retry on 500** is caught, but not by name in a full run. `TestConformance/errors/500` fails by name after the kit's 10 s watchdog when run alone (`-run TestConformance/errors/500`). In the full package, `TestConformance/canary` blocks: the kit's `callMCP` has no watchdog, and the wrongly retried 500 waits on the fake clock forever. The package then dies on its timeout before the parent test prints its subtests. This is a T049 bug, reported to its planner.

| Plant | Caught by |
|---|---|
| Keyless header sent with a key (both headers on keyed requests) | `TestAuthModes` (keyed cases), `rig.check` in every keyed test |
| `failed_results` dropped | `TestConformance/calls/extract_partial_failure`, `TestRecordedCases/extract-partial-failure`, `TestOutputBounded/extract` |
| Truncation splitting a rune (cut by bytes) | `TestContentTruncated` (2-byte, 4-byte, custom limit), `TestRecordedCases/search-news-answer` |
| Retry on 500 (and other 5xx) | `TestConformance/errors/500` (alone; see above), the package timeout in a full run |
| Key not redacted from text taken from a response | `TestConformance/canary`, `TestKeyRedacted`, `TestOutputBounded` |
| `max_results` default ignored (0 sent) | `TestConformance` (cassette request mismatch), `TestRecordedCases`, `TestContentTruncated`, `ExampleFactory` |
| Results not capped at `max_results` | `TestOutputBounded/search` |
| Content marker without how to get more | `TestContentTruncated`, `TestRecordedCases` |
| Content marker counts the whole text, not the cut part | `TestConformance` (golden), `TestContentTruncated`, `TestRecordedCases` |
| `answer` returned when `include_answer` is off | `TestAnswerOnlyWhenAsked` |
| 400 mapped to `ErrToolFailed` | `TestConformance/errors/400`, `TestUpstreamErrors` (recorded 400s), `TestRecordedCases/search-query-too-short` |
| 401/403 message echoes the body | `TestConformance` (canary in the 401 body), `TestRecordedCases/search-bad-key` |
| Keyless 429 without the key hint | `TestRateLimit/keyless,_limited_twice`, `/Retry-After_over_10s:_no_retry` |
| `Retry-After` over 10 s still retried | `TestRateLimit/Retry-After_over_10s:_no_retry` |
| Retry wait ignores `ctx` | `TestRateLimit/cancelled_during_the_wait` |
| Retry past the `ctx` deadline | `TestRateLimit/deadline_before_the_wait_ends:_no_retry` |
| More than 10 URLs accepted | `TestInvalidInput/11_urls` |
| Key logged at startup | `TestConformance` (log scan after every subtest), `FuzzResponses*` seeds, `rig.check` in every keyed test |
| `base_url` over http to any host accepted | `TestConformance/settings/plain_http_base_url`, `TestSettingsRejected/base_url_over_http_to_a_private_IP` |
| Body read without `io.LimitReader` | `TestConformance/errors/oversized_body` (bytes read over limit+1) |
| Body limit off by one (`>=`) | `TestBodyLimit/just_under_the_limit` (exactly 8 MB) |

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
- **Tests:** copy `conformance_test.go`: one `toolsettest.Connector` (without `KeyOptional`, since Exa is keyed only, so the suite's `missing_key` runs), `TestConformance`, and one `FuzzResponses` target per tool. Hand-write only what is Exa's own: request mapping against the cassettes, its error bodies, truncation, and output bounds.

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

Adopted on 2026-10-12 against T049's first cut (`686b6ff`). `TestConformance` passes as it is. Its two goldens are byte-identical to the recorded outputs, and nothing in the connector had to change except `WithBaseURL("")`, which now keeps the default (the suite builds with an empty `BaseURL` to test the setting).

- **Removed, now `RunConnector`'s:**
  - `FuzzDecodeSearch` and `FuzzDecodeExtract`;
  - the generic cases of `TestUpstreamErrors` (403, 500, 502, 200 not JSON, the 300-character cap, transport error);
  - `TestRateLimit`'s keyed "limited twice";
  - `TestBodyLimit`'s 200 over the limit;
  - `TestKeyRedacted`'s plain 400 echo;
  - three `TestSettingsRejected` cases (literal `api_key`, unknown field, http `base_url`).
- **Kept** (beyond the suite):
  - `rig.check` after every test, since the user brief asks for an auth-mode check after every test and the suite's check covers only its own subtests;
  - `cmd/agentkit.TestMCPTavily`, which covers the binary's `/mcp/tavily` (the suite serves the tools on its own MCP server);
  - the rate-limit rules the suite does not try: the keyless hint, missing or unparseable `Retry-After`, over 10 s, the deadline and cancel;
  - the body limit at exactly 8 MB and on a 400.
- **Where T049's API doesn't fit Tavily** (sent to the T049 planner):
  - **Key-required mode:** with `KeyOptional`, the suite skips `missing_key`, so `auth: key` without the key is tested only by `TestSettingsRejected/key_mode_without_the_variable`. A `KeyRequiredSettings` field (settings under which the key is required) would let the suite cover it.
  - **Definition variants:** definitions depend on settings (`model_chooses_depth` adds an input), and the suite sees only `Settings`. `TestDefinitions` covers both variants. `Definitions` and the fakes would need the settings too.
  - **`FuzzResponses` checks less than the targets it replaced.** It does not put the key in the fuzzed body or bound the output size. Both are cheap to add in the kit: the canary is known, and an optional `MaxOutputBytes` per tool would do.
  - **One tool per fuzz target**, so a two-tool connector needs two targets (four lines, not three).
  - **Schema honesty** (every declared field non-null in some cassette) is not in the first cut. If it is added, Tavily fails on `author`, which is in E10's shared search shape but which Tavily never sends. It will need a per-field exemption with a reason.
  - **`callMCP` has no watchdog.** A connector that blocks on the fake clock (one that wrongly retries a 500) hangs `canary` until the package timeout, which hides the named `errors/500` failure. `rig.invoke` already has a watchdog; `callMCP` needs the same.
  - Resolved by the first cut: per-connector error bodies (`ErrorBody`), and a `429_then_200` within 10 s (it uses `Retry-After: 1`).
