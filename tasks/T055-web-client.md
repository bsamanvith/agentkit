---
id: T055
title: "Console shell, login page, chat with agent picker, conversation list"
epic: E11
status: todo
depends_on: [T036, T053, T070, T078]
---

# T055: console shell and chat

## Context

People need to use the agent without a terminal: a chat like a terminal agent's, with UI for what the run does, in an admin console that later grows the agents, toolsets and credentials pages (T064). Design: [docs/UI.md](../docs/UI.md). Decisions: [E11](E11-clients.md). It is served by `agentkit` itself, so the Docker image is the whole deployment: run it, open `http://localhost:8080`.

## Scope

- **`web` component** (`pkg/web`): serves the embedded bundle at `/` (and its assets), with `Content-Security-Policy: default-src 'self'` (no inline scripts or styles), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`; `index.html` not cached, hashed assets cached. Ready when the bundle is present. `AGENTKIT_COMPONENTS=agent,mcp-server,web`; the image's default config and `scripts/try-agent.sh` turn it on.
- **UI** (React + TypeScript in `web/apps/console`, components from `web/packages/agent-ui` and `web/packages/ui`; build committed in `pkg/web/dist/`, CI checks it is fresh; T078 sets this up):
  - chat: message box (Enter sends, Shift+Enter new line), streamed answer, a **Stop** button that cancels the run (closes the stream);
  - **tool calls** as quiet one-line steps, grouped when several ("Used 3 tools · 2.6 s"), with details in place (request as key/value, results as a compact list, raw JSON on demand), source chips under the answer: exactly as specified in [docs/UI.md](../docs/UI.md) "Tool calls" and the mockup;
  - **neutral themes** per docs/UI.md "Look" (light: grey, white, minimal black; dark: near-black), with a Light/Dark/System switch;
  - per turn: usage (tokens) and time; errors from `run_error` shown inline with a retry button where retryable;
  - **agent picker** for a new chat (`GET /v1/agents`, T062); the conversation header shows its agent;
  - **conversations** in a sidebar from `GET /v1/conversations` (T062; `localStorage` only as a fallback before T062 lands): filter by agent, click to load, "New chat";
  - **login page** (T061) when the server has users; the token prompt otherwise;
  - the **app shell** the admin pages (T064) plug into: navigation, user menu, logout;
  - token prompt on first use (kept in `sessionStorage`), and a clear message on 401;
  - answers rendered as safe Markdown (E11 decision); tool results and errors as text only;
  - works at phone width; light and dark themes from the OS setting.
- **Browser tests** with Playwright (preinstalled Chromium) against `agentkit serve` with the replayed model and faked connectors.

## References

- [docs/UI.md](../docs/UI.md) and T070's approved mockup v3 (the design to build)
- [Vercel AI Chatbot](https://github.com/vercel/ai-chatbot) (streaming chat structure, message parts); [Open WebUI](https://github.com/open-webui/open-webui) (agent picker, sidebar list)
- [shadcn/ui](https://ui.shadcn.com/) and [Radix Primitives](https://www.radix-ui.com/primitives) (components), [Vite](https://vite.dev/)
- [Playwright](https://playwright.dev/) and [Testing Library](https://testing-library.com/) (tests)
- [OWASP XSS Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html) and [CSP](https://developer.mozilla.org/en-US/docs/Web/HTTP/CSP) (rendering untrusted text)
- [WAI-ARIA live regions](https://www.w3.org/WAI/ARIA/apg/) (announcing streamed text politely)

## From T053's handoff (2026-10-12)

- **Streaming:** use `fetch` with a stream reader, not `EventSource`, because runs are POSTs.
- **SSE framing:** port the framing rules and the cases of `pkg/agentclient`'s `TestSSEFraming` to TypeScript, with a line-length limit.
- **Cancel:** `AbortController.abort()` cancels the run, the same as the Go client's `Close()`.
- **Unknown events:** never stop the stream on an unknown event type (the additive rule).
- **Messages:** parse them leniently, as the Go side does since PR #4: ignore unknown fields, and show unknown block types as placeholders.

## From T078's handoff (2026-10-13)

Read T078's Notes "For T055, T064 and T066" first: where components go, `useApi`/`useResource`, the stream helpers, how to test, and the `pkg/web/dist` rebuild rule. Also in scope here:
- **Modal overlays and the CSP.** Radix's modal overlays (Dialog, AlertDialog, Sheet, modal DropdownMenu) lock scrolling with an injected `<style>` that the strict CSP blocks, and `make web-smoke` fails on the violation. Before the first confirm dialog or bottom sheet, replace react-remove-scroll with a CSS-class scroll lock, or use `modal={false}` with our own focus handling. A CSP hash won't work: the style is computed at runtime.
- **The list column's chat parts:**
  - the conversation list itself;
  - chat search and the agent filter in the list head;
  - bottom-sheet menus on phones;
  - the help and shortcuts button, and `Mod+K`.
- **Route names with a dot:** `pkg/web` treats a final path segment with an extension as a file (`/agents/my.agent` is a 404). Make the fallback check only known file extensions, unless agent and toolset names are restricted to exclude dots (decide and record).
- **`web` on by default:** `scripts/try-agent.sh` and docs/QUICKSTART.md use `AGENTKIT_COMPONENTS=agent,web,mcp-server`, and QUICKSTART's environment list names `web` and the console URL.
- **Keep the first load under 240 KB** (`make web-check`). The Markdown renderer and syntax highlighting are `React.lazy` chunks. Measure on "Slow 4G" with a 4× CPU slowdown and record the numbers.
- **Interrupted runs (T100):** a chat run that stops with its user shows `interrupted`, with **Continue** (`POST /v1/runs/{id}/resume`). The stop button calls `POST /v1/runs/{id}/stop`. A new message cancels the interrupted run and starts a new one (T037). Until T037 lands, the API is T036's in-memory one.

## Acceptance criteria

- [ ] In Chromium (Playwright) against `agentkit serve` (replayed model): sending a message streams the answer into the page; a tool call appears as a card with its input and result; a follow-up continues the conversation; reload + clicking the conversation in the sidebar shows the history.
- [ ] Stop during a run cancels it on the server (the run ends `cancelled`) and the UI returns to ready.
- [ ] XSS: a tool result and an answer containing `<script>`, `<img onerror=...>` and `javascript:` links render as text or are dropped; no script runs (a test hook that would fire on injection never does). CSP header present on every page response.
- [ ] Without a token, or with a wrong one: the token prompt and a clear error, no request loop.
- [ ] The committed bundle matches `make web` (CI check); `go build` from a fresh clone needs no Node.js (the committed build).
- [ ] Docker: `make image` then the container with `AGENTKIT_COMPONENTS=agent,mcp-server,web` serves the UI on `:8080`; the image stays within its 20 MB budget (report the new size).
- [ ] Mutation list recorded (at least: Markdown renderer allowing raw HTML, tool result inserted as HTML, CSP header missing, Stop not closing the stream, token stored in `localStorage`).
