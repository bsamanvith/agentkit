# Handoff prompt template

Copy into a new session (or a suggested-task card) and fill in. Keep it self-contained: the session knows nothing but the repo. See [docs/WORKFLOW.md §3](../docs/WORKFLOW.md#3-handoffs).

```markdown
# Task: implement T0xx — <title>

Repo `bsamanvith/agentkit` (Go, one module). Commit and push straight to `main` once
`make check` exits 0 (gate the push on its exit status; do not pipe it through grep);
CI runs it again on every push. Other sessions push to `main` in parallel (<which>):
`git pull --rebase origin main` right before each push. Touch only <owned files>,
`tasks/T0xx-....md` and its line in `tasks/E0x-....md`. If go.mod or
internal/archtest/rules.go conflict on rebase, keep both sides, then `go mod tidy` and
`make check`. Do not edit README.md or packages you do not own; if one truly needs a
change, keep it minimal, keep its tests green, and record why in your task file.

## Why
<what this unblocks, two or three lines>

## Read first, in order
1. `tasks/T0xx-....md` (the spec) and the epic's decisions.
2. Notes of the tasks this builds on ("For T0xx" sections).
3. `go doc -all` of the packages used.
4. `docs/ENGINEERING.md` §1–§2 (incl. §2.6) and the `write-tests` skill; `claude-api` for anything Anthropic.

## Build
<design, with known traps>

## Tests
One per acceptance criterion; invariants after every test; <replay e2e / live / fuzz>.

## Credentials / environment
Claude sessions have the Anthropic key as `ANTHROPIC_KEY`; `make test-live` maps it. Tasks touching a
model adapter, provider wiring or the agent API run `make test-live` before done (there is no key in
CI). If a key is missing: leave the box unticked, status `review`, never fake a result.

## Definition of done
1. Every acceptance criterion ticked with test names (live/manual/Docker only if they ran).
2. `make check` exits 0; `go test -race -shuffle=on -count=20` on touched packages;
   `make fuzz FUZZTIME=60s` if fuzz targets exist.
3. Mutation list in Notes, every planted bug caught by a failing test (not a compile
   error). At least: <specific bugs>.
4. Fresh clone passes `make check`.
5. Task file status and Notes ("For T0yy"), epic line ticked.
6. Pushed to `origin main`; CI green on that commit (GitHub MCP tools; `gh` is not available).

## Report when finished
Public API, test → criterion list, mutation results, live/manual results, CI link,
what the next tasks need.
```
