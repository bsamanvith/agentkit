---
id: T034
title: Nightly perf report in CI; live tests run in sessions
epic: E01
status: todo
depends_on: [T001]
---

# T034: Nightly perf report; live tests in sessions

## Context

**Owner decision (2026-10-12): no Anthropic key in GitHub.** Live tests therefore never run in CI. They run in Claude sessions, where the key is set as `ANTHROPIC_KEY`, and `make test-live` maps it. Any task that touches a model adapter, the harness's provider wiring or the agent API runs `make test-live` before it is done (this is in the handoff template). Replay tests (recorded real responses) keep covering the provider paths in CI. Benchmarks exist (for example `BenchmarkCallTool`) but have no history, so ENGINEERING §2.7 has no numbers to set a budget from.

## Scope

- `.github/workflows/nightly.yml`, on a schedule plus manual dispatch:
  - **perf report**: run the benchmarks with `-count` and compare against the previous night's results with `benchstat`. Publish the comparison as a job summary and artifact. **Report only, not gating**, until two weeks of numbers exist; then set budgets in ENGINEERING §2.7 and make regressions fail.
- Document in README how to run live tests (`make test-live`, with `ANTHROPIC_API_KEY` or `ANTHROPIC_KEY` set).

## Acceptance criteria

- [ ] A manual dispatch runs the perf job; its summary shows a benchstat comparison from its second run onwards.
- [ ] No job needs a secret; the workflow passes on a fork.
- [ ] `make test-live` runs with only `ANTHROPIC_KEY` set (checked in a Claude session).

## Notes

- 2026-10-12 (planner): `make test-live` with only `ANTHROPIC_KEY` set ran and passed every live test (`TestLiveRunUsesTool`, `TestLiveServeAgent`, `TestLiveMixedToolResultsAndText`, `TestLiveToolCall`). The third criterion is met; tick it when the task is done.

- Image-size budget is not here: it is set in T005 once the first image is measured.
