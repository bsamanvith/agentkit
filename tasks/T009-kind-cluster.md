---
id: T009
title: kind cluster and k8s overlays
epic: E01
status: todo
depends_on: [T005, T007]
---

# T009: kind cluster and k8s overlays

## Context

Optional path for things that need real Kubernetes: the k8s secrets credential backend, manifests, probes, multi-replica behavior. Compose stays the default ([ENGINEERING.md §3.1](../docs/ENGINEERING.md#31-approach)). Can move to a later epic if not needed yet.

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
