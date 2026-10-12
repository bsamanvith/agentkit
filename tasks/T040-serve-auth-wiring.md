---
id: T040
title: "agentkit serve behind auth: /v1 and /mcp/, per-principal toolsets"
epic: E07
status: todo
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

- [ ] Every `/v1` and `/mcp/` path (including unknown ones and the listing) without valid credentials is 401. Health, readiness and version are 200 without credentials. (Table test over paths × credential cases, like `TestTokenRequiredOnEveryPath`.)
- [ ] A principal limited to toolset `a`:
  - lists only `a`;
  - can call `a`'s tools over MCP;
  - gets 404 on `/mcp/b`, identical in status, headers and body to a toolset that does not exist.
- [ ] A tool called over MCP sees the principal's tenant and user in its `ctx`.
- [ ] Agent API isolation: a run and conversation created with tenant A's key are 404 with tenant B's key, and B's runs list does not include them.
- [ ] An agent run with a principal that allows only some of the agent's toolsets offers the model only those tools (fake model records the tools it was given).
- [ ] Scopes: a read-only key cannot start a run (403 `forbidden` in the T036 error shape), but can read its tenant's runs.
- [ ] Startup refuses off-loopback with no authenticator for `agent` and for `mcp-server` alone; refuses a glob matching no toolset; `AGENTKIT_API_TOKEN` alone still works (existing tests unchanged).
- [ ] Canary: a key used across all of the above never appears in logs, events or responses.
- [ ] `scripts/try-agent.sh --demo` passes against real Claude, including MCP with the token (session with `ANTHROPIC_KEY`).
- [ ] Mutation list recorded (at least: auth skipped on `/mcp/` listing, toolset check skipped on `tools/call` only, tenant taken from a header, a 403 instead of 404 for a hidden toolset).

## Notes

- `server.WithMiddleware` wraps the whole MCP handler, the listing included (T018 Notes). The per-toolset 404 must also cover `tools/call` and every other method on `/mcp/b`, not just the GET.
- Coordinate with T037: both edit `cmd/agentkit` and `pkg/agentapi`. Whichever starts second rebases onto the other.
