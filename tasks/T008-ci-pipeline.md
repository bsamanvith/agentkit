---
id: T008
title: Full CI pipeline (images, integration, smoke)
epic: E01
status: done
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
- [x] A PR that only changes `tasks/` or `docs/` skips the image and smoke jobs. *The `changes` job runs `scripts/ci-changes.sh HEAD^1 HEAD` (the PR's merge commit against its base); `image` and `smoke` need it and skip only on `code=false`. The rule and the wiring are tested by `scripts/ci-changes.sh --test` (in `make lint`); shown on GitHub by PR #90 (T087, `docs/` and `tasks/` only), run 39732298170: `image` and `smoke` skipped, `check`, `integration` and `web` ran and passed.*
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

- **Review fixes** (PR #84):
  - Renames: `git diff --name-only` detects renames by default and lists only the new path, so a PR that moved a file from `pkg/` into `docs/` answered `code=false` and skipped `image` and `smoke`. The script now diffs with `--no-renames` (both paths listed). `--test` builds a scratch repository and checks it: a merge commit against its first parent while the base moved on with code (`false`), a rename from `pkg/` into `docs/` (`true`), a file deleted from `docs/` (`false`). Mutation: dropping `--no-renames` fails "a file renamed from pkg/ into docs/".
  - The wiring check now asks for `scripts/ci-changes.sh HEAD^1 HEAD` and `fetch-depth: 2` in the `changes` job, not just the script's name. Mutation: `HEAD^2`, and depth 1, each fail it.
  - Lint cache: the linters in `bin/` are built with go.mod's Go and make reuses them by file name, but the key left out go.mod and `restore-keys` restored any older entry, so after a Go bump the old binary (which refuses code that targets a newer Go) came back. Now two caches: `bin/` on an exact key of Makefile and go.mod with no fallback; golangci-lint's analysis cache (which keys its own entries) keeps a fallback. `--test` checks the `bin/` cache's key and that it has no `restore-keys` (mutations: each fails it).
  - Checked and left as is: a failed `changes` job leaves its output empty, and `!cancelled()` runs `image` and `smoke` (not skipped green); a push to `main` answers `true` without diffing; live.yml has only `schedule` and `workflow_dispatch`, `contents: read`, so no PR or fork run sees its secrets; `make check` and the `web` job lose nothing but `make smoke`, which `smoke` runs (the fake model uses only Node built-ins, so no `pnpm install`). `make workflow-lint`: no findings.

## For later tasks

- Branch protection: `changes` and `smoke` are new job names; add `smoke` to the required checks if the others are (a skipped job counts as passing).
- A task that adds a path the images and the smoke suite never read can widen `scripts/ci-changes.sh`'s rule, with a `--test` case.
