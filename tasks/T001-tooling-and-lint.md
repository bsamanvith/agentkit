---
id: T001
title: Toolchain, Makefile, lint, architecture test, minimal CI
epic: E01
status: review
depends_on: []
---

# T001: Toolchain, Makefile, lint, architecture test, minimal CI

## Context

One command (`make check`) must tell a contributor whether their change is acceptable, and CI must run that same command. Import rules from [ENGINEERING.md §1.4](../docs/ENGINEERING.md#14-dependency-direction) are enforced by a test, not by review.

## Scope

- `go.mod`: `go 1.26.0`, `toolchain go1.27.2`.
- `Makefile` targets: `build`, `test`, `test-integration`, `test-live`, `fuzz`, `lint`, `fmt`, `check` (= fmt-check + lint + test), `help`.
  - Tests run with `-race -shuffle=on`; tiers selected by build tags `integration`, `e2e`, `live`.
  - `fuzz` runs every `Fuzz*` target for `FUZZTIME` (default 10s).
- `.golangci.yml` (golangci-lint v2, version pinned in the Makefile and installed into `bin/`): default linters plus `errorlint`, `gocritic`, `revive` basics; `gofumpt` + `goimports` formatters.
- `internal/archtest`: the architecture test with the five rules in §1.4, including the per-package third-party allowlist.
- `.github/workflows/check.yml`: `make check` and `make fuzz` on every push and PR.

## Out of scope

- Images, Compose, smoke tests (T005–T008).

## Acceptance criteria

- [x] `make check` passes on a clean checkout with only Go installed (it fetches the pinned linter).
- [x] archtest fails for each rule when violated: `pkg` → `cmd` import, parent importing its own subpackage, non-test code importing a `*test` package, a third-party import not on the package's allowlist, a mocking framework in a test. One test per rule, run against synthetic import data.
- [x] archtest passes on the real repository.
- [x] `make test` does not compile files tagged `integration`, `e2e`, or `live`.
- [ ] CI runs `make check` and `make fuzz` and is red when either fails.

## Notes

- archtest rules verified by hand on the real repo as well as by the unit tests: a non-test import of `go-cmp` in `pkg/tool` fails `third-party-allowlist`; importing `internal/testkit` from `pkg/tool` fails `no-test-helpers-in-non-test-code`.
- Tag isolation verified: a non-compiling `//go:build integration` file is ignored by `go test` and breaks `-tags integration`.
- `fmt-check` verified to fail on unformatted code; `tidy-check` caught a real untidy `go.mod` on the first run.
- Not yet verified: the CI workflow (needs a PR to run). Kept open.
- golangci-lint is pinned to v2.14.0 and installed into `bin/` by the Makefile.
