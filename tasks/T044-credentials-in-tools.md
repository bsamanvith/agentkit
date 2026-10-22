---
id: T044
title: "Credentials in tools: slots, scoped sources, OAuth2 refresh, canary end to end"
epic: E07
status: done
depends_on: [T042, T026]
---

# T044: credentials in tools

## Context

A toolset must call its upstream API with the calling tenant's (or user's) credential, chosen at call time, with no secret in config, the model's context, events, logs or errors. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`toolset.Spec` gets `Credential *CredentialRef`**: `{"slot": "...", "scope": "tenant"|"user"}`.
  - `scope` defaults to `tenant`.
  - Several toolsets may share a slot; two Salesforce orgs use two slots.
- **The factory gets a source:** it receives a **`credentials.Source`** bound to that slot alone. Planner direction: extend `Factory.Build` with a build input struct carrying `Settings` and `Credentials`. Pre-1.0, so the signature can change; update `Demo` and the toolset tests.
  - `src.Get(ctx) (Credential, error)` resolves the tenant (and user, for `scope: user`) from `ctx`.
  - `src.HTTPClient(ctx, base *http.Client) (*http.Client, error)` returns a client that adds auth (bearer, basic or API-key header as configured) and refreshes when needed.
  - The source cannot name another slot (type-level: it has no slot parameter).
- **OAuth2 refresh:**
  - refresh when the access token expires within a margin (default 60 s);
  - singleflight per `(tenant, user, slot)` in a process;
  - write back with CAS; on `ErrConflict`, re-read and use the stored token if it is fresh, else retry once;
  - `invalid_grant` sets `NeedsReconnect` and returns `ErrNotConnected`;
  - client credentials fetch and cache a token per `(tenant, slot)`.
  - Uses `golang.org/x/oauth2` for the exchange (add to the archtest allowlist for `pkg/credentials` only).
- **Errors:**
  - `ErrNotConnected` (no credential, or one needing reconnection) reaches the model as an `isError` tool result: "<toolset> is not connected for this account", with no detail from the vault;
  - over MCP it is the same tool error;
  - in the agent API, `tool_result` carries it, and a `run_done`/`run_error` never has a 500-style code because of it.
- **Wiring:** `agentkit serve` builds the vault (T043 or memory), passes sources to factories, and refuses a spec naming a slot when no vault is configured.
- **A test-only connector** (`toolset` test package, or a `connectors/httpbin`-style example under `examples/`): calls an `httptest` upstream that checks the credential. It is used for the end-to-end and canary tests.

## Out of scope

- Real connectors (a later epic). Endpoints to add credentials (T045); tests seed the vault directly.

## Acceptance criteria

- [x] **The right credential is used:** a tool call with tenant A in `ctx` reaches the upstream with A's credential; with B it uses B's; with `scope: user`, users U1 and U2 of one tenant each use their own. One table test drives this over MCP and through the agent.
- [x] **No credential:** the model gets the "not connected" tool error; the MCP result has `isError`; the run continues (the model can answer without the tool).
- [x] **One refresh:**
  - with an access token about to expire, 50 concurrent calls in one process cause exactly one refresh request;
  - two "replicas" (two vault users over one `BlobStore`) refreshing at once end with one stored token chain: the loser re-reads, and no refresh token is lost.
- [x] **`invalid_grant`** on refresh gives `ErrNotConnected` and `NeedsReconnect` in `Meta`, and no retry loop: the fake token server counts one request.
- [x] **Canary, end to end:**
  - seed a canary secret, then run tool calls (success, upstream 401, refresh failure, panic in the tool) over MCP and through the agent with a fake model;
  - the canary never appears in logs (all levels, debug included), SSE events, MCP responses, stored conversation entries, checkpoints or error strings.
- [x] **Slot isolation:** a factory's source cannot read another slot (type-level), and a spec with an unknown slot fails startup only when it is not `optional`.
- [x] **Mutation list** recorded (at least: tenant ignored when resolving, refresh without singleflight, CAS conflict treated as an error, `Authorization` header logged at debug level, upstream error body copied into the tool error).

## Notes

- The upstream's error body can echo credentials. Tool errors carry a status and a short reason, never the upstream body.
- Done 2026-10-22. Tests per criterion:

| Criterion | Test |
|---|---|
| The right credential | `cmd/agentkit` `TestCredentialsInTools` (one table over MCP and through the agent API with the fake model: tenants A and B, users U1 and U2, a tenant-scoped slot called by a user; the upstream counts whose credential it saw); `pkg/credentials` `TestSourceUsesTheCallersCredential` |
| No credential | `TestCredentialsInTools` rows "no credential", "no user credential", "invalid_grant": MCP `isError`, the agent's `tool_result` `is_error` and the run ends `run_done` with the model's answer; `TestSourceNotConnected` (none, another slot, no tenant, marked) |
| One refresh | `TestOneRefreshForConcurrentCalls` (50 callers, the token endpoint holds the refresh until all are in: 1 request); `TestTwoReplicasRefreshing` (two `Sources` over one `BlobStore`, both refreshes reach the endpoint: one stored chain, both use the winner's token, its refresh token valid); `TestReplicaRefusedRefreshDoesNotLoseTheChain`; `TestRefreshConflict` (a writer in between: its fresh token used, or a stale one refreshed once more) |
| invalid_grant | `TestInvalidGrant` (`ErrNotConnected`, `Meta.NeedsReconnect`, 1 request then and on later calls; a new write clears it); suites `needs_reconnect` (BlobStore and Vault) on memory, file, SQLite and Postgres |
| Canary | `TestCredentialsInTools/canary`: the canary is in every API key, access and refresh token, issued token and the OAuth2 client secret; calls succeed, hit an upstream 401 whose body echoes the header, a refresh that answers invalid_grant echoing the token, a refresh answering 503 echoing the form, and a panicking tool; it is absent from the debug JSON log of every component, every HTTP response the clients got (SSE, JSON, MCP), the model's requests, and the SQLite store's files (entries, runs, events, checkpoints; checked non-empty). `pkg/credentials` rig checks its debug log after every test |
| Slot isolation | `TestSourceCannotNameASlot` (no method of `Source` takes a string), `TestSourceNotConnected` (another slot's credential is not used); `pkg/toolset` `TestBuildGivesTheSlotsSource`, `TestBuildCredentialFailures` (unknown slot: fails when required, a `Problem` when optional; malformed slot or scope fails either way); `cmd/agentkit` `TestCredentialSlotNeedsAVault` |
| Mutations | below |

- Decisions:
  - **`Factory.Build(ctx, toolset.BuildInput{Settings, Credentials})`**, as the planner directed; every factory (demo, coretools, agenttools, the MCP client, the connectors, the kit's fakes) and caller updated mechanically. `toolset.Build(..., toolset.WithSources(p))` takes a `SourceProvider` (`*credentials.Sources` is one; `toolset.SourceFunc` adapts a func).
  - **"Unknown slot"** is a slot the provider refuses: with no vault, serve's provider refuses every slot (`ErrNoCredentials`, naming `$AGENTKIT_CREDENTIALS_KEY`), so a required spec fails startup and an optional one is skipped. Slots are not declared anywhere: any valid name works, and a slot with nothing stored is "not connected" at call time. A malformed slot or scope is a spec mistake and fails even when optional.
  - **`Vault.MarkNeedsReconnect(ctx, user, slot, ifVersion)`** (a fifth method): CAS on the version, keeps the sealed bytes, makes a new version. `BlobStore.Put` now stores `NeedsReconnect` as given (the vault's `Put` always passes false, so any write clears it). Columns `needs_reconnect` in SQLite migration 0011 and Postgres 0012; the file backend's records get `needs_reconnect`.
  - **Refresh**: margin 60 s (`WithRefreshMargin`); one refresh per (tenant, user, slot) in a process, on its own goroutine with the caller's values but not its cancellation (bounded by `WithTokenTimeout`, 30 s), so a caller that gives up does not fail the others. It re-reads first; writes with `Put(ifVersion)`; on `ErrConflict` re-reads and uses the stored token if fresh, else refreshes once more. A refresh token the endpoint does not rotate is kept. A failed refresh (not `invalid_grant`) uses the current token while it has not expired, else a `*SourceError`. `invalid_grant` marks (CAS; on conflict re-read once) and returns `ErrNotConnected`.
  - **Single-use refresh tokens across replicas**: the replica whose refresh is refused may mark the credential before the winner writes. The winner, on its conflict, finds the mark on the same refresh token it used and writes its new token over it (`supersede`), so the chain is not lost; the refused replica's one call fails as not connected. A provider with reuse detection that revokes the whole family on reuse cannot be helped without a lock across replicas.
  - **Client credentials**: tokens cached in memory per (tenant, user, slot) and credential version; a new version fetches again.
  - **No auth-style probing**: x/oauth2's auto-detection sends a failed request twice, so `OAuth2App.AuthStyle` is explicit (`header` default, `params`).
  - **Error text**: `NotConnectedError` says only "<toolset> is not connected for this account"; `SourceError` gives a short reason (status and an RFC 6749 standard error code only; never the endpoint's body or description, which may echo what was sent). The test connector maps an upstream error to its status.
  - **`HTTPClient`** binds the caller's tenant and user when made, gets the credential (refreshing) per request, never changes the caller's request, and refuses a redirect to another host (the transport adds auth, so Go's own stripping would not apply). API keys go as `Authorization: Bearer` unless `credentials.APIKeyHeader(name)`.
  - **Token endpoints are not redirected**: the token client never follows a redirect (a 307 would resend the refresh token and client secret), and `token_url` must be https (http only on loopback). `HTTPClient` also refuses a redirect from https to http.
  - **Shared refreshes**: an error from a refresh another toolset on the same slot started names the caller's toolset.
  - **Config**: `credentials.oauth2: [{slot, token_url, client_id, client_secret_env, auth_style}]`; the secret only from the environment (unset fails startup). T045 adds `auth_url`, `scopes`, `redirect_url` to the same entries.
  - **The test connector** is `toolsettest.CredentialEcho` (type `credential_echo`, tools `whoami` and `whoami_panic`) with `toolsettest.CredentialUpstream`; `credentialstest.TokenServer` is a fake token endpoint (refresh and client credentials grants, rotation, single use, hooks).
- Sizes (stripped `GOOS=linux CGO_ENABLED=0 -trimpath` binaries, origin/main at `5f75d75` and the branch): all +32,768, local +36,864, dev +32,768, api +32,768, runner +32,768, toolsets +36,864. x/oauth2 was already in the module and in all, local, dev and toolsets; it now also links into api and runner (through `pkg/toolset` → `pkg/credentials`), within those numbers. Under 100 KB everywhere: no docs/SIZES.md row.
- Mutations (each planted by a script, each caught): tenant ignored when resolving (unit and e2e); user ignored for a user scope; refresh without singleflight; CAS conflict treated as an error; the winner not superseding a refused replica's mark; `Authorization` header logged at debug (unit rig and e2e canary); upstream error body copied into the tool error (e2e canary); the token endpoint's error text in the tool error (unit and e2e); `invalid_grant` retried; `invalid_grant` not marked; `NeedsReconnect` ignored when reading; redirect to another host followed; client credentials token not cached; the cache ignoring the tenant; `MarkNeedsReconnect` ignoring the version (vault suite); SQLite and Postgres not storing `needs_reconnect` on update; the file backend forgetting it on reload; memory clearing it; an optional unknown slot failing the build; serve giving no sources; a factory given another slot's source.

## For T045

- Store credentials with `Vault.Put` (clears `NeedsReconnect`); list with `Vault.List`, whose `Meta.NeedsReconnect` is what `needs_reconnect` shows.
- Put the OAuth2 app fields beside `credentials.oauth2` entries (`cmd/agentkit/credentials.go`, `oauth2AppConfig`); `credentialSources` builds the `Sources` from them.
- `credentialstest.TokenServer` is a fake token endpoint (refresh and client credentials grants); the authorization code grant and PKCE are yours to add to it.
- `GET /v1/toolsets` still says `credential.status: not_needed` for a slot-based toolset (`cmd/agentkit/agent.go` `credentialOf` knows only env keys): give it a status of its own (per account, see `/v1/credentials`).

## Follow-ups (not in this task)

- Tenant and user do not cross `pkg/rpc` yet (ENGINEERING §1.5): a slot-based toolset served by the `toolsets` component to a remote runner or agent API gets no tenant and answers not connected. Needs the tenant and user in the RPC headers.
- MCP without an authenticator (loopback only) has no tenant in a tool call's context, so slot-based toolsets are not connected there; the agent API uses tenant `default` in that case. Decide whether the MCP server should too.
- The connector suite (`toolsettest.RunConnector`) does not run slot-based connectors yet; real connectors moving to slots need its key checks to take a Source.
