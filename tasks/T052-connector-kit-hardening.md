---
id: T052
title: "toolsettest hardening: checks the connectors still test themselves, golden first run, fuzz speed"
epic: E10
status: todo
depends_on: [T049]
---

# T052: connector kit hardening

## Context

Once all three connectors had adopted T049's kit, their mutation runs (T046–T048, chiefly T048's handoff) found planted bugs that the suite lets through. The connectors still catch them with their own tests. Per E10 ("every generic bug becomes a suite check", added through the staged rollout), they move into the kit. Two rough edges of the kit itself come with them.

## Scope

- **New checks.** Each one is opt-in first, with its own check ID. Turn it on for Tavily, Exa and Parallel, then make it opt-out once all three pass.
  - `errors.no_retry_5xx`: a planted "retry on 5xx" must fail. If the current error matrix lets it pass, find out why (for example, a fresh upstream per case hiding the second request) and fix the counting.
  - `errors.retry_wait_ctx`: a `Retry-After` wait must end when `ctx` is cancelled (cancel during the wait, by the fake clock; it must return the context's error without waiting out the clock).
  - `errors.attempt_timeout`: a per-attempt timeout separate from `ctx`. A hanging upstream with a long `ctx` must end at the connector's configured timeout. The current hang check ends through `ctx`, so an ignored timeout passes.
  - `limits.rune_boundary`: truncated content is valid UTF-8 and ends with the steering marker, using a cassette body built with multi-byte characters at the cut point.
  - `settings.honored`: a connector declares settings whose effect shows in the request (for example Parallel's `mode`, Tavily's `search_depth`), each as a `Call` with per-call `Settings` and a golden request. A planted "setting ignored" must fail.
- **Golden first run:** with `AGENTKIT_UPDATE_GOLDEN=1` and no golden file yet, the suite writes the golden instead of failing because it read the file first.
- **Fuzz speed:** `FuzzResponses` builds the connector once per fuzz target, not on every input. Measure execs/s before and after, and record both in Notes.

## Acceptance criteria

- [ ] **New checks catch their bugs:** each check fails on a toy-connector variant planted with its bug, on that check's ID.
- [ ] **Connectors pass:** Tavily, Exa and Parallel pass all five checks, enabled through `Enable` first.
- [ ] **Rollout:** after the connectors pass, the checks are moved to opt-out and the stage table in `docs/CONNECTORS.md` is updated.
- [ ] **Golden first run:** a fresh golden is written when `AGENTKIT_UPDATE_GOLDEN=1` (a test with a temp dir).
- [ ] **Fuzz speed:** `FuzzResponses` builds the connector once per target, and execs/s before and after are recorded.
- [ ] **Mutation list** recorded (the five bugs above, plus "golden read before write").
