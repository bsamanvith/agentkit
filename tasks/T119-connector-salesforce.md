---
id: T119
title: "connectors/salesforce: SOQL queries and records, with writes behind settings"
epic: E22
status: done
depends_on: []
---

# T119: `connectors/salesforce`

## Context

[E22](E22-connectors-and-tool-picker.md). Salesforce REST API (`<instance_url>/services/data/v61.0`), per-user OAuth through a connected app; the `salesforce` profile of [T095](T095-mcp-oauth-client.md) keeps the token response's `instance_url`, which names the org's API host. One toolset, one package.

## Scope

- **Factory:** `salesforce.Factory(opts...)`, type `salesforce`, tools `salesforce_*`. Token and instance from the slot's OAuth2 credential (`Extra["instance_url"]`); for development `access_token_env` (default `SALESFORCE_ACCESS_TOKEN`) plus `instance_url` in settings. Settings: `read_only` (default true), `api_version` (default `v61.0`), `timeout`, `max_records` (default 200, at most 2,000).
- **Tools:**
  - `salesforce_query` (read/low): a SOQL `query` (SELECT only; checked before sending), `next` for the next page → `records` flattened (no `attributes`), `total_size`, `next`.
  - `salesforce_search` (read/low): plain-text `query`, optional `objects` → SOSL built by the connector with the text escaped.
  - `salesforce_get_record` (read/low): `object`, `id`, `fields`.
  - `salesforce_describe_object` (read/low): `object` → fields (name, label, type, required, picklist values), cut to fit.
  - `salesforce_create_record` / `salesforce_update_record` (write/medium, `read_only: false`): `object`, `fields`.
- **Host check:** the instance URL must be https on a Salesforce domain (`*.salesforce.com`, `*.force.com`, `*.salesforce.mil`) unless `base_url` is set for tests (loopback).
- **Scopes:** `api refresh_token`.

## Out of scope

- Deletes, Apex, Bulk API, metadata API, files.

## Acceptance criteria

- [x] **Kit:** passes `toolsettest.RunConnector` with synthetic cassettes and a fuzz target per read tool — tests: `TestConformance`, `FuzzResponses*`
- [x] A query that is not a SELECT, or holds a `;`, is refused before any request — test: `TestQueryMustBeSelect`
- [x] Search escapes SOSL's reserved characters so input cannot change the query — test: `TestSOSLEscapeCannotBreakOut` (property test)
- [x] The instance comes from the slot token's `instance_url`, and a non-Salesforce host is refused — test: `TestInstanceFromToken`
- [x] `api_version` picks the path's version — test: `TestAPIVersionSetting`
- [x] The slot connect flow and refresh keep `instance_url` and give a token without `expires_in` the profile's lifetime — tests: `connect.TestKeepExtras`, `credentials.TestRefreshKeepsExtras`, `cmd/agentkit.TestConnectConfig`
- [x] Writes are absent by default and medium risk when on — test: `TestGatingFollowsSettings`

## Notes

- **Slot tokens.** The T095 profile kept `instance_url` only on the MCP OAuth path; the slot connect flow (`pkg/credentials/connect`) and its refresh dropped token extras. `credentials.OAuth2App` and `connect.App` gained `KeepExtras` and `DefaultTTL` (Salesforce sends no `expires_in`), filled from the profile by `cmd/agentkit`; a refresh keeps the extras it does not repeat.
- **Per call.** With a slot, each call reads the user's token, checks its `instance_url` (https, a Salesforce domain, no path) and sends there (`rest.Client.With`). `base_url`/`WithBaseURL` replaces the org for tests only.
- **Search** writes the SOSL itself and escapes every reserved character; `salesforce_query` takes SOQL as written, SELECT only, because SOQL cannot change data and the model needs its full expressiveness.
- **Paging.** `Sforce-Query-Options: batchSize` asks for pages of `max_records` (at least 200); a smaller `max_records` cuts the page and sets `truncated`, with no `next`.
- **Mutations checked:** dropping the `;` rule, leaving `}` unescaped, a domain match by `Contains`, writes on by default, a fixed API version: each fails its test.
- **Size:** +184,320 bytes, under the budgets T118 raised ([docs/SIZES.md](../docs/SIZES.md)).
- Cassettes are synthetic (owner: real accounts not required).
