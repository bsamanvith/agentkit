---
id: T001
title: Toolchain, Makefile, lint, import boundaries
epic: E01
status: todo
depends_on: []
---

# T001: Toolchain, Makefile, lint, import boundaries

## Context

One command (`make check`) must tell a contributor whether their change is acceptable. Import direction rules from [ENGINEERING.md §1.4](../docs/ENGINEERING.md#14-dependency-direction) are enforced by tooling, not review.

## Scope

- Bump `go.mod` to the current stable Go release; add `toolchain` line.
- `Makefile` targets: `build`, `test`, `test-integration`, `test-live`, `lint`, `fmt`, `check` (= fmt-check + lint + test), `help`.
- Test targets use `-race -shuffle=on`; tier selection by build tags `integration`, `e2e`, `live`.
- `.golangci.yml` with a small, agreed linter set (govet, staticcheck, errcheck, errorlint, gofumpt/goimports, revive basics, depguard).
- `depguard` rules:
  - `pkg/**` may not import `cmd/**`.
  - `pkg/harness` may not import any `pkg/llm/*` subpackage (interface only).
  - no mocking frameworks (`github.com/golang/mock`, `go.uber.org/mock`, `github.com/stretchr/testify/mock`).
- Pin `golangci-lint` version (run via `go run` or a versioned install script) so local and CI match.

## Out of scope

- CI workflow (T008).
- Package skeletons: packages are created when their feature lands.

## Acceptance criteria

- [ ] `make check` passes on a clean checkout with only Go installed (plus the pinned linter it fetches).
- [ ] A deliberate violation (e.g. a `pkg/` file importing `cmd/`) makes `make lint` fail — verified once by hand, recorded in Notes.
- [ ] `make test` does not compile files tagged `integration`, `e2e`, or `live`.
- [ ] `make help` lists every target with a one-line description.

## Notes

-
