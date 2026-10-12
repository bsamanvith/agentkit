---
id: E11
title: "Clients: web client for people, CLI and headless client for tests and evals"
status: todo
---

# E11: clients

The owner's next step (2026-10-12): deploy the agent API (in Docker first) and test the harness through **real clients**:

- **Web client**, for people: a chat like a terminal agent, plus UI elements for what the run is doing (streamed text, tool calls and results, conversations).
- **CLI client**, interactive in a terminal, plus a **headless** mode for our own scripted agentic tests and the evals (T050, which stays as planned and runs on this client).

Both talk to the public agent API (`/v1`, HTTP + SSE; [E09](E09-agent-api.md)). Neither gets a private path into the server, so whatever the clients can do, any other client can do too.

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
| Browser tests | `chromedp` (pure Go, test-only dependency) drives the preinstalled Chromium against `agentkit serve` with a **replayed** model (the replay server in `cmd/agentkit`'s e2e tests) and faked connectors (T049), so browser tests cost nothing. |

## Tasks

- [ ] [T053](T053-agentclient.md) `pkg/agentclient`: the Go client for `/v1`
- [ ] [T054](T054-cli-client.md) `agentkit chat` and `agentkit ask` (interactive and headless)
- [ ] [T055](T055-web-client.md) Web client: the `web` component, chat UI with tool calls, served from the image

T053 first (T054 and T055 build on it; T055's UI work can start beside it against the API directly). Then T054 and T055 in parallel. After them: T006 (Compose) for multi-container testing, and T050 (evals) on `agentkit ask --json`.
