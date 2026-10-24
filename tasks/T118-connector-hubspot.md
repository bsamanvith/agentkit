---
id: T118
title: "connectors/hubspot: search and read CRM records, notes, with writes behind settings"
epic: E22
status: done
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

- [x] **Kit:** passes `toolsettest.RunConnector` with synthetic cassettes from HubSpot's reference and a fuzz target per read tool — tests: `TestConformance`, `FuzzResponses*`
- [x] Search builds HubSpot's filter groups from the input, refusing an unknown operator or object before any request — test: `TestSearchRequest`, `TestInvalidInputSendsNothing`
- [x] Writes are absent by default and medium risk when on — test: `TestGatingFollowsSettings`
- [x] The slot's OAuth token is sent per caller; a caller with none gets "not connected" — test: `TestSlotToken`
- [x] The `hubspot` profile sets HubSpot's endpoints — test: `oauth.TestProfiles` (extended), `cmd/agentkit.TestConnectConfig`

## Notes

- **Shared client.** HubSpot and Salesforce act for a user with a bearer token like the Google family, so the Google client moved to `connectors/internal/rest` (provider-neutral: the account name, the error-body reader and the rate-limit rule are configuration) and `connectors/google/common` wraps it; Google's tests run through it unchanged. One behaviour change for every user of it: a 409 is now `ErrInvalidInput` with the provider's message (HubSpot answers an existing email with 409 and the existing id), where it was a plain failure.
- **OAuth.** The `hubspot` profile carries the endpoints, no refresh scope (HubSpot always issues one) and `AuthInParams` (HubSpot takes the client only in the token request's form); `cmd/agentkit` turns it into `auth_style: params` for a slot.
- **Notes** are timestamped with the factory's clock, so cassettes are stable under the kit's fake clock.
- **Mutations checked:** dropping the HAS_PROPERTY value rule (`TestSearchRequest`), sending the env token with a slot (`TestSlotToken`), exposing writes by default (`TestGatingFollowsSettings`), header auth for the hubspot profile (`TestProfiles`).
- **Size:** +184,320 bytes; agentkit and local budgets +1 MB ([docs/SIZES.md](../docs/SIZES.md), 2026-10-24).
- Cassettes are synthetic (owner: real accounts not required).
