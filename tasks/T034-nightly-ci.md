---
id: T034
title: Nightly perf report in CI; live tests run in sessions
epic: E01
status: review
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

- [ ] A manual dispatch runs the perf job; its summary shows a benchstat comparison from its second run onwards. *(Not run: GitHub dispatches only a workflow that is on the default branch, and this session has no Actions access. Dry run in Notes; check it after merge.)*
- [x] No job needs a secret; the workflow passes on a fork. *(`scripts/perf-report.sh --test`, in `make lint`: no `secrets.` and no `write` permission; the job's token reads the repository and its Actions artifacts only, so a fork's own dispatch runs it.)*
- [x] `make test-live` runs with only `ANTHROPIC_KEY` set (checked in a Claude session). *(Planner, 2026-10-12; the Makefile still maps `ANTHROPIC_KEY` to `ANTHROPIC_API_KEY`.)*

## Notes

- 2026-10-12 (planner): `make test-live` with only `ANTHROPIC_KEY` set ran and passed every live test (`TestLiveRunUsesTool`, `TestLiveServeAgent`, `TestLiveMixedToolResultsAndText`, `TestLiveToolCall`). The third criterion is met; tick it when the task is done.

- Image-size budget is not here: it is set in T005 once the first image is measured.

- 2026-10-22 (T034 session): built.
  - `.github/workflows/nightly.yml`, job `perf`: daily at 04:41 UTC and on dispatch. It finds the newest *successful* `nightly` run on the default branch (`gh run list -s success`, so never itself or a broken night), downloads its `perf` artifact, runs `scripts/perf-report.sh`, appends `summary.md` to the job summary and uploads `bench.txt`, `benchstat.txt` and `summary.md` as this run's `perf` artifact (30 days). No baseline (first run, an expired artifact, gh failing) is a notice and a report of tonight's numbers alone, never a failure.
  - `scripts/perf-report.sh` (`make perf`): `go test -run '^$' -bench . -benchmem -count 6 -p 1` over every package with a `Benchmark*` (found by `git grep`, so a new one joins by existing), then `benchstat previous=BASELINE tonight=bench.txt`. It exits non-zero only when a benchmark cannot run or benchstat cannot read the numbers, after writing the summary, which then shows the failure. `--test` (in `make lint`) runs it on stub `go` and `benchstat` against `scripts/testdata/perf-report/` and checks nightly.yml's wiring.
- Decisions:
  - The baseline is the default branch's last successful run, also for a dispatch from another branch: that compares the branch with main, which is what such a dispatch is for.
  - `-count 6` (benchstat's minimum for a confidence interval), `-benchtime 1s`, `-p 1` so packages never share the CPU while measured. One count took ~3.5 minutes on a loaded session machine (`pkg/runner` is 110 s of it), so a night is ~20 minutes; timeout 120.
  - benchstat is pinned to `golang.org/x/perf` `v0.0.0-20250813145418-2f7363a06fe1` in the script, through `go run pkg@version`: no go.mod dependency, nothing in any image.
  - Artifacts are kept 30 days: the next night needs one; two weeks of them are the history ENGINEERING §2.7's budgets get set from.
  - `pkg/store/postgres`'s benchmark skips without a database; the nightly runs no services, so it is not measured.
- Dry run (this session): `PKGS="./pkg/mcp/server ./pkg/bus/memory" BENCHTIME=100x COUNT=6 scripts/perf-report.sh` twice, the second with `BASELINE=` the first's `bench.txt`, with the real benchstat: the first summary says "No baseline" and shows tonight's table; the second shows the `previous` / `tonight` comparison with p-values. The baseline step's shell ran against a fake `gh` in its three cases (no earlier run; a run with the artifact; a run whose artifact is gone). actionlint and zizmor pass (`make workflow-lint`).
- Mutation self-check (`scripts/perf-report.sh --test`), each killed: `-p 1` dropped; baseline ignored; empty baseline used as one; go test failure exits 0; failure left out of the summary; benchstat failure exits 0; table left out of the summary; a `+N%` in the table fails the run (report-only broken); package list taken from every test file; nightly.yml with a `secrets.` token, an `actions: write` permission, no `GITHUB_STEP_SUMMARY`, the artifact renamed, no `workflow_dispatch`.
- Found, not fixed (outside this task): `live.yml` (T008) runs `make test-live LIVE_REQUIRED=1` nightly with `secrets.ANTHROPIC_API_KEY`. With no key in GitHub (owner decision) it fails every night rather than skipping.

## For the task that sets perf budgets (after two weeks of nights)

- The numbers are each night's `perf` artifact, `bench.txt` (raw `go test` output; benchstat reads any set of them). Set budgets in ENGINEERING §2.7 from their spread, then make `scripts/perf-report.sh` fail on a marked regression past a budget. Its `--test` case "compare" asserts today that a `+50%` does not fail, and changes with it.
