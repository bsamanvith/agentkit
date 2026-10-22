---
id: T045
title: "Connecting accounts: credential API and OAuth2 authorization code + PKCE"
epic: E07
status: done
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

- [x] **Full flow against a fake OAuth server** (`httptest`): connect, follow the authorize URL, then the callback; the vault holds the tokens for the right tenant and user, and a tool call uses them (T044's test connector).
- [x] **PKCE:** the token request carries the verifier matching the challenge; the fake server rejects a mismatch, and the callback reports failure without storing anything.
- [x] **Bad `state`:**
  - a reused `state` is rejected (also after a failed first use);
  - an expired `state` (fake clock) is rejected;
  - an unknown `state` is rejected;
  - a `state` from another tenant's flow cannot store into the caller's tenant (it stores into its own, or fails).
- [x] **Return URL:** one not on the allowlist is refused when the flow starts (no open redirect).
- [x] **Listing:** `GET /v1/credentials` never contains secret values (canary), and shows `needs_reconnect` after T044's `invalid_grant`.
- [x] **Scopes:**
  - a user can set their own user-scoped credential but not a tenant-wide one without `credentials:admin`;
  - tenant B cannot list, set or delete A's credentials.
- [x] **Fuzzing:** `FuzzCallback` (query parameters) never panics, never stores.
- [x] **Mutation list** recorded (at least: `state` not deleted on failure, no expiry check, verifier not sent, return URL not checked, tenant taken from the `state` request instead of the stored flow).

## Notes

- Keep flow state where every replica can read it: the callback may reach a different replica than the one that started the flow. Store it sealed in the credentials `BlobStore` under a reserved internal slot (or a small table beside it), not in process memory. Expire it there too.
- Done 2026-10-22. Every criterion runs against fakes (`credentialstest.TokenServer` gained `/authorize` and the authorization code grant with PKCE S256). No real provider account was tried: see Follow-ups. Tests per criterion:

| Criterion | Test |
|---|---|
| Full flow | `pkg/credentials/connect` `TestFullFlow` (connect, the authorize URL, the callback; tokens in the vault for tenant and user; a `toolsettest.CredentialEcho` call through a `Source` uses them); `cmd/agentkit` `TestConnectAccounts` (the same through serve's agent API, the fake provider and the mounted `/oauth/callback`, then the MCP tools use the stored credentials); `TestFlowAcrossReplicas` (a second `Service` over the same `BlobStore`, memory and file, finishes the flow; no plaintext state in the file) |
| PKCE | `TestPKCE` (the verifier sent is the one whose S256 is the challenge; a tampered verifier is refused by the fake, the callback answers failed, page or return URL, and stores nothing; a provider error echoing the form and the canary reaches neither log nor page) |
| Bad `state` | `TestBadState` (reused after a success and after a failure, expired on the fake clock, unknown, and another tenant's flow finished with a request carrying tenant B: it stores into A) |
| Return URL | `TestReturnURL` (allowlist by scheme, host and path; other path, host prefix, user info, fragment, other scheme refused when the flow starts) |
| Listing | `pkg/agentapi` `TestCredentialListing` (no value in any answer, canary; `needs_reconnect` after `invalid_grant` through `Sources`, cleared by reconnecting; per-caller status in `GET /v1/toolsets`) |
| Scopes | `TestCredentialScopes` (user sets their own, not the tenant's without `credentials:admin`; a key with no user cannot use a user slot; tenant B lists, deletes and sets only its own); `TestOpenAPIResponses` probes every documented status |
| Fuzzing | `FuzzCallback` (seeded; 15 s run clean): never panics, never stores |
| Mutations | below |

- Decisions:
  - **Slots are the toolsets'**: the API knows the slots and scopes that the server's toolset specs name (`credential.slot`, `scope`); any other slot is 404. A slot used with both scopes needs `scope` in the request. Tenant scope needs `credentials:admin` (`"*"` grants it); user scope needs a principal with a `user`.
  - **Flow state** is sealed in the vault's `BlobStore` under the reserved tenant `agentkit-oauth-flows`, slot `_oauth_flows` (slots starting with `_` are refused in config), in 64 buckets by the state's SHA-256 (the state itself is never stored). Each bucket is a JSON map written with CAS and retries; expired entries are pruned on every write, and a bucket of 64 drops its oldest. `take` deletes before anything is checked, so a failed use burns the state too. No migration.
  - **The callback** reads nothing from the request but the query: it clears the request's tenancy and uses the flow's tenant and user. It answers a small page with a CSP, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`, or a 303 to the return URL with `result` (`connected`, `denied`, `expired`, `failed`) and `slot`. Failures log the token endpoint's status and RFC 6749 code only (`tokenReason`), never its body.
  - **`pkg/mcp/oauth` (T095) is reused for its provider profiles only** (`profile` in `credentials.oauth2`, `google`, `microsoft`, `salesforce`): its `Connector` needs an MCP resource and finishes from an authenticated console post, while this callback is unauthenticated and the slot's app is configured. `nomcp` builds (api) have no profiles (`credentials_profiles_none.go`).
  - **T044's deferrals**: `GET /v1/toolsets` gives a slot toolset the caller's status (`connected`, `not_connected`, `needs_reconnect`) when the server keeps credentials. Stored token URLs (`client_credentials` via PUT) must be https and pass netguard when stored (`CheckTokenURL`), and are called through netguard's client (`credentials.WithStoredTokenClient`); `allow_hosts` lets internal ones through. Configured token URLs keep the plain client.
  - **CLI**: `agentkit credentials put` reads the secret from stdin's first line, never a flag; it refuses the memory backend and a missing key ring, and is behind `!noagent`.
  - **`connect` is behind `!noagent`** (`scripts/check-deps.sh` forbids it in runner and toolsets); `cmd/agentkit/credentials.go` does not import it, so `oauthCallbackPath` is its own const, checked equal to `connect.CallbackPath` in `TestConnectConfig`.
- Sizes: docs/SIZES.md T045 rows, measured on main `33c3085` (after T060 and T068): all +196,608, local +196,608, dev +192,512, api +323,584, runner +32,768, toolsets +40,960. Local's budget is raised to 31 by bsamanvith/agentkit#78, not here. api projects to about 22,165,504, 165,504 over 22 MB; the cuts api could take (the CLI behind a tag, 12,288; with a non-generic sort of `credentials.Meta`, 40,960) do not reach that, so `IMAGE_BUDGET_MB_api` goes to 23 with its own row, which names them as what gets cut first.
- Review fixes (2026-10-22):
  - **Key rotation bricked connecting:** a flow bucket sealed under a key the ring no longer has failed to open, and since a bucket is only rewritten after it is read, every `Connect` hashing to it failed for good (all of them once every bucket predates the rotation). A bucket that does not open now reads as empty at its version, so the next write replaces it; its flows are lost, which fails closed. `TestFlowsAfterKeyRotation`.
  - **Reserved tenant:** nothing stopped an API key, a token's claim or a tenant header from acting as `agentkit-oauth-flows`. The service now refuses every call in that tenant (`agentapi.ErrCredentialForbidden`, 403, as documented; it was a 400 at first, which `GET /v1/credentials` does not document), and `agentkit credentials put -tenant agentkit-oauth-flows` is refused. `TestReservedTenant`, `TestCredentialReservedTenant` (every endpoint over HTTP, with a key holding every scope), `TestCredentialsPutCLI`.
  - **Tests that were missing:** `TestStateRace` (n callbacks with one state all read the flow before any writes, through a barrier store: exactly one exchange and one connect; it is the only test that catches a write treating a lost compare-and-swap as success), and `TestBadState/two_codes` (a callback with the code twice is refused without an exchange).
  - Mutations added: a lost compare-and-swap taken as success (caught by `TestStateRace` only); two codes accepted; expiry checked with `After` (off by one); the reserved-tenant check off; `ErrCredentialForbidden` not mapped to 403 (caught by `TestCredentialReservedTenant`).
- Mutations (planted by a script, each caught; all restored): state not deleted on failure; state never deleted; no expiry check; verifier not sent; challenge not sent; return URL not checked; return URL path by prefix; return URL host by prefix; tenant taken from the callback request instead of the stored flow; the callback keeping the request's tenant scope; user ignored when storing; a 64-bit state; the token endpoint's error body in the log; tenant slot without `credentials:admin`; user scope for a key with no user; listing ignoring the user; `needs_reconnect` not shown; `/v1/toolsets` status not per caller; stored token URL not guard-checked; stored token URL on the config's client; callback not mounted; credentials not given to the API; redirect URL path not checked.
- **Security fix: state bound to the browser** (2026-10-22, RFC 9700 §4.7, login CSRF). The `state` was not tied to the browser that started the flow. A caller could start a flow for their own slot, send the authorization URL to someone else, and if that person consented, their account was stored under the caller's tenant and user. Separately, the 64 shared buckets of 64 let one caller push out everyone's pending flows by starting about 4,000 of their own.
  - **Binding:** `Connect` now also returns a cookie (`agentapi.Credentials.Connect` returns `(wire.ConnectResponse, *http.Cookie, error)`). It is named `agentkit_oauth_<8 bytes of a hash of the state>`, one per flow so that flows running at the same time in one browser don't replace each other. Its value is 256 random bits; it is `HttpOnly`, `SameSite=Lax`, its `Path` is the redirect URL's path (`/oauth/callback`), and its `Max-Age` is the flow's TTL. The agent API sets it on the `POST .../connect` answer, and makes it `Secure` when the request came over TLS or with `X-Forwarded-Proto: https` (believed from any peer, because `Secure` only narrows where the cookie goes). The flow keeps only the SHA-256 of the value. The callback takes the flow (still single use), clears the cookie, and requires it, compared in constant time. When the cookie is missing, has another value, or the flow has no binding, the callback answers 403 with a page ("not started in this browser"). It exchanges nothing, stores nothing, and does not redirect to the return URL. openapi.yaml documents the `Set-Cookie`, and QUICKSTART says to start the flow from the browser that will open the URL, on the redirect URL's host.
  - **Per-caller flows:** the state is now `<owner>.<random>`, where owner is 16 bytes of SHA-256 of the tenant and user, in hex. Each caller's pending flows live in their own blob, `flows-<owner>`, and that blob holds at most 16 (`maxPending`): a 17th drops the caller's own oldest. Because the blob name comes from the state, the callback still finds it without a tenant. An empty blob is written, not deleted, because a version restarting after a delete would let a stale compare-and-swap win. That means one small blob stays per caller who ever started a flow. Pending flows in the old `flows-00`..`flows-3f` buckets can no longer be reached after the upgrade (they expire in 10 minutes; those blobs stay as garbage).
  - **Console:** no change. It doesn't start flows yet (the Connections page is a placeholder), and its client fetches with `credentials: "same-origin"`, so the `Set-Cookie` is kept when it does. It must open `authorize_url` in the same browser.
  - **Tests:** `TestBrowserBinding` covers the cookie's attributes; that a cookie jar sends it to the callback only; that the flow keeps the hash and not the value; the happy path, with the cookie cleared; no cookie, with and without a return URL; the victim's own cookie under its own name and under the flow's; and a changed value. Each refusal is a 403 with no exchange, nothing stored and the state used up. `TestPendingFlowsPerCaller` checks that 1,000 flows from one caller leave the pending flows of a user and of the tenant in the same tenant, and of a user and of the same user id in another tenant, all finishing, with no foreign flow in their blobs. It also checks that the caller's own oldest flow, and their 17th newest, are dropped. `pkg/agentapi` `TestCredentialConnectCookie` covers the `Set-Cookie` over http, behind TLS and behind http proxying, plus a callback without it being 403. The other tests carry the cookie, and the e2e `TestConnectAccounts` uses a cookie jar.
  - **Mutations** (planted by a script, each caught, all restored): binding check skipped; cookie presence only, value not compared; exchange before the binding check; refusal following the return URL; cookie not cleared; not `HttpOnly`; `SameSite=None`; `Path=/`; no `Max-Age`; value kept instead of its hash; `Secure` never set; `Secure` always set; flows bucketed by state again (the old 64); per-caller cap 64; owner ignoring the user; owner ignoring the tenant; dropping the newest instead of the oldest. The first and the bucketing mutations restore the behaviour from before the fix.

## For T064 and T070 (console)

- The connections page lists `GET /v1/credentials`, sets keys with `PUT`, and connects with `POST /v1/credentials/{slot}/connect` and `return_url` set to a page in `credentials.return_urls` (e.g. `/connections/done`), which reads `result` and `slot` from its query.

## For T095

- The flow store (`flows.go`: sealed, bucketed, CAS, single use) and the unauthenticated callback could serve MCP OAuth too: a flow kind and a completion per kind would let both share `/oauth/callback`.

## Follow-ups (not in this task)

- Try the flow against a real Google and Microsoft app (no accounts here).
- Done (see "Security fix: state bound to the browser" in Notes; every flow is bound, API-key callers included): owner question, from the security review: **the state is not bound to the browser that started the flow** (RFC 9700 §4.7). A caller can start a flow for their own slot and get someone else to open the authorize URL; if that person consents, their account is stored as the caller's. The provider's consent page names the app and the account, which is all that stops it now. Binding needs the browser: a cookie set when the console starts the flow (T061's sessions), checked by the callback for flows started from a session, which leaves API-key callers as they are. Not done here: it changes how the console starts a flow (T064, T070).
- Owner questions: per-user rate limits on `connect` (pushing out others' pending flows is fixed, since each caller now has their own blob of 16; a rate limit would still bound the writes); whether listing should show only slots of agents the caller may use; the default scope when a slot is used with both (now: the request must say).
