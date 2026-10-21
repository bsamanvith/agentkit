---
id: T016
title: local-env skill
epic: E02
status: done
depends_on: [T010]
---

# T016: `local-env` skill

## Context

Lets an agent verify its change in a running system, not just unit tests: pick a topology, bring it up, smoke it, read logs. Source of truth: [ENGINEERING.md §3](../docs/ENGINEERING.md#3-local-environments-pods). Needs `scripts/dev.sh` from E01/T006.

## Scope

`.agents/skills/local-env/SKILL.md`, steps:

1. Choose the smallest topology that exercises the change (table in §3.2; `run` with no Docker when possible).
2. `scripts/dev.sh up <topology>`; on failure, `status` and `logs <service>` and report the failing service.
3. `scripts/dev.sh smoke`; for a new feature, add or update a smoke flow only if it is user-visible (§2.3).
4. Leave the environment as found: `down` unless the user asked to keep it running.
5. Troubleshooting section: port in use, stale volumes (`reset`), missing Docker (fall back to `run`), real-LLM mode env vars.

## Acceptance criteria

- [x] Dry run: from a clean machine state, an agent brings up `split`, runs smoke, and tears down using only the skill. Recorded in Notes.
- [x] Dry run with a deliberately broken service: the agent identifies the failing service from `status`/`logs`. Recorded in Notes.

## Notes

- Blocked on E01/T006 for the dry runs; the skill text can be drafted earlier.
- 2026-10-21: written for what T006 actually built. Compose and `scripts/dev.sh` were dropped (T006, rescoped 2026-10-17), so the skill's steps are different:
  - the split deployment runs as processes: `make smoke`, `scripts/smoke.sh [--keep]`;
  - a real model in one process: `scripts/try-agent.sh`;
  - the image: `try-agent.sh --docker`, `make image`;
  - Docker Desktop's Kubernetes: `make k8s-up`, `k8s-smoke`, `k8s-status`, `scripts/k8s-local.sh logs`, `k8s-down`.

  "Topology" is now the choice between those, and `status`/`logs` are smoke.sh's failure output, or `k8s-status` and `k8s-local.sh logs` on the cluster. The ENGINEERING §3 text still describes Compose and `dev.sh`; updating it is the planner's call (reported).
- **Dry runs**, each by a separate Claude Code session (`claude -p`, nothing but the repo and the skill), in a scratch copy of the repo. These cloud sessions have no Docker daemon and no `kubectl`, so the split deployment ran as processes.
  - **Criterion 1.** Asked to "bring up the split deployment, run the smoke suite, and tear it down" with only the skill, the agent:
    - checked Node and Docker, and chose `make smoke` as the skill says;
    - got `ok .../test/e2e` and exit 0;
    - confirmed the ports were free and no `agentkit-*` or fake-model process was left.
    - It also reported that the skill's `pgrep -f agentkit-` matched unrelated shells whose paths contain the repo name. The skill now gives a precise pattern and a `/readyz` check.
  - **Criterion 2.** The toolsets' config was broken: type `demo-v2` in `deploy/k8s/base/split.yaml`. Told only "`make smoke` fails", the agent:
    - mapped `18582/readyz did not answer` to `PORT`+2, which is toolsets;
    - quoted its log line: `toolset "demo": unknown type "demo-v2" (compiled-in types: ...)`;
    - named the API's and runner's "waiting for a dependency" lines as knock-on failures;
    - found the bad line, and changed nothing.
    - It was not sure that smoke reads that file, so the skill now says where smoke's config comes from.
  - Not run here: `make k8s-up` and `try-agent.sh` (no Docker or cluster, no key in these sessions). Their steps in the skill are from `scripts/k8s-local.sh`, `deploy/k8s/README.md` and `scripts/try-agent.sh`.
