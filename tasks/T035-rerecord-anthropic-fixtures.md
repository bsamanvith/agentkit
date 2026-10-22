---
id: T035
title: Re-record the Anthropic adapter fixtures from the real API
epic: E08
status: done
depends_on: [T021]
---

# T035: Re-record Anthropic fixtures

## Context

T021's replay fixtures were hand-written to the documented SSE format because no key was available; its Notes ask to re-record them once one is. A key is available now, and T024 already recorded real responses for its e2e tests.

## Scope

- An env switch (same style as T024's e2e re-record switch) that runs each `llmtest.RunModel` scenario and the adapter's own scenarios against the real API and writes the fixtures.
- Fixtures keep no account headers, IDs that identify the account, or keys; a test checks the fixture files for them.
- Where a scenario cannot be produced by the real API on demand (for example a refusal, or a 529), keep the hand-written fixture and mark it as such in a README next to the fixtures.

## Acceptance criteria

- [x] Every fixture that the real API can produce is re-recorded; the rest are listed as hand-written, with the reason.
- [x] All adapter tests, `RunModel` and the fuzz targets pass on the recorded fixtures.
- [x] A test fails if any fixture contains an API key, an `anthropic-organization-id` header or similar account data.
- [x] Re-recording is one documented command.

## Notes

- Run with the key mapped to `ANTHROPIC_API_KEY` at the call site.

### Done (2026-10-22)

- `make record-anthropic` runs `TestRecorded` (`pkg/llm/anthropic/record_test.go`) with `AGENTKIT_ANTHROPIC_RECORD=1`: the replay server gets a record mode that forwards each request the adapter sends to the API (with the real key; the adapter only has a dummy one) and writes the response. The same checks run on the live replies and, afterwards, on the replays. A replay that serves a different number of fixtures than recorded fails (re-record).
- Recorded: `recorded/{text,tool_call,tool_call_no_input,max_tokens,thinking,tool_truncated,cache}` (the RunModel scenarios with real replies, plus summarized thinking sent back and accepted, a tool call cut off by `max_tokens`, and a prompt-cache write then read), and in place `conformance/error_auth`, `conformance/error_invalid_request`, `conformance/error_context_too_long` (the suite only checks the sentinel of error scenarios) and `errors/not_found`. The hand-written `tool_truncated.http` and `TestTruncatedToolCallIsDropped` are replaced by `TestRecorded/recorded/tool_truncated`.
- Hand-written, with the reason for each: `testdata/README.md`. The conformance success fixtures stay hand-written because `llmtest.RunModel` compares replies with its own constants.
- `TestFixturesHoldNoAccountData` walks `testdata/`: no `sk-ant-`, organization ID or header, `cf-ray`, real request ID (`req_` + 12 or more characters; recordings replace them with `req_replay`), or the key in the environment; and every `.http` has only the allowed headers.
- Recorded on the default model, Claude Haiku 5.5, with the "AgentKit Cloud" environment's key. Recording the lot costs a few cents; the context-too-long request (about 4.9 M tokens) is rejected before it is processed.

### Mutation check

| Planted bug | Caught by |
|---|---|
| Truncated tool call kept | `TestRecorded/recorded/tool_truncated` |
| An `Anthropic-Organization-Id` header in a fixture (what a recorder keeping every header writes) | `TestFixturesHoldNoAccountData` |
| A real request ID in a fixture | `TestFixturesHoldNoAccountData` |
| An API key pasted into a fixture | `TestFixturesHoldNoAccountData` |
| `cache_read_input_tokens` not mapped | `TestRecorded/recorded/cache` |
