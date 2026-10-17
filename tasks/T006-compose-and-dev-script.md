---
id: T006
title: Local deploy on Docker Desktop's Kubernetes
epic: E01
status: review
depends_on: [T005, T102]
---

# T006: Local deploy on Docker Desktop's Kubernetes

## Rescoped 2026-10-17

First planned as Compose topologies and `scripts/dev.sh`. The owner already
runs Kubernetes in Docker Desktop and asked (2026-10-17) to deploy there
instead of to Compose, and not to create another cluster (no kind, no k3d).
Docker Desktop's cluster shares the local Docker image store, so the images
`make images-split` builds run as they are (`imagePullPolicy: IfNotPresent`),
with nothing to load or push.

## Context

One command brings up the split deployment (T102, docs/COMPONENTS.md) on the
laptop, with the console, and returns only when it is usable. It is the
target the T007 smoke suite runs against.

## Scope

- `deploy/k8s/base/`: the three components of T102 (`split.yaml`, moved from
  `deploy/k8s/`), as a kustomize base with no namespace and no store.
- `deploy/k8s/local/`: the overlay for Docker Desktop. Namespace `agentkit`;
  Postgres 17 as a one-pod StatefulSet on a 1 GiB volume; the API on
  Postgres, its init container running `agentkit migrate` with the same image
  and serve with `AGENTKIT_STORE_MIGRATE=off`; small requests and limits; the
  API's Service on node port 30080.
- `scripts/k8s-local.sh` (`up`, `status`, `token`, `logs`, `down`) and
  `make k8s-up`, `make k8s-status`, `make k8s-down`. Every kubectl call names
  the context (`KUBE_CONTEXT`, default `docker-desktop`), so the script cannot
  touch another cluster. `up` builds the images, creates the secret once
  (random tokens and Postgres password; `ANTHROPIC_API_KEY` from the
  environment), applies Postgres, waits, applies the rest, restarts the
  deployments so rebuilt `:latest` images are picked up, and waits for them.
- `deploy/k8s/README.md`: how to run it, open the console and reset it.

## Out of scope

- Compose and `scripts/dev.sh`. Integration tests take any Postgres
  (`docker run postgres:17`, `AGENTKIT_PG_URL`); a `deps` script can come back
  if that gets tedious.
- Redis and more than one API replica (the T090 topology): a later overlay.
- `fakellm` (T004): runs use the real API, or fail with `model_auth` when no
  key is set.
- The smoke tests themselves (T007), ingress, TLS, Helm, production
  manifests (T009 and the deploy epic).

## Acceptance criteria

- [x] `kubectl apply --validate=strict -k deploy/k8s/local` is accepted by a
      Kubernetes 1.34 API server.
- [x] The same images, environment and security settings, run as containers
      with the overlay's memory limits: migrate fails while Postgres is down
      and succeeds after; `/readyz` is 200 with Postgres; the console serves
      `/`; `/v1/agents` lists the agent with its remote toolsets; a run goes
      API, runner, model and is stored in Postgres.
- [ ] On Docker Desktop: `make k8s-up` returns with every pod Ready and the
      console at http://localhost:30080 accepts the token `make k8s-up` prints.
      (Needs the owner's machine; a nested cluster does not start in the
      cloud sessions.)
- [ ] `make k8s-down` leaves no namespace, pods or volumes.
- [x] `shellcheck scripts/k8s-local.sh` passes.

## Notes

- Idle, the four containers use about 45 MiB together (api 8, runner 4,
  toolsets 4, Postgres 27). The overlay requests 0.2 CPU and 300 MiB in all
  and limits the Go components to 128 MiB each, Postgres to 256 MiB.
- The secret is kept across `up`s: Postgres keeps the password it was
  initialized with. `make k8s-down` deletes it with the data; a new key in
  `ANTHROPIC_API_KEY` replaces the stored one and restarts the pods.
- No image changes, so no row in docs/SIZES.md. Postgres (`postgres:17-alpine`)
  is pulled from Docker Hub on the first `up`.
