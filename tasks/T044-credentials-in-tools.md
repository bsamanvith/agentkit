---
id: T044
title: "Credentials in tools: slots, scoped sources, OAuth2 refresh, canary end to end"
epic: E07
status: todo
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

- [ ] **The right credential is used:** a tool call with tenant A in `ctx` reaches the upstream with A's credential; with B it uses B's; with `scope: user`, users U1 and U2 of one tenant each use their own. One table test drives this over MCP and through the agent.
- [ ] **No credential:** the model gets the "not connected" tool error; the MCP result has `isError`; the run continues (the model can answer without the tool).
- [ ] **One refresh:**
  - with an access token about to expire, 50 concurrent calls in one process cause exactly one refresh request;
  - two "replicas" (two vault users over one `BlobStore`) refreshing at once end with one stored token chain: the loser re-reads, and no refresh token is lost.
- [ ] **`invalid_grant`** on refresh gives `ErrNotConnected` and `NeedsReconnect` in `Meta`, and no retry loop: the fake token server counts one request.
- [ ] **Canary, end to end:**
  - seed a canary secret, then run tool calls (success, upstream 401, refresh failure, panic in the tool) over MCP and through the agent with a fake model;
  - the canary never appears in logs (all levels, debug included), SSE events, MCP responses, stored conversation entries, checkpoints or error strings.
- [ ] **Slot isolation:** a factory's source cannot read another slot (type-level), and a spec with an unknown slot fails startup only when it is not `optional`.
- [ ] **Mutation list** recorded (at least: tenant ignored when resolving, refresh without singleflight, CAS conflict treated as an error, `Authorization` header logged at debug level, upstream error body copied into the tool error).

## Notes

- The upstream's error body can echo credentials. Tool errors carry a status and a short reason, never the upstream body.
