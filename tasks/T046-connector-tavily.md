---
id: T046
title: "connectors/tavily: search and extract, keyless or keyed"
epic: E10
status: todo
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

- [ ] **Search request:** sends the documented JSON (golden request body) with settings defaults applied and model inputs validated; the response (recorded fixture) maps to E10's search shape exactly (golden output).
- [ ] **Extract** maps `results` and `failed_results`, with partial failures kept (golden).
- [ ] **Auth modes** (table test):
  - `auto` with the variable set sends the bearer header; `auto` without it sends the keyless header;
  - `key` without the variable fails `Build` with a message naming the variable;
  - `keyless` never sends `Authorization`, even when the variable is set;
  - never both headers.
- [ ] **Settings validation:** a literal `api_key` in settings, unknown fields and bad enums each fail `Build` with a clear message; an `optional` toolset that fails is skipped and shows in `/readyz` as degraded (existing mechanism).
- [ ] **Input validation:** an empty query, `max_results` out of range, an unsupported enum, a non-http(s) URL, or more than 10 URLs is `ErrInvalidInput`, and no request is sent.
- [ ] **Errors:**
  - 400 gives `ErrInvalidInput` with Tavily's message (capped);
  - 401 gives "rejected the API key";
  - 429 in keyless mode retries once after `Retry-After` (fake clock), then fails with the "set TAVILY_API_KEY" hint;
  - 500 gives the status only, with no retry;
  - a cancelled `ctx` stops a retry wait.
- [ ] **Limits:**
  - content over `max_content_chars` is cut at a rune boundary with the marker;
  - a response over the body limit fails cleanly;
  - total output stays bounded.
- [ ] **Canary:** a key value appears in no tool output, error, log line (debug included) or MCP response, including when the upstream echoes it in an error body.
- [ ] **Fuzzing:** `FuzzDecodeSearch` and `FuzzDecodeExtract` (hostile upstream bodies) never panic and always return the E10 shape or an error.
- [ ] **Over MCP:** `/mcp/tavily` lists exactly `tavily_search` and `tavily_extract` with input and output schemas, and a call through the MCP client against the fake upstream returns the mapped result.
- [ ] **Live (`-tags live`), keyless:**
  - a search for a current topic returns at least one result with a URL;
  - extracting `https://www.tavily.com` returns content;
  - with `TAVILY_API_KEY` set, the same works keyed.
- [ ] **Live end to end:** `agentkit serve` with `search.json` answers "What is the latest Go release? Cite the source." with a `tool_call` to `tavily_search` and a URL in the answer, against real Claude (`ANTHROPIC_KEY`).
- [ ] **Mutation list** recorded (at least: keyless header sent with a key, `failed_results` dropped, truncation splitting a rune, retry on 500, key not redacted from an echoed error, `max_results` default ignored).
