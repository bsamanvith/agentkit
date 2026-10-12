---
id: T075
title: "Agent registry: stored, versioned agent definitions and toolset instances"
epic: E15
status: todo
depends_on: [T029, T062]
---

# T075: agent registry

## Context

The admin console must create and edit agents, and every change must be a version that can be rolled back. Decisions: [E15](E15-agent-definitions.md).

## Scope

- `pkg/registry`: agent definitions and toolset instances as versioned items in the store (tenant-scoped): save (new version, validated), list, get (published or a given version), publish, list versions, delete unused drafts.
- Validation reuses what `agentkit serve` checks today (toolsets exist and build, tool-name clashes, model and effort valid, skills load) and returns field-level errors.
- `agentkit serve` builds agents from the registry's published versions; config-file agents load as read-only items; changes take effect for new runs without a restart (the harness for an agent is rebuilt when its published version changes).
- Runs record `agent_version`; `GET /v1/runs/{id}` shows it.
- API: create, update, versions, publish for agents and toolset instances (admin scope), in the OpenAPI spec.
- `agentkit export` / `agentkit import` (files: one JSON per item, same shape as config).

## References

- How LangSmith Prompt Hub, Vercel Edge Config and Kubernetes (`kubectl rollout history/undo`) model versions, publishing and rollback.
- [JSON Merge Patch (RFC 7386)](https://www.rfc-editor.org/rfc/rfc7386) if partial updates are offered.
- T062's config shape and OpenAPI spec; T029's migrations.

## Acceptance criteria

- [ ] Saving an agent creates version 1; editing creates version 2; publishing 2 makes new runs use it while a run started on 1 still reports `agent_version: 1`; publishing 1 again rolls back.
- [ ] An invalid definition (unknown toolset, tool clash, bad model) is refused with field-level errors and changes nothing.
- [ ] Config-file agents are listed as read-only; editing one is refused with a clear error.
- [ ] Export then import into an empty store reproduces the same published definitions (round-trip test).
- [ ] Tenant isolation (suite), and admin scope enforced (with T040).
- [ ] Mutation list recorded.
