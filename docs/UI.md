# agentkit console: UI plan

> The web client is an **admin console** served by `agentkit` itself (component `web`). Every screen is built only on the public `/v1` API ([docs/api/openapi.yaml](api/openapi.yaml), T062), so Claude Code, scripts and other harnesses can do everything the console does. Decisions: [E11](../tasks/E11-clients.md). Clickable mockup with sample data: [ui/console-mockup.html](ui/console-mockup.html) (also published as an artifact for review).

## Screens

| Screen | Who | What it shows and does | API | Task |
|---|---|---|---|---|
| **Sign in** | everyone | Username and password; one generic error for any failure. Shown when the server has users; otherwise a token prompt. | `POST /v1/auth/login`, `GET /v1/auth/me`, `POST /v1/auth/logout` | T061, T055 |
| **Chat** | user, admin | **Agent picker** in the header (a conversation stays with its agent; switching agents starts a new chat). Streamed answer; each **tool call is a card**: name, short input, running/done/error, then expandable input JSON and result (as text, never HTML). Per turn: turns, tokens (with cache), time. **Stop** cancels the run. Enter sends, Shift+Enter is a new line. An interrupted conversation offers **Resume**. | `POST /v1/runs` (SSE), `GET /v1/agents` | T055 |
| **Conversations** | user, admin | The caller's conversations, newest first: title, agent, messages, updated, status. Search by title, filter by agent, click to continue, "New chat". The sidebar shows the five most recent. | `GET /v1/conversations`, `GET /v1/conversations/{id}`, `DELETE …` | T055, T062 |
| **Agents** | admin | One card per agent: description, model and effort, toolsets, skills, memory. The system prompt (admin only). "Chat with this agent". | `GET /v1/agents`, `GET /v1/agents/{name}` | T064 |
| **MCP toolsets & tools** | admin | Each toolset with status (ready, or skipped with the reason), tool count, its MCP URL and a copyable `claude mcp add` command (token placeholder, never the token). Each tool with description and a readable field list (name, type, required, help), raw schema on demand. | `GET /v1/toolsets`, `GET /v1/toolsets/{name}/tools` | T064 |
| **Credentials** | admin | Per toolset: provider, status (configured, missing, keyless, not needed), source (env var or slot), scope. Values are never sent to the page. v2: set, rotate and delete keys, connect OAuth accounts. | `GET /v1/toolsets` (v1); T045's `/v1/credentials` (v2) | T064, T045 |

Later, as the backend gains them: **approvals** (a run waiting for a decision, with Approve/Reject) and **run history** (T030/T037), a **memories** panel to view and delete what is remembered (E12), and **skills** on the agent page (T057).

## Rules

- **Roles are enforced by the API**, not by hiding pages: a `user` gets 403 on admin data even with a hand-made request. The console hides admin navigation for users only as a convenience.
- **Untrusted content stays text.** Tool results are third-party web pages; answers are Markdown rendered by a renderer that escapes everything, allows a fixed set of elements and only `http(s)` links. A strict Content-Security-Policy (`default-src 'self'`, no inline scripts) is served with the page.
- **Name things by what people recognize**: "Credentials", "Toolsets", "Stop", "Resume"; errors say what happened and what to do ("EXA_API_KEY is not set: set it in the server's environment and restart").
- **Look**: a quiet app shell (left rail, one working pane), IBM Plex Sans for UI and IBM Plex Mono for names, IDs and schemas; one blue accent; status as pills (ready, skipped, missing, interrupted); light and dark themes from the OS setting; works at phone width (the rail becomes a top bar).
- **Stack**: TypeScript without a UI framework, bundled by esbuild through its Go API, embedded in the binary; browser tests with `chromedp` against a replayed model and faked connectors (E11 decisions).

## Open for the owner

- Brand: keep the neutral "agentkit" look, or apply a product name, logo and colours when the name is final (PLAN open item).
