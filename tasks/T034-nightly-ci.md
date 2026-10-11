---
id: T034
title: Nightly CI: live tests and a perf report
epic: E01
status: todo
depends_on: [T001]
---

# T034: Nightly CI

## Context

`make test-live` only runs by hand, so a provider change can break the adapter unnoticed. Benchmarks exist (for example `BenchmarkCallTool`) but have no history, so ENGINEERING §2.7 has no numbers to set a budget from.

## Scope

- `.github/workflows/nightly.yml`, on a schedule plus manual dispatch:
  - **live**: `make test-live` with the `ANTHROPIC_API_KEY` repository secret. Skips with a clear notice (not a failure) if the secret is missing.
  - **perf report**: run the benchmarks with `-count` and compare against the previous night's results with `benchstat`. Publish the comparison as a job summary and artifact. **Report only, not gating**, until two weeks of numbers exist; then set budgets in ENGINEERING §2.7 and make regressions fail.
- Document in README how to run live tests locally (`ANTHROPIC_API_KEY=... make test-live`). Some environments name the variable differently; map it at the call site, never in code.

## Needs from the repository owner

- Add the `ANTHROPIC_API_KEY` repository secret (Settings → Secrets and variables → Actions).

## Acceptance criteria

- [ ] A manual dispatch runs both jobs; the live job passes with the secret and skips cleanly without it.
- [ ] The perf job's summary shows a benchstat comparison from its second run onwards.
- [ ] A failing live test makes the nightly run red and is visible on the Actions page.

## Notes

- Image-size budget is not here: it is set in T005 once the first image is measured.
