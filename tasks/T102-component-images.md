---
id: T102
title: An image per component, a migrate image, and the all-in-one image
epic: E01
status: todo
depends_on: [T037]
---

# T102: An image per component, a migrate image, and the all-in-one image

## Context

Today there is one image, `agentkit`, holding every component, every CLI
command and `agentkit migrate`. A pod that serves only the console or only the
MCP server still ships the Anthropic SDK, the MCP SDK, pgx and SQLite, and
`migrate`, which runs once per release as a job, ships in every serving pod.
The owner asked (2026-10-14) for an image per component plus one with
everything, and for migrate to be its own image. Sizes and what each part
weighs are in [docs/SIZES.md](../docs/SIZES.md).

This replaces ENGINEERING.md §1's "deployment shape is config, not image
choice" for production images. `AGENTKIT_COMPONENTS` stays: it picks what the
all-in-one image runs.

It waits for T037, because T037 rewrites the code this changes: `serve.go`
(store selection, recovery), `migrate.go`, and a new `store.go`
(`AGENTKIT_STORE`, `AGENTKIT_STORE_MIGRATE=off`) on `claude/project-thread-xwz75u`.

## Scope

- Images, all from the one `Dockerfile` (its stages are the same for every
  image; only `BIN` and `TAGS` differ, and copies drift):

  | Image | Holds | Make target |
  |---|---|---|
  | `agentkit` | everything: the components, the CLI, migrate (today's image) | `image-all` |
  | `agentkit-agent` | the agent API and the store drivers | `image-agent` |
  | `agentkit-mcp` | the MCP server and its toolsets | `image-mcp` |
  | `agentkit-web` | the console | `image-web` |
  | `agentkit-migrate` | `migrate` only: pgx and the migrations | `image-migrate` |

- Each component image is a small `main` under `cmd/` that imports only its
  component's packages (ENGINEERING.md §1 already allows this: "a 20-line
  `main` that imports the components you want"). Wiring that today lives in
  `cmd/agentkit/serve.go` moves to packages both mains can import.
- `migrate` gets its own `cmd/agentkit-migrate`; `cmd/agentkit migrate` stays
  in the all-in-one binary for local use.
- A budget per image instead of the one `IMAGE_BUDGET_MB`, each set from a
  measurement with its row in docs/SIZES.md. `make images` builds all five;
  `make image-size` checks each against its own budget.
- Compose (T006) and the docs: a serving pod runs `AGENTKIT_STORE_MIGRATE=off`
  and the migrate image runs as a job or init container first.
- `agentkit-web` alone serves the console, which calls `/v1/...` on its own
  origin, so it needs a proxy that sends `/v1/` to the agent. Document that
  topology, or decide the console image isn't worth it and say why.

## Out of scope

- Build tags for optional backends. The Dockerfile's header advertises
  `TAGS=nopostgres`, but no Go file has that tag (checked 2026-10-14): fix the
  comment here; tags are for later.
- Multi-arch publishing and a registry (T008).

## Acceptance criteria

- [ ] `make images` builds the five images; each passes its own `make image-size` budget. — CI job
- [ ] `agentkit-agent`, `agentkit-mcp` and `agentkit-web` each start, report ready on `/readyz` for their component only, and serve it (`/v1/agents`, `/mcp/...`, `/`). — test: per-image smoke in CI
- [ ] The packages each component binary links match the plan: no Anthropic SDK or pgx in `agentkit-web` or `agentkit-mcp`, no MCP SDK in `agentkit-web` (`go list -deps`). — test: a script in `make check`
- [ ] `agentkit-migrate` migrates an empty Postgres and exits 0, and a second run exits 0 with nothing to do. — test: integration
- [ ] An `agentkit-agent` with `AGENTKIT_STORE_MIGRATE=off` against a migrated database serves runs. — test: integration
- [ ] docs/SIZES.md has a row for each new image, with its measured size.

## Notes

- 2026-10-14 (from T055): `go list -deps` shows `pkg/web` and `pkg/agentapi`
  link neither the Anthropic SDK, the MCP SDK nor pgx; `pkg/mcp/server`
  links the MCP SDK only. Where those come in is `cmd/agentkit`'s wiring,
  which is what the split has to untangle. Measure; don't predict sizes.
- Moving migrate out of the serving binary saves nothing once T037 lands:
  T037 links pgx (4.1 MB) and SQLite into `serve` for the store. The reason
  for the migrate image is operational (a release runs it once, before the
  pods), not size.
