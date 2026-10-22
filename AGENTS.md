# agentkit

A Go kit for running Claude agents: one binary (`cmd/agentkit`), a web console
(`web/`), tasks in `tasks/`, docs in `docs/`. How we work is in
[docs/ENGINEERING.md](docs/ENGINEERING.md) and [docs/WORKFLOW.md](docs/WORKFLOW.md).

## Commands

- `make check`: what every change passes (tidy, format, lint, deps, unit
  tests with `-race`). Gate on its exit status: `make check > log 2>&1; echo $?`.
- `make lint`: golangci-lint, then `scripts/lint-skills.sh` (skills, links,
  task files; `--test` runs it on its fixtures).
- `make test`; one package: `go test -race -shuffle=on -count=20 ./pkg/<p>/...`.
- `make integration` (SQLite) or `make integration STORE=postgres`; `make smoke`
  (the split deployment as processes, fake model); `make fuzz`; `make golden`;
  `make eval` (tool-use evals on real Claude, `evals/`); `make record-anthropic`.
- `make web-check` for the console; `make image` and `make image-size`.
- Run it: `scripts/try-agent.sh` (one process, real model), `make k8s-up`
  (Docker Desktop's Kubernetes). The `local-env` skill picks one.

## Where things are

- [docs/PLAN.md](docs/PLAN.md) what we build; [ENGINEERING.md](docs/ENGINEERING.md)
  module design (§1), tests (§2), environments (§3), skills (§4);
  [COMPONENTS.md](docs/COMPONENTS.md), [CONNECTORS.md](docs/CONNECTORS.md).
- [tasks/](tasks/README.md): one file per task, epics, [STATUS.md](tasks/STATUS.md).
- `pkg/` the library, `cmd/` binaries, `connectors/`, `internal/archtest` (import rules).

## Skills

In `.agents/skills/` (Claude Code reads them through `.claude/skills`; rules and
template in [the README](.agents/skills/README.md)). Idea to merge:

| Skill | Use it to |
|---|---|
| `plan` | Turn an idea into an epic and task files with testable criteria |
| `design-module` | Shape a new package: layer, interfaces, `xxxtest`, component wiring |
| `write-tests` | Map criteria to tests, check invariants, run the mutation self-check |
| `write-connector` | Write, test and run a connector or a provider family |
| `implement-task` | Take a task file from `todo` to `review` |
| `review` | Review a diff against our rules and test its tests |
| `local-env` | Run the change in a real deployment, smoke it, read logs |

## Seams

Store, bus, model, turn loop, run orchestration, tools and transport are
each an interface with a conformance suite and one place that picks the
implementation ([docs/COMPONENTS.md](docs/COMPONENTS.md#plugging-in-another-implementation)).
A new implementation passes the suite and adds a row to that table's
picker; it never adds a switch elsewhere or an `init()` registration.

## Sizes

Image size is tracked, and every change to it is explained.

- A change that moves an image by 100 KB or more (`make image-size`) adds a
  row to [docs/SIZES.md](docs/SIZES.md) in the same PR: date, PR, image, the
  new size, the change, and why. Measure before and after; don't estimate.
- A new Go dependency, or a new import of a heavy one into another binary,
  counts even when you haven't built the image: build the binary with and
  without it (docs/SIZES.md, "How to measure") and log the difference.
- Crossing a budget (`IMAGE_BUDGET_MB` in the Makefile) needs the reason it is
  worth it, or what gets cut first, in the same row. Don't raise a budget
  without a row that says why.
- The console has its own first-load budget (`web/scripts/check-size.sh`);
  the image log records what it adds to the image.

## Cloud sessions

Claude Code cloud sessions have no Docker. `.claude/hooks/session-start.sh`
prepares one (Go modules, the linter, the web workspace, Postgres and Redis
binaries), so `make check`, `make web-check` and these run as on a laptop:

- `make integration`: the agent API's suite on SQLite and the smoke suite
  (the split deployment as processes, fake model). Seconds.
- `make integration STORE=postgres`: CI's integration tier against a
  throwaway Postgres and Redis run as processes, then the smoke suite on
  that Postgres. A few minutes.
- Either one also runs the live tests against the real model when
  `ANTHROPIC_API_KEY` is set: add it as an environment variable of the
  project's cloud environment.
