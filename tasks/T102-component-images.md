---
id: T102
title: Split images (API, runner, toolsets) on a generic RPC, and the all-in-one image
epic: E01
status: review
depends_on: [T037]
---

# T102: Split images (API, runner, toolsets) on a generic RPC, and the all-in-one image

## Rescoped 2026-10-16

The owner asked (2026-10-16) for three components, each runnable alone or
bundled with the others: a light API image (agent API and console), a light
runner image (the agents' turn loops, which can also run inside the API), and
a toolsets image (the connectors, alone or bundled). Links between them are
interfaces with an in-process and a wire implementation, on one generic
transport that makes adding methods and services cheap.

Done on `claude/t102-component-split-5azj34`:

- `pkg/rpc`: JSON over HTTP, unary and streaming methods declared once as
  typed values (`rpc.Unary`, `rpc.Stream`), errors that keep their identity
  across the wire (`rpc.Code`), a bearer token, and `rpc.Pipe` in process.
- `pkg/tool/toolrpc` (`tool.Registry` remote; the tool conformance suite
  passes over it) and `pkg/runner/runnerrpc` (`runner.Agent` remote; same
  events and errors as a local run, `*harness.RunError` partials included).
- Components `runner` and `toolsets`; `AGENTKIT_RUNNER_URL`,
  `AGENTKIT_TOOLSETS_URL`, `AGENTKIT_RPC_TOKEN`, `AGENTKIT_DEPENDENCY_WAIT`.
- Tags `nomodels`, `noconnectors`, `nomcp`, `noweb`; images `agentkit-api`,
  `agentkit-runner`, `agentkit-toolsets` (`make images-split`), a budget each,
  `make deps-check` in `make check`.
- docs/COMPONENTS.md, rows in docs/SIZES.md, `deploy/k8s/split.yaml`.

Left from the original scope: the migrate image (an `agentkit-api migrate`
job covers it for now) and a separate console image (the console stays in
the API image, which serves `/v1` on the same origin). A `noapi` tag would
take `pkg/agentapi` out of the runner and toolsets images.

## Original scope (2026-10-14)

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

Reworded 2026-10-21 to the image set the owner chose (see Notes): the
all-in-one `agentkit`, `agentkit-local` and `agentkit-dev`, and one image per
component, `agentkit-api`, `agentkit-runner` and `agentkit-toolsets`; no
migrate image (the api image runs `migrate` as a job or init container).

- [ ] `make image`, `make image-local`, `make image-dev` and `make images-split` build the six images; each passes its own budget (`make image-size`, `make image-size-<variant>`); and the split images, run as containers, pass the per-image smoke on Postgres (`scripts/smoke.sh --images`: the checks of the next three criteria, against the images). — CI job `image` (check.yml). Not run here: no Docker in this session.
- [x] `agentkit-api`, `agentkit-runner` and `agentkit-toolsets` (binaries built with each image's tags, wired as deploy/k8s wires them) each start, report ready on `/readyz` for their own components only (`agent,web`; `runner`; `toolsets,mcp-server`), serve their surface (`/v1/agents` and `/`; `/rpc/runner/`; `/rpc/toolsets/` and `/mcp/demo`) and answer 404 for the others'. — test: `TestComponents` (test/e2e/components_test.go), run by `make smoke` (CI's web job) and `make integration`
- [x] The packages each image's binary links match its tags: api links no model SDK (Anthropic, openaicompat), MCP SDK, SQLite, connectors or skills; runner no MCP SDK, pgx, SQLite, go-redis or connectors; toolsets no model SDK, pgx, SQLite, go-redis or skills; local no pgx or go-redis; dev no SQLite (`go list -deps`). — test: `scripts/check-deps.sh` (`make deps-check`, in `make check`)
- [x] `agentkit migrate` from the image that holds the store (`agentkit-api`) migrates an empty Postgres and exits 0, and a second run exits 0 with nothing to do (schema and `schema_migrations` unchanged). — test: `scripts/migrate-check.sh`, run by `scripts/smoke.sh` on Postgres (`make integration STORE=postgres`; CI's image job against the image); also `TestMigrateCommand` (CI integration job)
- [x] An `agentkit-api` with `AGENTKIT_STORE_MIGRATE=off` against the database it migrated serves runs (`TestChat`: a run with a tool call, kept by the store). — test: `scripts/smoke.sh` on Postgres (`make integration STORE=postgres`; CI's image job against the image)
- [x] docs/SIZES.md has a row for each new image, with its measured size. — `agentkit-local`, `agentkit-dev` (2026-10-14, T090) and `agentkit-api`, `agentkit-runner`, `agentkit-toolsets` (2026-10-16, #26), each measured with `make image-size`

## Notes

- 2026-10-21 (reconciliation, `claude/project-thread-trypa2-t102`): status
  stays `review` because the first criterion needs `docker build`, which only
  CI's `image` job has; it now also runs the per-image smoke against the
  images on a Postgres service.
  - Image set: the owner chose one image per component plus the all-in-one
    (and local and dev for the stores), migrate not baked in as its own image
    and not run by serving pods, SQLite only in the local image. That is what
    main has: one Dockerfile whose `BIN` and `TAGS` pick the image (the
    original scope's reason, copies drift, still holds), variants by build
    tag. The criteria above were reworded from `agentkit-agent/-mcp/-web/
    -migrate` to it. No `agentkit-web`: the console stays in the API image,
    which serves `/v1` on the same origin (Rescoped section). No
    `agentkit-migrate`: pgx is in the API image anyway (the store), so a
    migrate image saves nothing; the release runs `agentkit-api migrate`
    (deploy/k8s's init container) and serves with `AGENTKIT_STORE_MIGRATE=off`.
  - Already covered on main and ticked with their tests: the dependency
    script (`make deps-check`, built in #26) and the SIZES rows.
  - Built here: `TestComponents` (per-component readiness and surfaces; it
    skips when the runner and toolsets URLs are not set, as under
    `make k8s-smoke`, where the cluster does not expose them);
    `scripts/migrate-check.sh` (empty database required, two runs, a
    fingerprint of the columns and of `schema_migrations` with `applied_at`
    must not move); `scripts/smoke.sh --images` (the same suite against the
    images on the host's network, `docker run`, config mounted at the image's
    default path); smoke.sh on Postgres now runs migrate-check instead of one
    bare `migrate`. CI's `image` job got a Postgres service, Go and Node, and
    runs `scripts/smoke.sh --images` on it.
  - Defaults chosen: the per-image smoke reuses the smoke suite and its fake
    model rather than a second harness; the images run with `--network host`
    (Linux CI runners) so the script's URLs are the same in both modes;
    `IMAGE_TAG` defaults to `latest`, as `make images-split` tags it.
  - Checked here: `make smoke` (memory) and `scripts/smoke.sh` on a
    throwaway Postgres pass; `--images` was run through a fake `docker` that
    starts the tagged binaries (argument plumbing only, on memory); the
    images themselves are left to CI.
  - Mutation self-check (each made a test fail, then reverted):
    1. toolsets started without `mcp-server`: `TestComponents/toolsets`
       fails (readyz list, `/mcp/demo` 404).
    2. api started without `web`: `TestComponents/api` fails (readyz, `/`).
    3. `TAGS_api` without `nomodels`: `make deps-check` fails (openaicompat,
       Anthropic SDK, skill linked).
    4. postgres migrate bumps `applied_at` on every run: migrate-check fails
       ("second run changed the schema or schema_migrations").
    5. `agentkit migrate` returns before migrating: migrate-check fails
       ("first run exited 0 but applied no migration").
    6. the schema check behind `AGENTKIT_STORE_MIGRATE=off` refuses a
       current schema: the API never gets ready and smoke fails.
  - No image content changed (test, scripts, workflow and docs only), so no
    SIZES row.

- 2026-10-14 (from T055): `go list -deps` shows `pkg/web` and `pkg/agentapi`
  link neither the Anthropic SDK, the MCP SDK nor pgx; `pkg/mcp/server`
  links the MCP SDK only. Where those come in is `cmd/agentkit`'s wiring,
  which is what the split has to untangle. Measure; don't predict sizes.
- Moving migrate out of the serving binary saves nothing once T037 lands:
  T037 links pgx (4.1 MB) and SQLite into `serve` for the store. The reason
  for the migrate image is operational (a release runs it once, before the
  pods), not size.
- 2026-10-14 (from T090, bsamanvith/agentkit#13): the owner asked for the
  local and the deployed image to stop carrying each other's store. Build
  tags `nosqlite`, `nopostgres`, `noredis` now pick serve's backends
  (`cmd/agentkit/backends.go`), and `make image-local` (SQLite, 24.3 MB) and
  `make image-dev` (Postgres and Redis, 26.1 MB) build the two, each with its
  budget; `make image` stays the all-in-one (30.4 MB). The component split
  here builds on those tags, so "Build tags for optional backends" is no
  longer out of scope.
