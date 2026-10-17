---
id: T040
title: "agentkit serve behind auth: /v1 and /mcp/, per-principal toolsets"
epic: E07
status: done
depends_on: [T039, T036, T018]
---

# T040: `agentkit serve` behind auth

## Context

`/mcp/` is open today and `/v1` has one static token (T036). With T039's middleware, both surfaces authenticate, every request carries a tenant, and a principal sees only its toolsets. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **Config:** an `auth` section: `{"api_keys": [{id, hash, tenant, user?, scopes, toolsets}]}`.
  - Keys may also come from a file named by `AGENTKIT_API_KEYS_FILE` (same JSON), so hashes can live in a mounted secret.
  - `AGENTKIT_API_TOKEN` keeps working as one key: tenant `default`, scopes `*`, toolsets `*`.
- **One `auth.Middleware`** in front of `/v1/` and `/mcp/`. `/healthz`, `/readyz` and `/version` stay open.
- **MCP:**
  - a toolset the principal may not use is **404** (the same answer as an unknown toolset);
  - the `GET /mcp/` listing shows only allowed toolsets;
  - a tool's `ctx` carries the tenant and user.
- **Agent API:**
  - a run uses only the agent's configured toolsets that the principal allows;
  - runs and conversations are kept per tenant, so tenant B cannot read A's run or conversation (404, not 403);
  - scope `agent:run` is needed for `POST /v1/runs`, `agent:read` for reads; `*` grants all.
- **Startup:**
  - fails when `agent` or `mcp-server` listens off loopback with no authenticator;
  - fails when a key's toolset glob matches no configured toolset (catches typos, like `Spec.Include`);
  - loopback with no auth stays allowed, with a warning in the log.
- **Docs:** update `scripts/try-agent.sh` and `docs/QUICKSTART.md` (MCP now needs the token: `claude mcp add --transport http --header "Authorization: Bearer ..."`), and the config reference in T036's Notes. Keep the T036 tests green.

## Out of scope

- JWT (T041). Stateful MCP sessions bound to a principal: if `WithStateful` is used with auth, refuse the combination at startup until that is designed (record why).

## Acceptance criteria

- [x] Every `/v1` and `/mcp/` path (including unknown ones and the listing) without valid credentials is 401. Health, readiness and version are 200 without credentials. (Table test over paths × credential cases, like `TestTokenRequiredOnEveryPath`.)
- [x] A principal limited to toolset `a`:
  - lists only `a`;
  - can call `a`'s tools over MCP;
  - gets 404 on `/mcp/b`, identical in status, headers and body to a toolset that does not exist.
- [x] A tool called over MCP sees the principal's tenant and user in its `ctx`.
- [x] Agent API isolation: a run and conversation created with tenant A's key are 404 with tenant B's key, and B's runs list does not include them.
- [ ] ~~An agent run with a principal~~ (replaced; see Notes, "Agents a key may use")
  An agent run with a principal that allows only some of the agent's toolsets offers the model only those tools (fake model records the tools it was given).
- [x] Scopes: a read-only key cannot start a run (403 `forbidden` in the T036 error shape), but can read its tenant's runs.
- [x] Startup refuses off-loopback with no authenticator for `agent` and for `mcp-server` alone; refuses a glob matching no toolset; `AGENTKIT_API_TOKEN` alone still works (existing tests unchanged).
- [x] Canary: a key used across all of the above never appears in logs, events or responses.
- [ ] `scripts/try-agent.sh --demo` passes against real Claude, including MCP with the token (session with `ANTHROPIC_KEY`).
- [x] Mutation list recorded (at least: auth skipped on `/mcp/` listing, toolset check skipped on `tools/call` only, tenant taken from a header, a 403 instead of 404 for a hidden toolset).

## Notes

- `server.WithMiddleware` wraps the whole MCP handler, the listing included (T018 Notes). The per-toolset 404 must also cover `tools/call` and every other method on `/mcp/b`, not just the GET.
- Coordinate with T037: both edit `cmd/agentkit` and `pkg/agentapi`. Whichever starts second rebases onto the other.

### Done (2026-10-17)

- `pkg/mcp/server.WithAccess`: a toolset the request may not use is answered exactly as an unknown one (404 for every method, after the middleware), and `GET /mcp/` leaves it out.
- `pkg/agentapi`: `WithAuthenticator` (any `auth.Authenticator`; `WithToken` is now one key, tenant `default`, scopes and toolsets `*`). Every route needs scope `agent:read` (GET) or `agent:run` (anything else), else 403 `forbidden`. The tenant comes from the principal (`WithTenant` still wins when set). Agents and toolsets the key may not use are left out of the catalog and are 404 everywhere else.
- `cmd/agentkit`: `auth.api_keys` in the config and `AGENTKIT_API_KEYS_FILE` (same JSON, unknown fields refused), plus `AGENTKIT_API_TOKEN`; one authenticator for `agent` and `mcp-server`. Startup fails off loopback with none, on a toolset pattern matching none of `mcp.toolsets`, and on `mcp.stateful` with credentials.
- Tests: `pkg/agentapi/access_test.go` (paths × credentials, tenants, scopes, toolsets, keys never echoed), the OpenAPI test probes 403 on every route, `cmd/agentkit` `TestE2EServeAuth` (the binary, three kinds of key, MCP and API), serve config cases. A tool sees the tenant and user in `ctx`: `auth.Middleware` puts them there and `TestMiddleware` (pkg/mcp/server) shows a tool gets what the middleware sets.

### Agents a key may use (decision)

The scope asked that a run use only the agent's toolsets the key allows. Done that way, the allowed set has to travel with the run: into the run record, through recovery after a crash, and over the runner RPC, and a resumed run would need the key's set again. Instead an agent is usable (listed, runnable, its conversations continued, its runs resumed) only by a key that allows **all** its toolsets; to any other key it does not exist (404, or 400 "no agent" naming the visible ones). The effect for a narrow key is safe (it can never reach a tool it may not use) and simple to reason about; the cost is that it cannot use a wide agent at all. Follow-up if that is needed: carry the allowed set on the run and filter tools in the runner.

### Stateful MCP

`mcp.stateful` with credentials is refused at startup: a session would outlive the request that authenticated it, and binding sessions to a principal (and refusing another principal's session id) is not designed yet.

### Mutation list (each caught by a test)

- scope check skipped (`TestScopes`, OpenAPI 403 probes)
- hidden toolset's tools listed in `/v1/toolsets/{name}/tools` (`TestToolsetsOfAKey`)
- tenant taken from somewhere other than the principal (`TestTenantsDoNotSeeEachOther`)
- continuing a conversation of a hidden agent (`TestToolsetsOfAKey`)
- MCP toolset check skipped, so `tools/call` on a hidden toolset works (`TestAccess`, `TestE2EServeAuth`)
- MCP listing not filtered (`TestAccess`, `TestE2EServeAuth`)
- 403 instead of 404 for a hidden toolset (`TestAccess` compares status, headers and body with an unknown toolset)

### Left

- `scripts/try-agent.sh --demo` against real Claude: not run (no Anthropic key in the session). The MCP step now sends the token. `make smoke` (fake model, the console's token path) passes.

