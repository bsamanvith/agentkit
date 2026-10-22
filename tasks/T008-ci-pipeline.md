---
id: T008
title: Full CI pipeline (images, integration, smoke)
epic: E01
status: review
depends_on: [T001, T006, T007]
---

# T008: Full CI pipeline (images, integration, smoke)

## Context

CI runs the same commands a contributor runs locally, nothing CI-only. The minimal `make check` workflow comes earlier, in T001; this task adds the jobs that need containers.

## Scope

GitHub Actions workflow(s):

- **On every PR** (parallel jobs):
  - `make test-integration` with `scripts/dev.sh up deps` and `AGENTKIT_REQUIRE_INTEGRATION=1`.
  - Build all images; fail if any is over the budget set in T005.
  - `scripts/dev.sh up allinone && scripts/dev.sh smoke`, then the same for `split`; dump `logs` on failure.
- **Manual / nightly**: `make test-live` with provider keys from repository secrets.
- Go build cache and module cache enabled.

## Out of scope

- Releases and image publishing.

## Acceptance criteria

- [x] A PR with a failing integration or smoke test, or an oversized image, is red on the matching job. *Each job's steps are make targets or scripts that exit non-zero on failure (`make image-size*`, `scripts/smoke.sh --images`, `go test -tags integration`, `make smoke`), and none sets `continue-on-error`. Shown on GitHub before this task: run 39712187214 (T060) red on `image` at `make image-size`; run 39709474719 (T065) red on `integration`; run 39710099344 (T071) red on `image` at the per-image smoke. `make smoke` moved from the `web` job to a `smoke` job of its own, same command.*
- [ ] A PR that only changes `tasks/` or `docs/` skips the image and smoke jobs. *The `changes` job runs `scripts/ci-changes.sh HEAD^1 HEAD` (the PR's merge commit against its base); `image` and `smoke` need it and skip only on `code=false`. The rule and the wiring are tested by `scripts/ci-changes.sh --test` (in `make lint`); the skip itself shows only on a docs-only PR on GitHub, not run here.*
- [x] Integration job fails (not skips) if a required env var is missing. *`testkit.IntegrationEnv` fails instead of skipping under `AGENTKIT_REQUIRE_INTEGRATION=1` (test: `TestIntegrationEnv`, internal/testkit), and `scripts/ci-changes.sh --test` fails if check.yml's `integration` job stops setting it.*
- [x] Whole PR pipeline finishes in under 10 minutes at skeleton level. *Measured on GitHub before this change: runs 39721253532 and 39718728738 took 6.1 and 7.5 minutes wall clock (longest job `check`; `image` 2.5, `web` 2.9). This change puts the `changes` job (a depth-2 checkout, seconds) in front of `image` and `smoke`, which finish well before `check`, and takes `make smoke` out of `web`. Re-check on this PR's run.*

## Notes

- **What already existed** (T001, T088, T089, T102, T078): `check`, `integration` (service containers for Postgres 17 and Redis 7, `AGENTKIT_REQUIRE_INTEGRATION=1`), `image` (every image and variant, each `make image-size*`, then `scripts/smoke.sh --images` on Postgres, which prints the containers' logs on failure) and `web` (which ran `make smoke`). There is no `scripts/dev.sh`: the integration tier uses service containers in CI and `make integration STORE=postgres` locally, and the smoke suite runs as processes (`make smoke`) and against the images (`--images`). This task adds only what the criteria lacked.
- **Paths filter** (`changes` job, `scripts/ci-changes.sh`). Not `on.pull_request.paths-ignore`: a workflow that does not run leaves required checks pending forever, while a job skipped by `if:` reports success. Rules: a change whose every path is under `docs/` or `tasks/` skips; anything else (root `*.md` too, `.github/`, `web/docs/`), an empty list, or a push to `main` runs everything. The jobs' `if` is `!cancelled() && needs.changes.outputs.code != 'false'`, so a failed `changes` job (empty output) runs them rather than skipping them green. Only `image` and `smoke` skip; `check`, `integration` and `web` always run (`make lint` checks task files and links).
- **Smoke job**: `make smoke` moved out of `web` into `smoke` (Go, plus Node for the console's fake model; no `pnpm install`), so it can skip, and it runs beside the browser smoke instead of after it.
- **Live** (`.github/workflows/live.yml`): nightly at 03:23 UTC and `workflow_dispatch`, never on a PR. It runs `make test-live LIVE_REQUIRED=1`: the new `LIVE_REQUIRED` variable makes a missing `ANTHROPIC_API_KEY` (an absent secret reads as empty) fail at once with a message and, in Actions, an `::error` annotation, rather than a green run whose tests all skipped. Optional secrets `OPENAI_API_KEY`, `VOYAGE_API_KEY`, `EXA_API_KEY`, `PARALLEL_API_KEY`, `TAVILY_API_KEY` are passed when they exist; their tests skip without them. Checked here: `make test-live LIVE_REQUIRED=1 ANTHROPIC_API_KEY= ANTHROPIC_KEY=` fails with the message; not run on GitHub.
- **Caches**: setup-go already caches the module and build caches (its default, keyed on go.sum) in every Go job. Added to `check`: `actions/cache` (v4.3.0, SHA-pinned) for the linters make builds into `bin/` and golangci-lint's analysis cache, keyed on Makefile, go.sum and .golangci.yml. No Docker layer cache for `image` (it needs buildx and the Actions cache token); a follow-up if `image` grows.
- `make workflow-lint` (actionlint, zizmor 1.30.1): no findings.
- **Mutation self-check** (each planted in a scratch copy; the named test failed): `docs*` without the slash (`--test`: "a docs/ prefix that is not the folder"); an empty list answering false ("nothing"); root `*.md` counted as docs ("a root file"); `needs: changes` removed from `image`; `smoke`'s `if` as `== 'true'`; `!cancelled() &&` dropped from `image`'s `if`; `AGENTKIT_REQUIRE_INTEGRATION` removed from `integration`; the `changes` job not running the script (these five by `--test`'s check.yml checks); `IntegrationEnv` skipping under `AGENTKIT_REQUIRE_INTEGRATION=1` (`TestIntegrationEnv`).

## For later tasks

- Branch protection: `changes` and `smoke` are new job names; add `smoke` to the required checks if the others are (a skipped job counts as passing).
- A task that adds a path the images and the smoke suite never read can widen `scripts/ci-changes.sh`'s rule, with a `--test` case.
