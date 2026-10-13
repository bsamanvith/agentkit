---
id: T101
title: "CI hardening: pin actions by SHA, no persisted credentials, least permissions"
epic: E01
status: todo
depends_on: []
---

# T101: CI hardening

## Context

Left for later by T078's review (2026-10-13): the CI jobs use actions by tag (`actions/checkout@v4`), so a moved tag changes what runs, and `checkout` leaves its token in `.git/config` for every later step. All jobs should move together, so this is one task, not a change per job.

## Scope

- **Pin every `uses:` to a full commit SHA**, with the tag in a comment (`actions/checkout@<sha> # v4.2.2`). Add Dependabot (or Renovate) for `github-actions` so pins get update PRs. The owner's push-to-`main` rule applies to our own work; bot PRs are reviewed and merged by the owner.
- Set `persist-credentials: false` on every `actions/checkout`.
- Set `permissions: contents: read` at the top (already there), and nothing wider per job unless a job needs it (record why).
- **A lint for workflows:** `actionlint` and `zizmor` run in `make check` when installed, or in a small CI step. They fail on unpinned actions, template injection and excessive permissions.

## Acceptance criteria

- [ ] **Pinned:** every `uses:` in `.github/workflows/` is a 40-character SHA; the lint fails on a tag (tried and recorded).
- [ ] **No persisted credentials:** `.git/config` in a CI job holds no token after checkout (a step checks it).
- [ ] **Unchanged results:** all jobs (check, fuzz, image, web, integration) are green, with the same jobs required as before.

## References

- [GitHub: security hardening for Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions) (pin to a full SHA)
- [zizmor](https://docs.zizmor.sh/) and [actionlint](https://github.com/rhysd/actionlint)
- T078 Notes, "Review (2026-10-13)", "Left for later"
