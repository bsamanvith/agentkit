---
id: T047
title: "connectors/exa: search and extract (contents), keyed"
epic: E10
status: todo
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


- [ ] **Search request:** sends the documented JSON (golden request body: `query`, `numResults`, `type`, `contents`, filters) with the `x-api-key` header; the recorded response maps to E10's search shape (golden output), for both highlights and text modes.
- [ ] **Extract** maps successes and per-URL failures (`statuses`) (golden).
- [ ] **Settings validation:** a missing `EXA_API_KEY` fails `Build` naming the variable; an `optional` toolset is skipped and shown degraded in `/readyz`; a literal `api_key`, unknown fields and bad enums fail with clear messages.
- [ ] **Input validation:** an empty query, `max_results` out of range, a bad date, an unknown category, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent.
- [ ] **Errors:**
  - 400 gives `ErrInvalidInput` with Exa's message (capped);
  - 401/403 give "rejected the API key";
  - 429 retries once after `Retry-After` (fake clock), then fails;
  - 5xx gives the status only, with no retry;
  - a cancelled `ctx` stops the wait.
- [ ] **Limits:** truncation at a rune boundary with the marker; the body limit is enforced.
- [ ] **Canary:** the key appears in no tool output, error, log line or MCP response, including when the upstream echoes it.
- [ ] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeContents` never panic.
- [ ] **Over MCP:** `/mcp/exa` lists exactly `exa_search` and `exa_extract` with schemas, and a call through the MCP client against the fake upstream returns the mapped result.
- [ ] **Live (`-tags live`, needs `EXA_API_KEY`):** a search returns results with URLs and content, and extracting a known page returns its text. If the key is not set, leave this unticked with `status: review`.
- [ ] **Mutation list** recorded (at least: `numResults` not sent, highlights not joined, per-URL failures dropped, key header missing, key not redacted from an echoed error, retry on 5xx).
