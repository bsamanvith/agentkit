# agentkit

A Go kit for running Claude agents: one binary (`cmd/agentkit`), a web console
(`web/`), tasks in `tasks/`, docs in `docs/`. How we work is in
[docs/ENGINEERING.md](docs/ENGINEERING.md) and [docs/WORKFLOW.md](docs/WORKFLOW.md).
T010 fills in the rest of this file (commands, skills).

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
