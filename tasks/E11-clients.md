---
id: E11
title: "Clients: the web console, CLI and headless client, and the APIs behind them"
status: todo
---

# E11: clients, console and APIs

The owner's next step (2026-10-12): deploy the agent API (in Docker first) and test the harness through **real clients**:

- **Web client**, for people: a chat like a terminal agent, plus UI elements for what the run is doing (streamed text, tool calls and results, conversations).
- **CLI client**, interactive in a terminal, plus a **headless** mode for our own scripted agentic tests and the evals (T050, which stays as planned and runs on this client).

Both talk to the public agent API (`/v1`, HTTP + SSE; [E09](E09-agent-api.md)). Neither gets a private path into the server, so whatever the clients can do, any other client can do too.

**Update (owner, 2026-10-12): the web client is an admin console.** Pages:
- **login**: simple user login;
- **chat**: with an **agent picker**;
- **conversations**: a list where you pick a conversation or start one with any agent;
- **agents**;
- **MCP toolsets and their tools**;
- **credentials**.

**API first:** every page is built only on documented `/v1` endpoints, so the harness can be driven and tested the same way from Claude Code or any other CLI harness. Agents are also exposed **over MCP**, so Claude Code can list agents, run them and read conversations as tools.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| One client library | **`pkg/agentclient`** (T053), a Go client for `/v1`: start a run (streamed or JSON), typed events, continue a conversation, read runs and conversations, typed errors (`conversation_busy`, `over_capacity` with `Retry-After`, ...), cancel by closing the stream. The CLI, the headless mode, `cmd/agentkit`'s e2e tests and the evals all use it, so the API has one tested client. |
| Wire types | One definition, shared by server and client. If importing `pkg/agentapi` would pull the harness into clients, T053 moves the wire types (`Event`, `Run`, `Conversation`, `ErrorResponse`) to `pkg/agentapi/wire` and keeps T036's golden tests green. |
| CLI shape | Subcommands of the one binary (no second binary): **`agentkit chat`** (interactive) and **`agentkit ask`** (one prompt, headless). Server URL and token come from `--server`/`AGENTKIT_SERVER` and `AGENTKIT_API_TOKEN`. |
| Headless output | `agentkit ask --json` prints one event per line (JSONL, the same JSON as the SSE `data`), then exits 0 on `run_done`, 1 on `run_error`, 2 on a usage or HTTP error. Scripts and evals parse it; humans use `chat`. |
| Where the web client lives | **Served by `agentkit` itself**, as a `web` component (`AGENTKIT_COMPONENTS=agent,mcp-server,web`): static files embedded in the binary (`go:embed`), at `/` on the same origin as `/v1` (no CORS). One image, so "run the container, open localhost:8080" is the whole setup. |
| Web stack | **TypeScript, no UI framework, bundled by esbuild through its Go API** (`go run ./internal/webbuild`, `make web`). No Node.js toolchain in the repo or the image, and one small dependency (esbuild, build-time only). The bundle is committed under `pkg/web/dist/` (so `go build` works from a fresh clone); CI fails if it is stale (like `tidy-check`). |
| Untrusted content in the web client | Tool results are third-party web pages (search, extract): rendered **as text**, never as HTML. Model answers are Markdown, rendered by a small renderer that escapes everything and allows only a fixed set of elements and `http(s)` links. A Content-Security-Policy (`default-src 'self'`, no inline script) is served with the page. |
| Auth in the web client before E07 | The user pastes the API token once; it is kept in `sessionStorage` (cleared with the tab), sent as `Authorization: Bearer`. E07's OIDC login replaces this later. |
| Console = API client | Every console page uses only public `/v1` endpoints, described in **`docs/api/openapi.yaml`**. A test checks that the server's routes and responses match the spec, so the spec is what Claude Code, scripts and other harnesses can rely on. No endpoint exists only for the console. |
| Several agents | Config moves from one `agent` to **`agents: [{name, ...}]`**; a single `agent` section is still accepted, as the agent named `default`. A conversation belongs to one agent; a run names the agent, or continues the conversation's. `GET /v1/agents` lists them (T062). |
| Agents over MCP | An `agentkit` MCP toolset at `/mcp/agentkit` with `agents_list`, `agent_run` (agent, message, conversation_id?; returns answer, run and conversation IDs, a tool-call summary), `conversations_list` and `conversation_get`. `claude mcp add` against it lets Claude Code drive and test our agents (T063). Same auth as every MCP path (T040). |
| Login | **Local users** for now: config holds username, an argon2id password hash, tenant and role (`admin` or `user`). Login creates a session cookie (`HttpOnly`, `SameSite=Strict`, `Secure` over TLS); unsafe methods with a cookie also need an `Origin` check and a CSRF header. API clients keep bearer keys. Built on E07's `pkg/auth` (T039) as one more authenticator (T061); OIDC (T041) later. Until T061 lands the console uses the token prompt. |
| Roles | `user`: chat, own conversations, agent picker. `admin`: also the agents, toolsets and credentials pages. Enforced by the API (scopes from T039/T040), not by hiding pages. |
| Credentials page | **v1:** each toolset's credential status (configured, missing, keyless), which env var or slot it uses, and the readiness reason. Values are never shown. **v2**, after E07 T043–T045: set, rotate and delete keys, and connect OAuth accounts, through T045's endpoints. |
| Browser tests | `chromedp` (pure Go, test-only dependency) drives the preinstalled Chromium against `agentkit serve` with a **replayed** model (the replay server in `cmd/agentkit`'s e2e tests) and faked connectors (T049), so browser tests cost nothing. |

## Tasks

- [x] [T053](T053-agentclient.md) `pkg/agentclient`: the Go client for `/v1` (wire types moved to `pkg/agentapi/wire`; read its "For T054" and "For T055" Notes first)
- [ ] [T054](T054-cli-client.md) `agentkit chat` and `agentkit ask` (interactive and headless)
- [ ] [T062](T062-api-agents-catalog.md) API: several agents, conversation listing, toolsets and tools catalog, OpenAPI spec
- [ ] [T063](T063-agents-over-mcp.md) Agents over MCP (`/mcp/agentkit`), so Claude Code and other harnesses can drive them
- [ ] [T061](T061-local-users-login.md) Local users and login sessions (E07 track, needs T039)
- [ ] [T070](T070-console-ux-design.md) Console UX design session: layout, navigation, states, components, mockup v3 (before T055)
- [ ] [T055](T055-web-client.md) Console shell, login, chat with agent picker, conversation list
- [ ] [T064](T064-console-admin-pages.md) Console admin pages: agents, toolsets and tools, credentials

Order: T053 and T062 now (independent), and T039 (E07) now for login. Then T054 (CLI), T063 (MCP) and T055 (console) in parallel, then T061 (login) and T064 (admin pages). The UI design (screens and flows) is in [docs/UI.md](../docs/UI.md); a dedicated design session (T070) settles the layout and produces mockup v3 for the owner's approval before T055 starts. After that: T006 (Compose) and T050 (evals, on `agentkit ask --json` and `/mcp/agentkit`).
