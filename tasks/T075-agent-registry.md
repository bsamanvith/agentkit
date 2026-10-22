---
id: T075
title: "Agent registry: stored, versioned agent definitions and toolset instances"
epic: E15
status: done
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

- [x] Saving an agent creates version 1; editing creates version 2; publishing 2 makes new runs use it while a run started on 1 still reports `agent_version: 1`; publishing 1 again rolls back.
- [x] An invalid definition (unknown toolset, tool clash, bad model) is refused with field-level errors and changes nothing.
- [x] Config-file agents are listed as read-only; editing one is refused with a clear error.
- [x] Export then import into an empty store reproduces the same published definitions (round-trip test).
- [x] Tenant isolation (suite), and admin scope enforced (with T040).
- [x] Mutation list recorded.

## Notes

Status `done`: every criterion has a test, listed below; all run in `make
check` (Postgres's in the integration tier, `STORE=postgres`).

**Criteria and their tests.**
- Versions, publish, rollback, `agent_version`:
  `pkg/agentapi/registry_test.go` `TestRegistryPublishAndRollBack` (a run
  held on v1 still runs and reports v1 after 2 is published, through a stop
  and a resume; new runs and conversations that go on get 2; publishing 1
  rolls back), `pkg/registry` `TestVersionsPublishAndRollBack`, and
  `pkg/runner` `TestRunKeepsItsAgentVersion`.
- Invalid definitions: `TestRegistryRefusesInvalidDefinitions`
  (`pkg/agentapi`), `TestInvalidDefinitionChangesNothing` (`pkg/registry`),
  and with serve's real builder `cmd/agentkit` `TestStoredAgentRefused`
  (unknown toolset, tool clash, unknown provider, a model that does not
  build, bad interaction, isolation, skills, memory, unknown field, wrong
  type).
- Config agents read-only: `TestRegistryConfigAgentsAreReadOnly`,
  `TestConfigAgentsAreReadOnly`, and the save over "plain" in
  `TestStoredAgentRefused`.
- Export and import: `cmd/agentkit` `TestExportImport` (two servers, the
  second on an empty store; drafts and config agents are not exported; a
  refused file is reported by field and the others still import).
- Tenants and scope: `storetest.RunItemStore` (`Tenants`, run on memory,
  SQLite and Postgres, plus the Postgres RLS test over `items` and
  `item_versions`), `pkg/registry` `TestTenants`, and
  `TestRegistryTenantsAndScope` (agent:admin for every registry endpoint,
  GETs too; a key limited to some toolsets cannot save an agent naming
  others; another tenant sees nothing).

**What was built.**
- `pkg/store`: `ItemStore`, versioned items by kind and name per tenant
  (save with compare-and-swap on the latest version, publish, versions,
  delete a never-published draft; numbers never reused). Memory, SQLite
  (`0014_items.sql`) and Postgres (`0015_items.sql`, with RLS) backends and
  the `storetest` suite (with a model the suite checks every subtest
  against, concurrent saves and a publish racing a delete). Runs gain
  `agent_version` (`store.WithAgentVersion`).
- `pkg/registry`: `agentapi.AgentRegistry` over the store. Save normalizes
  the definition (name set, keys sorted), validates it by building it,
  then stores it; publish builds it again. Built agents are cached by
  (tenant, name, version) in an LRU; versions never change, so publishing
  takes effect on the next run without a restart and nothing is rebuilt
  per run.
- `pkg/agentapi`: `GET`/`POST /v1/agents/{name}/versions`, `GET`/`DELETE
  /v1/agents/{name}/versions/{version}`, `POST /v1/agents/{name}/publish`;
  `400 invalid_definition` with `error.fields`, `409 read_only` and `409
  version_published`. Stored agents are in `GET /v1/agents` (with
  `version`; config agents get `read_only`), run, resume and route like the
  config's; the runner gets the version in its `Placement`. OpenAPI
  updated, every status probed.
- `cmd/agentkit`: the `agent_registry` config section (on by default),
  serve's builder (`registry.go`: the same checks as the config, errors
  mapped to fields), `agentkit export <dir>` / `agentkit import <dir>`
  (`<dir>/agents/<name>.json`, the config's shape). `pkg/agentclient`:
  `SaveAgent`, `AgentVersions`, `AgentVersion`, `PublishAgent`,
  `DeleteAgentVersion`, `APIError.Fields`.

**Decisions.**
- Toolset instances are not a second registry. MCP servers as tenant data
  are T096's (`pkg/mcp/servers`), which stored agents use through
  `runtime_toolsets`. Stored connector instances (type, settings,
  credential slot) are left for a follow-up: they need an operator
  allowlist of types and of the variables and paths settings may name, or a
  tenant admin could point a connector at the server's keys or files. The
  item store is generic over kinds for it.
- Registry GETs need agent:admin too: a version holds the whole definition
  (system prompt, policy).
- A version that was ever published cannot be deleted (runs may name it);
  drafts can, and their numbers are not given again.
- Stored agents cannot have `skills` or `memory` yet (both name paths on
  the server; T076 brings a skills store), nor `ui` (the API decides at
  startup whether any agent streams UI) or `notify` (a task can name its own
  webhook), nor isolation above `process` (no such pools run yet). Tasks
  (`POST /v1/tasks`) run stored agents, at the published version. The registry is off when agents run on a runner
  elsewhere (`$AGENTKIT_RUNNER_URL`), which builds only its file's agents.
- With the registry on, the agent API builds every toolset of
  `mcp.toolsets`, not only those config agents name, so a stored agent may
  use any of them.
- A config agent has no version: `agent_version` is absent on its runs.

**Mutation self-check** (each planted alone; the named test failed):
1. A run resumes on the published version, not its own
   (`describe` passes 0): `TestRegistryPublishAndRollBack`.
2. The runner drops the run's version on resume: `TestRunKeepsItsAgentVersion`.
3. Save skips validation: `TestInvalidDefinitionChangesNothing`.
4. Config agents writable: `TestConfigAgentsAreReadOnly`.
5. Cache key without the tenant: `TestTenants`.
6. Cache key without the version: `TestVersionsPublishAndRollBack`.
7. Registry endpoints need only read/run scope: `TestRegistryTenantsAndScope`.
8. The memory store deletes a published version: `TestItemStore/DeleteDrafts`.
9. SQLite forgets a run's `agent_version`: `TestRunStore/QueueOptionsAreStored`.
10. serve's builder takes unknown toolsets: `TestStoredAgentRefused`.
11. Import saves drafts: `TestExportImport`.
12. Export writes the latest version, not the published one: `TestExportImport`.
13. The run view omits `agent_version`: `TestRegistryPublishAndRollBack`.

**Size.** About 238 KB in the all-in-one, 209 KB in local and dev; 78 KB in api (the
builder and `pkg/registry` need models, so `nomodels` leaves them out:
`cmd/agentkit/registry_none.go`); about 25 KB in runner and toolsets (the
client calls and the two commands). Every image stays under its budget
(measured on main after T068; local's is 31 MB since #78); docs/SIZES.md has the row.

### For T076 (skills store)
- Store skills as another kind of `store.ItemStore` (`kind` "skill"); the
  registry's normalize, validate-by-building and LRU are the pattern.
- Then lift the refusal of `skills` in `cmd/agentkit/registry.go`
  (`storedAgentBuilder`): a stored agent would name stored skills, not
  paths.

### For T077 (console editors)
- The editor saves through `POST /v1/agents/{name}/versions` and shows
  `error.fields` by field (`model.provider`, `toolsets`, ...; empty is the
  whole definition). `if_latest` guards concurrent edits (409 conflict).
- `GET /v1/agents` marks config agents `read_only`; the console should
  show them without an edit button.

### Follow-ups
- Stored connector instances (see Decisions), and export/import of the
  toolsets added at run time.
- Stored agents on a separate runner component (the runner reads the
  registry, as T096's TODO says for runtime toolsets).
