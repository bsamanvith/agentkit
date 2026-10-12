---
id: T048
title: "connectors/parallel: search and extract, keyed"
epic: E10
status: todo
depends_on: [T026, T036]
---

# T048: `connectors/parallel`

## Context

Third search-and-extract connector, wanted by the owner and built in parallel with T046 (Tavily) and T047 (Exa). Parallel Web Systems serves `POST /v1/search` and `POST /v1/extract` on `https://api.parallel.ai`, with the `x-api-key` header. Decisions, shared output shape, limits and error rules: [E10](E10-search-connectors.md).

API sources (fetch with `curl`; WebFetch may fail on DNS in sessions):
- index: `https://docs.parallel.ai/llms.txt`;
- the OpenAPI spec `https://docs.parallel.ai/public-openapi.json`, the source of truth;
- guides: `https://docs.parallel.ai/search/search-quickstart.md`, `/search/modes.md`, `/search/advanced-search-settings.md`, `/search/source-policy.md`, `/extract/extract-quickstart.md`, `/extract/advanced-extract-settings.md`.

**Use `/v1/...` only.** The docs say `/v1beta` is for existing integrations, and that `/v1/extract` takes `full_content`, `fetch_policy` and `excerpt_settings` inside `advanced_settings`: sending them top-level returns 422.

## Scope

- **Factory:** `parallel.Factory(opts...) toolset.Factory` with `Type() == "parallel"`.
  - Options: `WithHTTPClient`, `WithBaseURL`, `WithLookupEnv`, `WithClock`, `WithLogger`.
- **Settings:**
  - `api_key_env`, default `PARALLEL_API_KEY`; required, and a missing variable fails `Build`;
  - `timeout`, default 30 s;
  - `max_content_chars`;
  - `search_defaults`: `mode`, default **`fast`** (Parallel's recommendation for agents; `turbo`, `basic`, `advanced` allowed), `max_chars_per_result`, `location`, `fetch_policy`;
  - `extract_defaults`: `full_content` (default false: excerpts only), `fetch_policy`.
  - Unknown fields are refused, `api_key` included.
- **`parallel_search`** inputs:
  - `query` (required; sent as the first of `search_queries`);
  - `objective` (optional natural-language goal; defaults to `query`; Parallel ranks and compresses by it);
  - `extra_queries` (0–4 more keyword queries);
  - `max_results` (1–20, default 5);
  - `include_domains` / `exclude_domains` and `after_date` (ISO date), mapped to `source_policy`;
  - `mode` only if settings allow the model to choose.

  Maps to E10's search shape: `content` is the excerpts joined with `\n…\n`; `published_date` comes from `publish_date`; `warnings` are logged, not returned.
- **`parallel_extract`:**
  - inputs: `urls` (1–10), optional `objective`;
  - `content` is `full_content` when enabled, else the joined excerpts;
  - `errors` become `failed`.
- **Wiring:**
  - add the factory to `cmd/agentkit/catalog.go`;
  - add `parallel` (`optional: true`) to `deploy/local/agent/search.json` (create it if T046/T047 have not);
  - add `PARALLEL_API_KEY` to the QUICKSTART search section.

## Out of scope

- Task/Deep Research, Responses, FindAll, Monitor, Image Search. Parallel's free anonymous tier: it exists only through their hosted MCP server (`https://search.parallel.ai/mcp`), not the REST API (anonymous REST calls get 401). We can reach it once `mcp/client` exists (an MCP toolset proxied through our server), not in this task. Per-tenant keys (T044).

## Acceptance criteria

**Test kit (added 2026-10-12, [T049](T049-connector-test-kit.md)).**
- `toolsettest.RunConnector` runs the generic checks: settings, missing key, the error matrix (400/401/429/5xx/garbage/oversized/hang), the retry rule, the canary, no network, MCP and concurrency. Criteria below that it covers are ticked by "passes `RunConnector`"; don't hand-write those tests.
- Write cassettes in T049's format.
- Accept a `base_url` setting (`https` unless loopback).
- Make no request in `Build`.
- Add the three-line `FuzzResponses` target.
- If T049 is not on `main` yet, build the connector and its specific tests first, then adopt the suite when it lands.


- [ ] **Search request:** sends the documented `/v1/search` JSON (golden request body: `objective`, `search_queries`, `mode`, result count and `source_policy` placed exactly where the spec puts them) with the `x-api-key` header; a recorded response maps to E10's search shape (golden output).
- [ ] **Extract:** sends `/v1/extract` with the extract options inside `advanced_settings` (golden; a test pins that nothing the docs moved is sent top-level); it maps `results` and `errors`, with excerpts mode and full-content mode both covered.
- [ ] **Settings validation:**
  - a missing `PARALLEL_API_KEY` fails `Build` naming the variable;
  - an `optional` toolset is skipped and shown degraded in `/readyz`;
  - a literal `api_key`, unknown fields and bad enums fail with clear messages.
- [ ] **Input validation:** an empty query, more than 4 extra queries, `max_results` out of range, a bad date, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent.
- [ ] **Errors:**
  - 400/422 give `ErrInvalidInput` with Parallel's message (capped);
  - 401/403 give "rejected the API key";
  - 429 retries once after `Retry-After` (fake clock), then fails;
  - 5xx gives the status only, with no retry;
  - a cancelled `ctx` stops the wait.
- [ ] **Limits:** truncation at a rune boundary with the marker; the body limit is enforced.
- [ ] **Canary:** the key appears in no tool output, error, log line or MCP response, including when the upstream echoes it.
- [ ] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeExtract` never panic.
- [ ] **Over MCP:** `/mcp/parallel` lists exactly `parallel_search` and `parallel_extract` with schemas, and a call through the MCP client against the fake upstream returns the mapped result.
- [ ] **Live (`-tags live`, needs `PARALLEL_API_KEY`):** a search returns results with URLs and excerpts, and extracting a known page returns content. If the key is not set, leave this unticked with `status: review`.
- [ ] **Mutation list** recorded (at least: `objective` dropped, extra queries not sent, extract options sent top-level, `errors` dropped, key header missing, key not redacted from an echoed error, retry on 5xx).
