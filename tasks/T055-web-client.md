---
id: T055
title: "Web client: the web component, a chat UI with tool calls, served from the image"
epic: E11
status: todo
depends_on: [T036]
---

# T055: web client

## Context

People need to use the agent without a terminal: a chat like a terminal agent's, with UI for what the run does ([E11](E11-clients.md)). It is served by `agentkit` itself, so the Docker image is the whole deployment: run it, open `http://localhost:8080`.

## Scope

- **`web` component** (`pkg/web`): serves the embedded bundle at `/` (and its assets), with `Content-Security-Policy: default-src 'self'` (no inline scripts or styles), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`; `index.html` not cached, hashed assets cached. Ready when the bundle is present. `AGENTKIT_COMPONENTS=agent,mcp-server,web`; the image's default config and `scripts/try-agent.sh` turn it on.
- **UI** (TypeScript, no framework, esbuild via its Go API: `internal/webbuild`, `make web`; bundle committed in `pkg/web/dist/`, CI checks it is fresh):
  - chat: message box (Enter sends, Shift+Enter new line), streamed answer, a **Stop** button that cancels the run (closes the stream);
  - **tool calls** as cards: tool name, input (collapsed JSON), status (running, done, error), result (collapsed, plain text, truncated with "show all");
  - per turn: usage (tokens) and time; errors from `run_error` shown inline with a retry button where retryable;
  - **conversations** in a sidebar: kept in `localStorage` (id, title from the first message, time); click to load (`GET /v1/conversations/{id}`), "New chat";
  - token prompt on first use (kept in `sessionStorage`), and a clear message on 401;
  - answers rendered as safe Markdown (E11 decision); tool results and errors as text only;
  - works at phone width; light and dark themes from the OS setting.
- **Browser tests** with `chromedp` (test-only dependency) against `agentkit serve` with the replayed model and faked connectors.

## Acceptance criteria

- [ ] In Chromium (chromedp) against `agentkit serve` (replayed model): sending a message streams the answer into the page; a tool call appears as a card with its input and result; a follow-up continues the conversation; reload + clicking the conversation in the sidebar shows the history.
- [ ] Stop during a run cancels it on the server (the run ends `cancelled`) and the UI returns to ready.
- [ ] XSS: a tool result and an answer containing `<script>`, `<img onerror=...>` and `javascript:` links render as text or are dropped; no script runs (a test hook that would fire on injection never does). CSP header present on every page response.
- [ ] Without a token, or with a wrong one: the token prompt and a clear error, no request loop.
- [ ] The committed bundle matches `make web` (CI check); `go build` from a fresh clone needs no Node.js.
- [ ] Docker: `make image` then the container with `AGENTKIT_COMPONENTS=agent,mcp-server,web` serves the UI on `:8080`; the image stays within its 20 MB budget (report the new size).
- [ ] Mutation list recorded (at least: Markdown renderer allowing raw HTML, tool result inserted as HTML, CSP header missing, Stop not closing the stream, token stored in `localStorage`).
