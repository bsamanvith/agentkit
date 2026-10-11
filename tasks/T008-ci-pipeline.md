---
id: T008
title: Full CI pipeline (images, integration, smoke)
epic: E01
status: todo
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

- [ ] A PR with a failing integration or smoke test, or an oversized image, is red on the matching job.
- [ ] A PR that only changes `tasks/` or `docs/` skips the image and smoke jobs.
- [ ] Integration job fails (not skips) if a required env var is missing.
- [ ] Whole PR pipeline finishes in under 10 minutes at skeleton level.

## Notes

-
