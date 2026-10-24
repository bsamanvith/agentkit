---
id: T119
title: "connectors/salesforce: SOQL queries and records, with writes behind settings"
epic: E22
status: todo
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

- [ ] **Kit:** passes `toolsettest.RunConnector` with synthetic cassettes and a fuzz target per read tool — tests: `TestConformance`, `FuzzResponses*`
- [ ] A query that is not a SELECT, or holds a `;`, is refused before any request — test: `TestQueryMustBeSelect`
- [ ] Search escapes SOSL's reserved characters so input cannot change the query — test: `TestSOSLEscapeCannotBreakOut` (property test)
- [ ] The instance comes from the slot token's `instance_url`, and a non-Salesforce host is refused — test: `TestInstanceFromToken`
- [ ] Writes are absent by default and medium risk when on — test: `TestGatingFollowsSettings`

## Notes

-
