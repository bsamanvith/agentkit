---
id: T009
title: "kind cluster and k8s overlays (superseded by T006)"
epic: E07
status: done
depends_on: [T005, T007]
---

> **Superseded by [T006](T006-compose-and-dev-script.md) (closed 2026-10-24, owner).** The local cluster is the owner's Docker Desktop Kubernetes (`make k8s-up`, `deploy/k8s`), not kind: kind does not run in cloud sessions, and the owner chose not to create a new cluster (2026-10-17). Nothing of this task is left to build; the k8s secrets backend for credentials, if wanted, is a new task.

# T009: kind cluster and k8s overlays

## Context

Optional path for things that need real Kubernetes: the k8s secrets credential backend, manifests, probes, multi-replica behavior. Compose stays the default ([ENGINEERING.md §3.1](../docs/ENGINEERING.md#31-approach)). Moved to the credentials epic (E07, to be planned): the k8s secrets backend is the first thing that needs it.

## Scope

- `deploy/k8s/base/` (Deployments, Services, probes on `/healthz` and `/readyz`, non-root security context).
- `deploy/k8s/overlays/allinone` and `overlays/split` (Postgres as a simple StatefulSet for dev only).
- `scripts/kind.sh`: `up` (create cluster), `load` (build + `kind load` images), `apply <topology>`, `smoke` (port-forward + reuse T007 suite), `down`.

## Out of scope

- Helm charts, production manifests, ingress.

## Acceptance criteria

- [ ] `scripts/kind.sh up && scripts/kind.sh apply split && scripts/kind.sh smoke` passes on a fresh machine with Docker and kind.
- [ ] Pods report Ready only after `/readyz` returns 200.
- [ ] `scripts/kind.sh down` removes the cluster.

## Notes

-
