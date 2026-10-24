---
id: T118
title: "connectors/hubspot: search and read CRM records, notes, with writes behind settings"
epic: E22
status: todo
depends_on: []
---

# T118: `connectors/hubspot`

## Context

[E22](E22-connectors-and-tool-picker.md). HubSpot CRM API v3 (`https://api.hubapi.com`), per-user OAuth (a public app, authorization at `https://app.hubspot.com/oauth/authorize`, token at `https://api.hubapi.com/oauth/v1/token`; refresh tokens always issued). One toolset, so one package under `connectors/` ([`write-connector`](../.agents/skills/write-connector/SKILL.md)).

## Scope

- **Factory:** `hubspot.Factory(opts...)`, type `hubspot`, tools `hubspot_*`. The token comes from the toolset's credential slot (OAuth2) or, for development, a private app token named by `access_token_env` (default `HUBSPOT_ACCESS_TOKEN`). Settings: `read_only` (default true), `base_url`, `timeout`.
- **Tools:**
  - `hubspot_search` (read/low): `object` (`contacts`, `companies`, `deals`, `tickets`), `query`, optional property `filters` (`property`, `operator` from a fixed list, `value`), `properties`, `limit` (≤100), `after`.
  - `hubspot_get_record` (read/low): `object`, `id`, `properties`, `associations` (object types).
  - `hubspot_create_record` / `hubspot_update_record` (write/medium, `read_only: false`): `object`, `properties` (string map).
  - `hubspot_create_note` (write/medium, `read_only: false`): `body`, associated record ids.
- **OAuth:** a `hubspot` profile in `pkg/mcp/oauth.Profiles` (its endpoints; no refresh scope) and in `cmd/agentkit`'s `oauthProfile`; `hubspot.Scopes(settings)` (`crm.objects.<type>.read`/`.write`).

## Out of scope

- Deleting records, emails/marketing, owners and pipelines admin, webhooks.

## Acceptance criteria

- [ ] **Kit:** passes `toolsettest.RunConnector` with synthetic cassettes from HubSpot's reference and a fuzz target per read tool — tests: `TestConformance`, `FuzzResponses*`
- [ ] Search builds HubSpot's filter groups from the input, refusing an unknown operator or object before any request — test: `TestSearchRequest`, `TestInvalidInputSendsNothing`
- [ ] Writes are absent by default and medium risk when on — test: `TestGatingFollowsSettings`
- [ ] The slot's OAuth token is sent per caller; a caller with none gets "not connected" — test: `TestSlotToken`
- [ ] The `hubspot` profile sets HubSpot's endpoints — test: `oauth.TestProfiles` (extended), `cmd/agentkit.TestOAuth2AppsProfiles`

## Notes

-
