---
id: T035
title: Re-record the Anthropic adapter fixtures from the real API
epic: E08
status: todo
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

- [ ] Every fixture that the real API can produce is re-recorded; the rest are listed as hand-written, with the reason.
- [ ] All adapter tests, `RunModel` and the fuzz targets pass on the recorded fixtures.
- [ ] A test fails if any fixture contains an API key, an `anthropic-organization-id` header or similar account data.
- [ ] Re-recording is one documented command.

## Notes

- Run with the key mapped to `ANTHROPIC_API_KEY` at the call site.
