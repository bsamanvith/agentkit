---
id: T101
title: "CI hardening: pin actions by SHA, no persisted credentials, least permissions"
epic: E01
status: review
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

- [x] **Pinned:** every `uses:` in `.github/workflows/` is a 40-character SHA; the lint fails on a tag (tried and recorded).
- [ ] **No persisted credentials:** `.git/config` in a CI job holds no token after checkout (a step checks it).
- [ ] **Unchanged results:** all jobs (check, fuzz, image, web, integration) are green, with the same jobs required as before.

## References

- [GitHub: security hardening for Actions](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions) (pin to a full SHA)
- [zizmor](https://docs.zizmor.sh/) and [actionlint](https://github.com/rhysd/actionlint)
- T078 Notes, "Review (2026-10-13)", "Left for later"

## Notes

**Status: review (2026-10-21).** Everything is in place and checked locally; the two criteria about CI itself stay unticked until the PR's CI runs (this session cannot run GitHub Actions). On the PR, check: the four "the checkout left no credentials" steps pass, the `check` job's log shows zizmor ran ("No findings to report"), and check, integration, image and web are green. (There is no separate fuzz job: the seed corpus runs in `make check`.) Job names are unchanged, so the required checks stay the same.

What changed:

- **Pins.** Every `uses:` is a 40-character SHA with its release in a comment, each pinned to the commit its floating tag pointed to on 2026-10-21, so CI runs exactly what it ran before: checkout v4.4.0, setup-go v5.6.0, pnpm/action-setup v4.3.0 (`v4` pointed there, not at v4.4.0), setup-node v4.4.0, upload-artifact v4.6.2. SHAs from `git ls-remote --tags` (annotated tags dereferenced with `^{}`).
- **Dependabot** (`.github/dependabot.yml`): `github-actions`, weekly, grouped into one PR, with a 7-day cooldown so a bad release has time to be pulled first. Its PRs update the SHA and the comment; the owner reviews and merges them.
- **No persisted credentials.** `persist-credentials: false` on all four checkouts, and right after each one `scripts/check-no-git-credentials.sh` fails if git's config holds an `http.*.extraheader`, an `includeIf` (where checkout v6 puts it), or a value with an http(s) URL carrying a user, an `authorization:` header or `x-access-token`. It prints names, never values. Checked locally by planting each through `GIT_CONFIG_COUNT` (no file changed): extraheader, a token URL and an includeIf each exit 1; the clean repo exits 0. Nothing after checkout needs the token (no push, no gh call).
- **Permissions.** `contents: read` at the top, unchanged; no job widens it.
- **Workflow lint** (`make workflow-lint`, part of `make check`): actionlint v1.7.12, built into `bin/` like golangci-lint, so it always runs; zizmor 1.30.1 through `scripts/zizmor.sh`, which uses a zizmor of that version on PATH, else `uv tool run`, else skips with a message. CI's `check` job installs it with pipx and sets `ZIZMOR_REQUIRED=1`, so there a missing zizmor fails instead of skipping. zizmor runs `--offline` (its online audits need a token) with `.github/zizmor.yml`, which sets `unpinned-uses` to hash-pin for every action, GitHub's own included. Versions picked at least two weeks old (zizmor 1.31.x was six days old).

**Tried and recorded** (each made `make workflow-lint` fail, then was reverted):

1. `actions/upload-artifact@v4` instead of the SHA: zizmor `error[unpinned-uses]`, exit 2. Before this change the same lint found 10 unpinned uses and 4 `artipacked`.
2. `persist-credentials: false` removed from one checkout: zizmor `warning[artipacked]`, exit non-zero.
3. `permissions: write-all` on a job: zizmor `error[excessive-permissions]`.
4. `run: echo "${{ github.event.pull_request.title }}"`: actionlint `[expression]` (untrusted input), and zizmor `error[template-injection]`.
5. zizmor missing with `ZIZMOR_REQUIRED=1`: exit 1; without it: skip, exit 0.

### For later tasks

- A new workflow or job: pin by SHA with the tag comment, `persist-credentials: false` plus the credential-check step after checkout, and a job-level `permissions:` block only with a comment saying why. `make workflow-lint` catches all three if forgotten (the step itself excepted).
- Bumping the linters: `ACTIONLINT_VERSION` / `ZIZMOR_VERSION` in the Makefile; CI reads the zizmor version from there (`make print-zizmor-version`).
