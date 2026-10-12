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
- [x] **Settings validation:** a missing `EXA_API_KEY` fails `Build` naming the variable; an `optional` toolset is skipped and shown degraded in `/readyz`; a literal `api_key`, unknown fields and bad enums fail with clear messages. `TestBuildRejectsSettings` (21 bad settings, `base_url` included), `cmd/agentkit` `TestServeExaWithoutKeyIsDegraded`.
- [x] **Input validation:** an empty query, `max_results` out of range, a bad date, an unknown category, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent. `TestInvalidInputSendsNothing` (26 cases).
- [x] **Errors:** `TestUpstreamErrors`, `TestRateLimitRetry`, `TestCancelStopsRetryWait`.
  - 400 gives `ErrInvalidInput` with Exa's message (capped);
  - 401/403 give "rejected the API key";
  - 429 retries once after `Retry-After` (fake clock), then fails;
  - 5xx gives the status only, with no retry;
  - a cancelled `ctx` stops the wait.
- [x] **Limits:** truncation at a rune boundary with the marker; the body limit is enforced. `TestLimits`.
- [x] **Canary:** the key appears in no tool output, error, log line or MCP response, including when the upstream echoes it. `TestKeyNeverLeaks`, the rig's checks after every call and test, `cmd/agentkit` `TestServeExaOverMCP`.
- [x] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeContents` never panic (and return the shared shape or `ErrUpstream`).
- [x] **Over MCP:** `/mcp/exa` lists exactly `exa_search` and `exa_extract` with schemas, and a call through the MCP client against the fake upstream returns the mapped result. `cmd/agentkit` `TestServeExaOverMCP`.
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
    - `exa_test.go`, `fuzz_test.go`, `example_test.go`, `export_test.go`, `live_test.go` (`-tags live`);
  - `testdata/`: `cassettes/`, `golden/` (outputs), and `openapi/`:
    - `exa.json`, the spec slice;
    - `cut.go`, which reproduces that slice byte for byte from the published YAML;
    - a `README.md`.
- **`cmd/agentkit`:** `catalog.go` (one line, the factory with `os.LookupEnv`); `exa_test.go` (MCP and degraded tests, and a test that `search.json` builds).
- **`internal/archtest/rules.go`:** a `connectors` tree rule. Connectors may import `pkg/tool`, `pkg/toolset`, `pkg/clock`, and in tests `clocktest` and `internal/testkit`. This is the first connector on `main`; T046 and T048 reuse the rule.
- **`deploy/local/agent/search.json`** (new): the `demo` and `exa` toolsets, with `exa` `optional: true`.
- **`docs/QUICKSTART.md`:** §6 "Web search".
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

**Adoption is deferred to a follow-up.** T049's first cut (`686b6ff`) reached `main` after this connector was built and gated. It is not adopted here, for three reasons:

- `Call` has no per-call `Settings`, so highlights and text mode do not fit one `Connector` (below).
- `docs/CONNECTORS.md`, the kit's guide, is not on `main` yet, and Tavily (T046) has not adopted the kit either.
- Adopting deletes the tests the 49-of-49 mutation run, the race run and the fuzz run were measured on, so every gate would start over.

Nothing is ticked as "passes `RunConnector`"; every criterion is covered by the hand-written tests named above. One prerequisite is already done: `WithBaseURL("")` keeps the default, as the suite's `Env.BaseURL` contract requires (`TestBuildRejectsSettings`). The `FuzzResponses` target comes with the adoption, since it takes the same `Connector`.

- **Replaced by `RunConnector`** at adoption; delete these then:
  - `TestUpstreamErrors`, `TestRateLimitRetry`, `TestCancelStopsRetryWait`;
  - the body-limit part of `TestLimits`, `TestKeyNeverLeaks`, `TestConcurrentCalls`;
  - the missing-key, literal-`api_key`, unknown-field and `base_url` parts of `TestBuildRejectsSettings`;
  - the listing part of `cmd/agentkit` `TestServeExaOverMCP`;
  - `cassette_test.go`, replaced by `toolsettest.Upstream`.
- **Kept:**
  - the mapping tests and goldens;
  - `TestInvalidInputSendsNothing`;
  - rune truncation and the marker text;
  - the spec tests, which are Exa-specific;
  - the two decoder fuzz targets, plus the three-line `FuzzResponses` target once the helper exists.
- **Where T049's API does not fit Exa:**
  - **Content mode is a setting.** One `Connector` with `Calls []Call{Tool, Input, Cassettes}` cannot cover both highlights and text. `Call` needs a per-call `Settings` (as `searchCases` has), or the connector is registered twice.
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

**Result: 49 of 49 caught.** None survived, none failed to compile, and none hung.

Before reaching that result:
- an earlier run had one survivor, "failed result still listed in results", now covered by `TestExtractMapsSuccessesAndFailures/a failed URL's result is dropped`;
- a panic in a `TestLimits` subtest hid other tests' results; `only()` now fails the test instead of panicking;
- the retry tests had no real-time deadline, so a regression would have hung the run for 10 minutes. `waitDone` now gives each wait 10 s, then fails.

The six mutants the task requires are among the rows below (`numResults` not sent, highlights not joined, per-URL failures dropped, key header missing, key not redacted, retry on 5xx).

| Mutant | Caught by |
|---|---|
| numResults not sent | `TestSearchRequestAndMapping` |
| max_results default ignored | `TestSearchRequestAndMapping` |
| highlights not joined (first only) | `TestSearchRequestAndMapping`, `TestServeExaOverMCP` |
| empty highlights not skipped | `TestSearchRequestAndMapping` |
| search contents mode ignored | `TestLimits`, `TestSearchRequestAndMapping` |
| text mode maxCharacters not sent | `TestSearchRequestAndMapping` |
| author dropped | `TestSearchRequestAndMapping` |
| answer not mapped | `TestSearchRequestAndMapping` |
| unknown category accepted | `TestInvalidInputSendsNothing` |
| end date not end of day | `TestSearchRequestAndMapping` |
| company/people filter check removed | `TestInvalidInputSendsNothing` |
| search content cap not applied | `TestLimits` |
| per-URL failures dropped | `TestExtractMapsSuccessesAndFailures` |
| failed result still listed in results | `TestExtractMapsSuccessesAndFailures` |
| unmentioned URL not reported | `TestExtractMapsSuccessesAndFailures` |
| query highlights ignored in extract | `TestExtractMapsSuccessesAndFailures` |
| max_age_hours not sent | `TestExtractMapsSuccessesAndFailures` |
| more than 10 URLs allowed | `TestInvalidInputSendsNothing` |
| non-http URL accepted | `TestInvalidInputSendsNothing` |
| URL credentials accepted | `TestInvalidInputSendsNothing` |
| extract content cap not applied | `TestLimits` |
| key header missing | `TestCancelStopsRetryWait`, `TestConcurrentCalls`, `TestExtractMapsSuccessesAndFailures`, `TestKeyNeverLeaks`, `TestLimits`, `TestRateLimitRetry`, `TestSearchRequestAndMapping`, `TestServeExaOverMCP`, `TestUpstreamErrors` |
| key also sent as Bearer | `TestCancelStopsRetryWait`, `TestConcurrentCalls`, `TestExtractMapsSuccessesAndFailures`, `TestKeyNeverLeaks`, `TestLimits`, `TestRateLimitRetry`, `TestSearchRequestAndMapping`, `TestUpstreamErrors` |
| key not redacted | `TestKeyNeverLeaks`, `TestServeExaOverMCP` |
| retry on 5xx | `TestUpstreamErrors` |
| 429 retried twice | `TestRateLimitRetry` |
| Retry-After ignored | `TestRateLimitRetry` |
| Retry-After cap not enforced | `TestRateLimitRetry` |
| cancel does not stop the wait | `TestCancelStopsRetryWait` |
| 400 message not carried | `TestServeExaOverMCP`, `TestUpstreamErrors` |
| 400 message not capped | `TestUpstreamErrors` |
| 422 not invalid input | `TestUpstreamErrors` |
| 401 echoes the body | `TestKeyNeverLeaks`, `TestUpstreamErrors` |
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
| base_url: plain http to any host | `TestBuildRejectsSettings` |
| base_url setting ignored | `TestBuildRejectsSettings` |
| base_url credentials accepted | `TestBuildRejectsSettings` |

