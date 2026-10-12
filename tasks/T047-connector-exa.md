---
id: T047
title: "connectors/exa: search and extract (contents), keyed"
epic: E10
status: review
depends_on: [T026, T036]
---

# T047: `connectors/exa`

## Context

Second connector, wanted by the owner, built in parallel with T046 (Tavily). Exa needs an API key: `x-api-key` header, or `Authorization: Bearer`. Decisions, shared output shape, limits and error rules: [E10](E10-search-connectors.md).

API source of truth: the OpenAPI spec `https://exa.ai/docs/exa-spec.yaml` (`/search`, `/contents`), with guides at `https://exa.ai/docs/search/quickstart.md` and `https://exa.ai/docs/contents/quickstart.md`. Fetch them with `curl`; in Claude sessions WebFetch may fail on DNS.

## Scope

- **Factory:** `exa.Factory(opts...) toolset.Factory` with `Type() == "exa"`.
  - Options: `WithHTTPClient`, `WithBaseURL`, `WithLookupEnv`, `WithClock`, `WithLogger`.
- **Settings:**
  - `api_key_env`, default `EXA_API_KEY`; required, and a missing variable fails `Build`;
  - `timeout`, default 30 s;
  - `max_content_chars`;
  - `search_defaults`: `type` (default `auto`), content mode `highlights` (default) or `text` with `max_characters`, `category`;
  - `extract_defaults`: `text.max_characters`, `livecrawl` policy if the spec still has it, otherwise its current freshness option.
  - Unknown fields are refused, `api_key` included.
- **`exa_search`:**
  - inputs: `query`, `max_results` (1–20, default 5, sent as `numResults`), `include_domains`, `exclude_domains`, `start_published_date` / `end_published_date` (ISO dates), `category` (the spec's enum), `type` (only if settings allow the model to choose);
  - POST `/search` with `contents` from the settings, mapped to E10's search shape (`content` = joined highlights, or the text; `published_date`, `author`, `score` when present).
- **`exa_extract`:**
  - inputs: `urls` (1–10), optional `query` (used for highlights);
  - POST `/contents`; per-URL `statuses` errors go to `failed`.
- **Wiring:**
  - add the factory to `cmd/agentkit/catalog.go`;
  - add `exa` (`optional: true`) to `deploy/local/agent/search.json` if T046 created it, else create it with `exa` and demo;
  - extend the QUICKSTART section with `EXA_API_KEY`.

## Out of scope

- `findSimilar`, `answer`, Agent, Monitors, Websets. Per-tenant keys (T044).

## Acceptance criteria

**Test kit (added 2026-10-12, [T049](T049-connector-test-kit.md)).**
- `toolsettest.RunConnector` runs the generic checks: settings, missing key, the error matrix (400/401/429/5xx/garbage/oversized/hang), the retry rule, the canary, no network, MCP and concurrency. Criteria below that it covers are ticked by "passes `RunConnector`"; don't hand-write those tests.
- Write cassettes in T049's format.
- Accept a `base_url` setting (`https` unless loopback).
- Make no request in `Build`.
- Add the three-line `FuzzResponses` target.
- If T049 is not on `main` yet, build the connector and its specific tests first, then adopt the suite when it lands.


- [x] **Search request:** sends the documented JSON (golden request body: `query`, `numResults`, `type`, `contents`, filters) with the `x-api-key` header; the recorded response maps to E10's search shape (golden output), for both highlights and text modes. `TestSearchRequestAndMapping` (5 cassettes: highlights with every filter, defaults, text, highlights with `maxCharacters`, deep with `answer`; the request must be JSON-equal to the cassette's `.req.json`), the header on every request by the rig's `checkTest`. The responses are **synthetic** until recorded (Notes).
- [x] **Extract** maps successes and per-URL failures (`statuses`) (golden). `TestExtractMapsSuccessesAndFailures` (`contents-partial`, `contents-query`).
- [x] **Settings validation:** a missing `EXA_API_KEY` fails `Build` naming the variable; an `optional` toolset is skipped and shown degraded in `/readyz`; a literal `api_key`, unknown fields and bad enums fail with clear messages. Passes `RunConnector` (`settings/*`: missing key named, literal `api_key`, unknown field, plain-http and loopback `base_url`, `base_url` routing). `TestBuildRejectsSettings` (18 Exa-specific bad settings: enums, nested unknown fields, timeouts, caps, `base_url` forms; a custom variable name and a blank value), `cmd/agentkit` `TestServeExaWithoutKeyIsDegraded`.
- [x] **Input validation:** an empty query, `max_results` out of range, a bad date, an unknown category, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent. `TestInvalidInputSendsNothing` (26 cases); five of them again in `RunConnector`'s `invalid`, which also checks that no request is sent.
- [x] **Errors:** passes `RunConnector` (`errors/*`: 400, 401, 403, 422, 500, 502, 503, malformed JSON, oversized body, hang, connection refused, `429_retry_after`, `429_then_200`). Exa-specific, beyond the suite: `TestUpstreamErrors` (Exa's error-body format, the typed sentinels, 402, the message cap, no echo of other bodies), `TestRateLimitRetry` (the 10 s cap, a second 429, no `Retry-After`, the call timeout ending before the wait), `TestCancelStopsRetryWait`.
  - 400 gives `ErrInvalidInput` with Exa's message (capped);
  - 401/403 give "rejected the API key";
  - 429 retries once after `Retry-After` (fake clock), then fails;
  - 5xx gives the status only, with no retry;
  - a cancelled `ctx` stops the wait.
- [x] **Limits:** truncation at a rune boundary with the marker; the body limit is enforced. `TestLimits` (the marker, and a body of exactly 8 MB accepted and one byte more refused); passes `RunConnector` (`errors/oversized_body`, which also checks that at most limit+1 bytes are read).
- [x] **Canary:** the key appears in no tool output, error, log line or MCP response, including when the upstream echoes it. Passes `RunConnector` (`canary`: the key echoed in 400, 401 and 500 bodies, over MCP and in logs; and after every subtest). `TestKeyNeverLeaks` (the key JSON-escaped, and in every field of a 200's results), the rig's checks after every call and test, `cmd/agentkit` `TestServeExaOverMCP`.
- [x] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeContents` never panic (and return the shared shape or `ErrUpstream`); `FuzzResponses` and `FuzzResponsesExtract` (T049's target, through the tools).
- [x] **Over MCP:** `/mcp/exa` lists exactly `exa_search` and `exa_extract` with schemas, and a call through the MCP client against the fake upstream returns the mapped result. `cmd/agentkit` `TestServeExaOverMCP`; passes `RunConnector` (`mcp`, `concurrency`).
- [ ] **Live (`-tags live`, needs `EXA_API_KEY`):** a search returns results with URLs and content, and extracting a known page returns its text. If the key is not set, leave this unticked with `status: review`.
- [x] **Mutation list** recorded (at least: `numResults` not sent, highlights not joined, per-URL failures dropped, key header missing, key not redacted from an echoed error, retry on 5xx). In Notes.

## Notes

### Status: review

Two things keep this in `review`; nothing else is open.

- **No `EXA_API_KEY` in this session.** `TestLive` (`go test -tags live ./connectors/exa/`) skipped, so the live criterion is unticked. It was not run in any form.
- **All seven cassettes are synthetic** (`"synthetic": true` in each `.req.json`). They were built by hand from the response schema of Exa's OpenAPI spec and the examples in its docs, because nothing could be recorded. `TestCassettesMatchSpec` checks each one against the vendored spec, and lists them.
  - The per-URL error tags in `contents-partial` (`CRAWL_NOT_FOUND`, `CRAWL_TIMEOUT`) are illustrative: the spec types `statuses[].error.tag` as a free string.

To finish, in a session with the key:

```sh
EXA_API_KEY=... AGENTKIT_RECORD_CASSETTES=1 go test -tags live -count=1 -run TestLive ./connectors/exa/
make golden   # review the output goldens, then make check
```

- `TestLive` makes exactly the calls of `searchCases` and `extractCases`, so recording rewrites every cassette, without `synthetic`.
  - It keeps a cassette only if the output passed `checkShape`.
  - It refuses one that contains the key.
- `TestExtractMapsSuccessesAndFailures` asserts how many results and failures `contents-partial` gives (1 and 3). A real recording may change those counts, for example if `example.com/missing-page` answers 200. Adjust the counts to what was recorded.
- Then tick the live criterion and set `done`.

### Files

- **`connectors/exa`** (new):
  - `doc.go`, `exa.go` (factory, options, settings, `Build`), `client.go` (HTTP, retry, redaction, truncation), `search.go`, `contents.go`;
  - tests:
    - `rig_test.go`: the fake upstream and the rig, with its invariants after every call and every test;
    - `cassette_test.go`: cassette replay, a stand-in for T049's `toolsettest.Upstream`;
    - `spec_test.go`: the OpenAPI check;
    - `conformance_test.go`: Exa described to T049's kit, `TestConformance` (`RunConnector`) and the two `FuzzResponses` targets;
    - `exa_test.go`, `fuzz_test.go`, `example_test.go`, `export_test.go`, `live_test.go` (`-tags live`);
  - `testdata/`: `cassettes/`, `golden/` (mapping outputs `*.output.json`, suite outputs `<call>.json`), and `openapi/`:
    - `exa.json`, the spec slice;
    - `cut.go`, which reproduces that slice byte for byte from the published YAML;
    - a `README.md`.
- **`cmd/agentkit`:**
  - `catalog.go`: one line, the factory with `os.LookupEnv`, next to Tavily's;
  - `exa_test.go`: the MCP and degraded tests;
  - `tavily_test.go`: Tavily's `TestSearchConfigBuilds`, which collided by name with mine on rebase, became the one test for `search.json`. It now expects agent toolsets `demo`, `tavily`, `exa`, and `exa` skipped for its missing key unless `EXA_API_KEY` is set.
- **`internal/archtest/rules.go`:** removes Exa's `PendingConnectors` entry (T049's exemption until adoption). Otherwise unchanged: T046's `connectors` tree rule (`pkg/tool`, `pkg/toolset`, `pkg/clock`, and in tests `clocktest`, `internal/testkit` and `toolsettest`) is a superset of the one this task first added, so the rebase kept T046's alone.
- **`deploy/local/agent/search.json`:** shared with T046. It now holds the `demo`, `tavily` and `exa` toolsets in both `mcp.toolsets` and `agent.toolsets`, with `exa` `optional: true`, and the system prompt names both search tools.
- **`docs/QUICKSTART.md`:** §6 "Web search", one section shared with T046, with a subsection each for Tavily and Exa.
- No new dependency. `go.yaml.in/yaml/v4`, already in `go.mod`, is used only by `testdata/openapi/cut.go`, a `go:build ignore` tool.

### Tools

Both schemas are derived by `tool.Func` from the Go types, and both tools declare an output schema. `additionalProperties: false` holds everywhere; slices are typed `["null","array"]` by the derivation, but outputs always carry `[]`.

**`exa_search`**

- Input:
  - `query` (string, required);
  - `max_results` (integer, 1–20, default 5, sent as `numResults`);
  - `include_domains`, `exclude_domains` (arrays of strings: `example.com`, `example.com/docs`, `*.example.com`);
  - `start_published_date`, `end_published_date` (string: `YYYY-MM-DD` or RFC 3339);
  - `category` (string: `company`, `publication`, `news`, `personal site`, `financial report`, `people`);
  - `type` (string: `instant`, `fast`, `auto`, `deep-lite`, `deep`, `deep-reasoning`), present only with `search_defaults.model_chooses_type`.
- Output: `{"results": [{"title", "url", "published_date"?, "author"?, "content"}], "answer"?}`.
  - `title`, `url` and `content` are required.
  - `content` is the highlights, joined with `"\n…\n"`, or the text.
  - `answer` appears for deep types only.

**`exa_extract`**

- Input: `urls` (array of 1–10 absolute `http`/`https` URLs, no credentials, at most 2,048 characters each; duplicates are sent once); `query` (string, optional).
- Output: `{"results": [{"url", "title"?, "content"}], "failed": [{"url", "error"}]}`.
  - With a `query`, `content` is the page's highlights for it; otherwise it is the page text.
  - `error` is Exa's tag and HTTP status, for example `CRAWL_NOT_FOUND (HTTP 404)`, or `no content returned` for a URL Exa did not mention.

### Decisions, and differences from Tavily for the planner to reconcile

- **No `score`.** Exa's `/search` results have no relevance score in the spec (only `highlightScores` per highlight), so `score` is never set. Declaring it would also fail T049's schema-honesty check, since it would never appear.
- **`answer` only for deep types.** It is set only when `type` is `deep-lite`, `deep` or `deep-reasoning` and Exa returns a string in `output.content`. It is capped at `max_content_chars.extract` with the short marker. Exa's `answer` endpoint is out of scope.
- **`max_content_chars` is an object `{search, extract}`.** E10 gives two defaults (4,000 and 20,000), and one number could not express both. If Tavily took a single number, one of the two connectors should change.
- **Truncation marker.**
  - Content ends with `…[truncated N characters; <hint>]`, with `N` comma-grouped and counted in characters (runes).
  - The hints are:
    - search: "call exa_extract on this URL for the full page";
    - extract: "call exa_extract again with a query to get the passages that matter". Calling `exa_extract` again for the same page cannot return more, but a `query` returns the relevant passages instead of the first N characters.
  - Titles, URLs, dates and error messages get the short `…[truncated]`.
  - In text mode, `search_defaults.max_characters` defaults to the search content cap, so Exa cuts the page on its side and the connector sees no overflow. The marker then does not appear, although the page was longer. It reports only what the connector itself cut. In highlights mode, and in extract with `extract_defaults.max_characters` above the cap, the connector does the cutting. The planner may want Tavily aligned on which side cuts. A content hint in an error would steer the agent wrongly.
- **Exported error sentinels**, checked with `errors.Is`, all inside `tool.Error`: `ErrMissingKey`, `ErrKeyRejected`, `ErrRateLimited`, `ErrUpstream`, `ErrResponseTooLarge`.
- **Content mode is a setting, not an input.** The model sees only `type`, and only with `search_defaults.model_chooses_type`.
- **Dates.** `YYYY-MM-DD` or RFC 3339 is accepted. An end date that is a bare day covers that whole day (`T23:59:59.999Z`), because Exa's fields are date-times.
- **`category`.** The spec's schema is an enum of the six values. Its description also says "other strings are accepted" as hints. The connector follows the enum, so the model cannot invent a category. `TestEnumsMatchSpec` checks that each search type and category the connector accepts is in the spec.
- **`base_url`** (a setting, which overrides `WithBaseURL`) must be `https`, or `http` to `localhost` or a loopback IP, with no credentials, query or fragment. `WithBaseURL` follows the same rule.
- **`Build` makes no request.** The rig fails any test where it does.

### Toward T049 (`toolsettest`)

**Adopted.** T049's kit reached `main` while this task was in progress (`686b6ff`, then `7923283`, which accepts `synthetic` cassettes). `TestConformance` runs `RunConnector` with `schema_honesty.dead_fields` enabled, and the suite lists the seven synthetic cassettes. Its goldens (`testdata/golden/<call>.json`) equal the mapping goldens of the same cassettes.

- **One configuration, three calls.** `Call` has no per-call `Settings`, so `connector()` uses the union of three cases' settings: `model_chooses_type` from `search-deep`, and `extract_defaults` from `contents-partial`. They do not interact: `model_chooses_type` adds the `type` input and leaves a call without one unchanged, and `extract_defaults` touch only `exa_extract`. So `search-defaults`, `search-deep` and `contents-partial` are suite calls, and `answer` and `failed[]` appear, as `dead_fields` requires. Text mode and the highlights cap change the search request, so they stay in `TestSearchRequestAndMapping` only.
- **The suite found a bug.** After an oversized body, the client's deferred drain read up to another 8 MB, to keep the connection; `errors/oversized_body` allows at most limit+1 bytes. My own body-limit test checked only the error. The drain is gone: a body within the limit is already read to EOF, so it only ever ran past the limit.
- **Deleted as duplicates of the suite:**
  - `TestConcurrentCalls`;
  - the literal-`api_key`, unknown-field and plain-http-`base_url` rows of `TestBuildRejectsSettings`;
  - the three raw error-echo rows of `TestKeyNeverLeaks`.
- **Kept, because they check what the suite does not:**
  - `TestUpstreamErrors`: Exa's error body (`requestId`, `error`, `tag`), the sentinels (`ErrKeyRejected`, `ErrUpstream`), 402, the message cap, no echo of other bodies;
  - `TestRateLimitRetry` and `TestCancelStopsRetryWait`: the 10 s cap, a second 429, no `Retry-After`, the call timeout ending before the wait, cancel mid-wait;
  - the 8 MB boundary in `TestLimits` (exactly 8 MB accepted; the suite sends limit+4096);
  - `TestKeyNeverLeaks`' escaped and 200-result echoes;
  - `cmd/agentkit` `TestServeExaOverMCP`, which goes through the binary's `/mcp/exa` route;
  - the mapping tests, `TestInvalidInputSendsNothing`, rune truncation and the marker, the spec tests, and the two decoder fuzz targets.
- **Not replaced yet:** `cassette_test.go` and the rig still replay the mapping cases. Moving them onto `toolsettest.Upstream` is a separate refactor (Tavily kept its own too).

- **Where T049's API does not fit Exa:**
  - **Content mode is a setting.** One `Connector` cannot cover both highlights and text, even with the settings union above. `Call` needs a per-call `Settings` (as `searchCases` has), or the connector is registered twice.
  - **Schema honesty.** `answer` appears only for deep types, so a paid deep search must be among the recordings, and `author` must appear at least once. Both are true of the synthetic set.
  - **The OpenAPI checker** in `spec_test.go` belongs in the kit, because Parallel's spec needs it too.
    - It covers the keywords Exa's slice uses: `$ref`, `type`, `enum`, `const`, `anyOf`, `oneOf`, `properties`, `required`, `additionalProperties`, `propertyNames`, `items`, `min`/`maxItems`, `min`/`maxLength`, `pattern`, `format`, `minimum`, `maximum`, `exclusiveMinimum`.
    - It fails on any other keyword.
    - It needs no dependency, so the archtest allow-list stands.
  - **Exa's `/search` and `/contents` responses are `additionalProperties: false`** in the spec. Any field Exa adds without updating the spec will fail the spec check after a re-recording. That is drift worth seeing, but the kit may want it reported as a warning.

### For T044: a credential slot instead of `api_key_env`

Today `Build` reads `lookupEnv(settings.api_key_env)` once and stores the key in the client. With T044:

- the setting becomes a credential reference, for example `"credential": "exa"` (slot name), and `api_key_env` stays as the single-tenant fallback;
- `Build` checks only that the slot exists. It reads no secret, consistent with no request in `Build`;
- the client resolves the key per call from the call's tenant: `Credentials.Get(ctx, tenant, slot)`, through an option such as `WithCredentials(resolver)` that replaces `WithLookupEnv`.

Everything downstream is unchanged:

- `send` sets `x-api-key` from the resolved value;
- `redact` takes that value, not a field, so each tenant's key is redacted from its own responses;
- the canary tests run per tenant: two tenants with two canaries, neither appearing in the other's calls;
- `ErrMissingKey` moves from `Build` to the call and names the slot, not a variable.

### Mutation list

Each mutant was planted in `connectors/exa/*.go`, one at a time, on a copy of the tree. For each, `go test -count=1 -timeout 90s ./connectors/exa/` and the exa tests of `cmd/agentkit` were run.

**Result: 50 of 50 caught**, re-run after adopting `RunConnector` and deleting the tests it duplicates. None failed to compile, and none hung.

Before reaching that result:
- the re-run after adoption had one survivor, "default LookupEnv reads the process env": its only killer was the deleted "no `LookupEnv` reads nothing" subtest. The suite always passes `WithLookupEnv`, so the subtest was not a duplicate, and it is back;
- "body read past the limit" is new: it plants the over-read the suite found. Only `RunConnector`'s `oversized_body` catches it;
- an earlier run had one survivor, "failed result still listed in results", now covered by `TestExtractMapsSuccessesAndFailures/a failed URL's result is dropped`;
- a panic in a `TestLimits` subtest hid other tests' results; `only()` now fails the test instead of panicking;
- the retry tests had no real-time deadline, so a regression would have hung the run for 10 minutes. `waitDone` now gives each wait 10 s, then fails.

The six mutants the task requires are among the rows below (`numResults` not sent, highlights not joined, per-URL failures dropped, key header missing, key not redacted, retry on 5xx).

| Mutant | Caught by |
|---|---|
| numResults not sent | `TestConformance`, `TestSearchRequestAndMapping` |
| max_results default ignored | `TestConformance`, `TestSearchRequestAndMapping` |
| highlights not joined (first only) | `TestConformance`, `TestSearchRequestAndMapping`, `TestServeExaOverMCP` |
| empty highlights not skipped | `TestConformance`, `TestSearchRequestAndMapping` |
| search contents mode ignored | `TestLimits`, `TestSearchRequestAndMapping` |
| text mode maxCharacters not sent | `TestSearchRequestAndMapping` |
| author dropped | `TestConformance`, `TestSearchRequestAndMapping` |
| answer not mapped | `TestConformance`, `TestSearchRequestAndMapping` |
| unknown category accepted | `TestInvalidInputSendsNothing` |
| end date not end of day | `TestSearchRequestAndMapping` |
| company/people filter check removed | `TestInvalidInputSendsNothing` |
| search content cap not applied | `TestLimits` |
| per-URL failures dropped | `TestConformance`, `TestExtractMapsSuccessesAndFailures` |
| failed result still listed in results | `TestExtractMapsSuccessesAndFailures` |
| unmentioned URL not reported | `TestConformance`, `TestExtractMapsSuccessesAndFailures` |
| query highlights ignored in extract | `TestExtractMapsSuccessesAndFailures` |
| max_age_hours not sent | `TestConformance`, `TestExtractMapsSuccessesAndFailures` |
| more than 10 URLs allowed | `TestConformance`, `TestInvalidInputSendsNothing` |
| non-http URL accepted | `TestConformance`, `TestInvalidInputSendsNothing` |
| URL credentials accepted | `TestInvalidInputSendsNothing` |
| extract content cap not applied | `TestLimits` |
| key header missing | every test that makes a call (the rig and `RunConnector` check the header on each request) |
| key also sent as Bearer | every test that makes a call (the rig and `RunConnector` check the header on each request) |
| key not redacted | `TestConformance`, `TestKeyNeverLeaks`, `TestServeExaOverMCP` |
| retry on 5xx | `TestConformance`, `TestUpstreamErrors` |
| 429 retried twice | `TestConformance`, `TestRateLimitRetry` |
| Retry-After ignored | `TestConformance`, `TestRateLimitRetry` |
| Retry-After cap not enforced | `TestRateLimitRetry` |
| cancel does not stop the wait | `TestCancelStopsRetryWait` |
| 400 message not carried | `TestConformance`, `TestServeExaOverMCP`, `TestUpstreamErrors` |
| 400 message not capped | `TestConformance`, `TestUpstreamErrors` |
| 422 not invalid input | `TestConformance`, `TestUpstreamErrors` |
| 401 echoes the body | `TestConformance`, `TestUpstreamErrors` |
| truncation splits a rune | `TestLimits` |
| body limit not enforced | `TestLimits` |
| invalid input reported as tool failure | `TestInvalidInputSendsNothing`, `TestKeyNeverLeaks`, `TestUpstreamErrors` |
| literal api_key gets the generic message | `TestBuildRejectsSettings` |
| empty key accepted | `TestBuildRejectsSettings` |
| default LookupEnv reads the process env | `TestBuildRejectsSettings` |
| bad search type in settings accepted | `TestBuildRejectsSettings` |
| max_age_hours range unchecked | `TestBuildRejectsSettings` |
| marker without the count | `TestLimits` |
| marker without the hint | `TestLimits` |
| count not grouped | `TestLimits` |
| error message gets the content hint | `TestUpstreamErrors` |
| titles get the content marker | `TestLimits` |
| base_url: plain http to any host | `TestBuildRejectsSettings`, `TestConformance` |
| base_url setting ignored | `TestBuildRejectsSettings`, `TestConformance` |
| base_url credentials accepted | `TestBuildRejectsSettings` |
| body read past the limit | `TestConformance` |

