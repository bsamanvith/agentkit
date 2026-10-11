---
id: T016
title: local-env skill
epic: E02
status: todo
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

- [ ] Dry run: from a clean machine state, an agent brings up `split`, runs smoke, and tears down using only the skill. Recorded in Notes.
- [ ] Dry run with a deliberately broken service: the agent identifies the failing service from `status`/`logs`. Recorded in Notes.

## Notes

- Blocked on E01/T006 for the dry runs; the skill text can be drafted earlier.
