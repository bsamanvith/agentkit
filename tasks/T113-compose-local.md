---
id: T113
title: Local deploy in Docker Compose, beside the Kubernetes one
epic: E01
status: review
depends_on: [T006, T102]
---

# T113: Local deploy in Docker Compose, beside the Kubernetes one

## Context

The owner wants to test locally both ways, "in docker compose and in kubectl
way" (2026-10-22). T006 deploys the split deployment (T102,
docs/COMPONENTS.md) to Docker Desktop's Kubernetes (`make k8s-up`); it was
first planned as Compose and rescoped on 2026-10-17. This task brings Compose
back as a parallel way to run the same deployment, reusing T006's design:
same images, config, environment, security settings and limits, one script
with the same commands, and the T007 smoke suite against it.

## Scope

- `deploy/compose/compose.yaml`, the twin of `deploy/k8s/local`: Postgres 17
  on a named volume; a one-shot `migrate` (the api image running
  `agentkit migrate`); `api` with `AGENTKIT_STORE_MIGRATE=off`, started only
  after `migrate` exits 0; `runner`; `toolsets`. The images `make
  images-split` builds (`pull_policy: never`), the base's environment,
  healthchecks on `/readyz` through the image's own `agentkit healthcheck
  -ready` (distroless: no curl), the overlay's limits, the console on
  127.0.0.1:30081 (the cluster's is 30080).
- The config shared, not copied: `deploy/k8s/base/config.json`, from which
  the base's ConfigMap is now generated (`configMapGenerator`, no hash
  suffix: the rendered overlay is byte for byte what it was), which Compose
  mounts at `/etc/agentkit/config.json` and `scripts/smoke.sh` copies.
- Secrets in `deploy/compose/.env` (mode 600, ignored by git), written by the
  first `up` and kept, as T006's secret is: random RPC and API tokens, the
  Postgres password, `ANTHROPIC_API_KEY` from the environment (a new one
  replaces the stored one). Each service gets only what it needs.
- Profile `allinone`: the `agentkit-local` image alone on SQLite in a named
  volume, console on 127.0.0.1:30082. A one-shot busybox hands the new
  volume to uid 65532.
- `scripts/compose-local.sh` (`up [allinone]`, `status`, `token`,
  `logs [svc]`, `smoke [allinone]`, `down [-v]`) with project name
  `agentkit`; `make compose-up`, `compose-status`, `compose-smoke`,
  `compose-down` (`down -v`).
- Docs: `deploy/compose/README.md` (how to run it, what runs, configs,
  secrets and mounts), a "two ways to run it locally" pointer in
  `deploy/k8s/README.md`, ENGINEERING §3 rewritten to describe what exists
  (it still described an unbuilt `scripts/dev.sh`), and the `local-env`
  skill offering Compose or Kubernetes.

## Out of scope

- `fakellm` (T004) in Compose: the containerized ways call the real model.
- Redis and more than one API replica; Vault, LocalStack, observability.
- Exposing the runner and toolsets to the host so `TestComponents` runs
  against Compose (`make compose-smoke` skips it, as `make k8s-smoke` does).
- A Compose job in CI: CI's image job already runs the images
  (`scripts/smoke.sh --images`).

## Acceptance criteria

- [x] `docker compose config -q` accepts `deploy/compose/compose.yaml` (with
      and without `--profile allinone`) once the secrets are set, and refuses
      it with a message naming `scripts/compose-local.sh up` when they are
      not.
- [x] The base still renders as before: `kubectl kustomize deploy/k8s/local`
      gives the same output, byte for byte, with the ConfigMap generated from
      `config.json`.
- [x] Every agentkit service, run as a process with exactly the environment
      `docker compose config` renders for it (service names mapped to local
      ports) and each image's build tags: `migrate` fails while Postgres is
      down and succeeds, twice, after; each service's healthcheck command
      passes; the smoke suite passes strict (all six tests, `TestComponents`
      included) against the split services on a password-checking Postgres,
      with a conversation kept there; the suite passes against `allinone` on
      SQLite.
- [x] The secrets file is written once with mode 600 and kept: a later `up`
      keeps the tokens and password and replaces only a changed model key.
- [x] `shellcheck scripts/compose-local.sh` and `bash -n` pass.
- [ ] On Docker Desktop: `make compose-up` returns with every service healthy
      and `migrate` exited 0, and the console at http://localhost:30081
      accepts the token it prints; `make compose-smoke` passes. (Needs
      Docker: cloud sessions have no daemon.)
- [ ] `scripts/compose-local.sh up allinone` serves the console on
      http://localhost:30082 with its data kept across `up`s.
- [ ] `make compose-down` leaves no containers, volumes or `.env`.

## Notes

- Validation, 2026-10-22, in a cloud session without a Docker daemon: the
  Compose CLI (v5.6.0) is installed, so `docker compose config` ran for
  real. The process run is a scratch script (not committed) that calls
  `ensure_env` from `compose-local.sh`, renders the file with
  `docker compose config --format json`, and starts each service's binary
  with that environment only (`env -i`), on Postgres 16 from the session
  (the file pins 17-alpine) with scram password auth and the generated
  password, and the console's fake model in place of the Anthropic API.
  **Not run**: an actual `docker compose up` (image builds, the
  `service_completed_successfully` and healthcheck ordering, the volume
  ownership step, the 127.0.0.1 port bindings, the read-only root and memory
  limits under Docker, Postgres 17).
- `up` passes `--force-recreate` after a build, since the images are
  `:latest`; that recreates Postgres too (its data is in the volume). A new
  model key changes the runner's environment, which Compose recreates on its
  own.
- `up` waits on the healthchecks (`docker compose ps`) and on the console's
  `/readyz` from the host, not on `docker compose up --wait`, which treats
  the one-shot `migrate` exiting as a failure in some Compose versions.
- The model key lives in `.env` in plain text, as it does in the cluster's
  Secret; the file is mode 600 and git ignores `.env`.
- Files a toolset reads (the knowledge folder of another task) mount beside
  the config on `toolsets`; `compose.yaml` shows the line to add.
- No image changes, so no row in docs/SIZES.md. `postgres:17-alpine` and
  `busybox:1.37` (allinone only) are pulled on the first `up`.
- E01's checkbox is ticked with "in review", as E21 does for tasks in review.
