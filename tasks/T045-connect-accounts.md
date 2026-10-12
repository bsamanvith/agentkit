---
id: T045
title: "Connecting accounts: credential API and OAuth2 authorization code + PKCE"
epic: E07
status: todo
depends_on: [T040, T044, T043]
---

# T045: connecting accounts

## Context

Credentials have to get into the vault somehow: an admin sets a tenant's API key, and a person connects their own Gmail. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **Endpoints** on the agent API, behind T040's auth, all tenant-scoped:
  - `GET /v1/credentials` lists metadata for the tenant, plus the caller's user-scoped ones;
  - `PUT /v1/credentials/{slot}` sets an API key, basic or client-credentials credential (scope `credentials:admin` for tenant-wide, the user themself for user-scoped);
  - `DELETE /v1/credentials/{slot}`.
- **OAuth2 per slot** in config: `{slot, auth_url, token_url, client_id, client_secret_env, scopes, redirect_url}`. The client secret is read from the environment variable named by `client_secret_env`, never placed in config.
- **The flow:**
  - `POST /v1/credentials/{slot}/connect` returns `{authorize_url}` with `state` and a PKCE S256 challenge;
  - `GET /oauth/callback?code&state` exchanges the code with the verifier and stores the tokens under the principal's tenant (and user, for user-scoped slots);
  - it then shows a minimal success page, or redirects to a configured return URL that is on an allowlist.
- **`state`:**
  - random 256-bit;
  - stored server-side with `{tenant, user, slot, verifier, return_url, expiry (10 min)}`;
  - single-use (deleted on first use, even on failure).
  - The callback needs no bearer token (the browser does not have one): the `state` alone identifies the flow.
- **CLI:** `agentkit credentials put <slot> --tenant ...` writes directly to the configured vault, for bootstrapping without the API.

## Out of scope

- A UI. Dynamic client registration. Device code flow (later, if a CLI needs it).

## References

- [RFC 7636: PKCE](https://www.rfc-editor.org/rfc/rfc7636)
- [RFC 9700: OAuth 2.0 Security Best Current Practice](https://www.rfc-editor.org/rfc/rfc9700) (state, redirect URI checks, open redirects)
- `golang.org/x/oauth2` (`GenerateVerifier`, `S256ChallengeOption`, `Exchange`)
- How Nango and Composio model 'connections' per user and tenant (UI and API shape)

## Acceptance criteria

- [ ] **Full flow against a fake OAuth server** (`httptest`): connect, follow the authorize URL, then the callback; the vault holds the tokens for the right tenant and user, and a tool call uses them (T044's test connector).
- [ ] **PKCE:** the token request carries the verifier matching the challenge; the fake server rejects a mismatch, and the callback reports failure without storing anything.
- [ ] **Bad `state`:**
  - a reused `state` is rejected (also after a failed first use);
  - an expired `state` (fake clock) is rejected;
  - an unknown `state` is rejected;
  - a `state` from another tenant's flow cannot store into the caller's tenant (it stores into its own, or fails).
- [ ] **Return URL:** one not on the allowlist is refused when the flow starts (no open redirect).
- [ ] **Listing:** `GET /v1/credentials` never contains secret values (canary), and shows `needs_reconnect` after T044's `invalid_grant`.
- [ ] **Scopes:**
  - a user can set their own user-scoped credential but not a tenant-wide one without `credentials:admin`;
  - tenant B cannot list, set or delete A's credentials.
- [ ] **Fuzzing:** `FuzzCallback` (query parameters) never panics, never stores.
- [ ] **Mutation list** recorded (at least: `state` not deleted on failure, no expiry check, verifier not sent, return URL not checked, tenant taken from the `state` request instead of the stored flow).

## Notes

- Keep flow state where every replica can read it: the callback may reach a different replica than the one that started the flow. Store it sealed in the credentials `BlobStore` under a reserved internal slot (or a small table beside it), not in process memory. Expire it there too.
