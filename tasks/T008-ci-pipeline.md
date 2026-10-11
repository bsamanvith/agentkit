---
id: T008
title: CI pipeline
epic: E01
status: todo
depends_on: [T001, T006, T007]
---

# T008: CI pipeline

## Context

CI runs the same commands a contributor runs locally, nothing CI-only.

## Scope

GitHub Actions workflow(s):

- **On every PR** (parallel jobs):
  - `make check` (lint + unit tests, `-race -shuffle=on -count=1`).
  - `make test-integration` with `scripts/dev.sh up deps` and `AGENTKIT_REQUIRE_INTEGRATION=1`.
  - Build all images; fail if any is over 20 MB.
  - `scripts/dev.sh up allinone && scripts/dev.sh smoke`, then the same for `split`; dump `logs` on failure.
- **Manual / nightly**: `make test-live` with provider keys from repository secrets.
- Go build cache and module cache enabled.

## Out of scope

- Releases and image publishing.

## Acceptance criteria

- [ ] A PR with a failing unit test, a lint error, or a failing smoke test is red on the matching job.
- [ ] A PR that only changes `tasks/` or `docs/` skips the image and smoke jobs.
- [ ] Integration job fails (not skips) if a required env var is missing.
- [ ] Whole PR pipeline finishes in under 10 minutes at skeleton level.

## Notes

-
