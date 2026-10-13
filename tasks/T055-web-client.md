---
id: T055
title: "Console shell, login page, chat with agent picker, conversation list"
epic: E11
status: review
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

- [x] In Chromium (Playwright) against `agentkit serve` (replayed model): sending a message streams the answer into the page; a tool call appears as a card with its input and result; a follow-up continues the conversation; reload + clicking the conversation in the sidebar shows the history.
- [x] Stop during a run cancels it on the server (the run ends `cancelled`) and the UI returns to ready.
- [x] XSS: a tool result and an answer containing `<script>`, `<img onerror=...>` and `javascript:` links render as text or are dropped; no script runs (a test hook that would fire on injection never does). CSP header present on every page response.
- [x] Without a token, or with a wrong one: the token prompt and a clear error, no request loop.
- [x] The committed bundle matches `make web` (CI check); `go build` from a fresh clone needs no Node.js (the committed build).
- [ ] Docker: `make image` then the container with `AGENTKIT_COMPONENTS=agent,mcp-server,web` serves the UI on `:8080`; the image stays within its 20 MB budget (report the new size).
- [x] Mutation list recorded (at least: Markdown renderer allowing raw HTML, tool result inserted as HTML, CSP header missing, Stop not closing the stream, token stored in `localStorage`).

## Notes

### Status: review

Three things keep this in `review`:

- **The image criterion.** `make image` ran, and the container with `AGENTKIT_COMPONENTS=agent,mcp-server,web` served the console on `:8080` (`/readyz` lists `web` ready; `/c/conv.1` is the app with the CSP). But the image is 22,162,944 bytes, over the 20 MB the criterion names. The budget CI enforces is 30 MB (`IMAGE_BUDGET_MB` in the Makefile), and the base of this branch (`e197728`) was already 22,097,408 bytes, so this task adds 65,536 bytes. The criterion stays unticked until the owner confirms the 30 MB budget is the one meant.
- **Login page (T061)** isn't built: the server has no users yet. The token prompt is the only sign-in.
- **Stop and Resume use T036's API**, not T037's `POST /v1/runs/{id}/stop` and `/resume`, which haven't landed (see Decisions).

### Results

- **Chat.** `/new` and `/c/{id}`: the agent picker (or chip), the thread, the composer, Stop, and the ⋯ menu (Copy ID, Delete). Answers stream as safe Markdown, a lazy chunk. Tool calls are steps, grouped and windowed, with details in place. Source chips and a usage line ("2 turns · 1,134 tokens (1,000 cached) · 3.2 s") sit under each answer. `run_error` shows inline with Retry or Resume.
- **Runs live in an app-level store** keyed by conversation, above the router (`chat/runs.ts`). A run goes on while another conversation or an admin page is open, and its list row shows a spinner, then a "New answer" dot. Text deltas are applied once per animation frame. A page with a run asks before closing.
- **New chat** creates the conversation on the first send (T092), then navigates to `/c/{id}` while the run streams. The agent is picked, then `?agent=`, then the last one used (localStorage), then the server's default.
- **Errors before a run starts** put the message back in the composer, after anything typed meanwhile. A 401 opens the token prompt. A 503 keeps the message pending and retries once after 2 s, then shows "Still busy." with Retry now and Cancel. Drafts are per conversation, in sessionStorage.
- **The Chats list**: search over the loaded rows (`Mod+K`), an agent filter, date groups (Today, Yesterday, Previous 7 days, Older), status marks (answering, interrupted, new answer) and Load more. Conversations that were created but never sent aren't listed. Keys: `Mod+Shift+O` new chat, `/` to the composer, Esc stops.
- **Accessibility**: the thread is `role="log"` with `aria-live="off"`, and one polite status region announces "X is answering", "Answer complete", "Stopped", a failed tool and errors. There is a "Skip to message box" link.
- **pkg/web**: only paths ending in a file's extension (the build's types, plus `.map`, image, font, `.xml`, `.webmanifest` and `.wasm`) are files. Any other path is an app route, so `/agents/research.v2` and `/c/conv.1` work on reload. Agent and toolset names keep their dots.
- **Quickstart**: `scripts/try-agent.sh` and docs/QUICKSTART.md serve `agent,web,mcp-server` and say to open the console. The Dockerfile is unchanged: `AGENTKIT_COMPONENTS` is set at `docker run`, as before.
- **Size**: the first load is 216,407 bytes (52,528 of fonts), against 210,256 at `e197728` and the 240 KB budget. The chat page is a lazy 30 KB chunk and Markdown a 5 KB one.
- **Speed**, with Lighthouse's mobile throttling (150 ms RTT, 1.6 Mbps down, 4× CPU slowdown) against a local server, three runs each: `/new` to a usable composer took 1.57 to 1.65 s, and a conversation to its history took 2.07 s.
- **Tests**:
  - Unit tests: 16 in `ui`, 128 in `agent-ui` and 78 in the console. They cover the run store, the chat page against a fake server and the Chats list.
  - `make web-smoke`: 8 tests, the 4 smoke tests and 4 chat tests. The chat tests run `agentkit serve` against `e2e/fake-model.ts`, a fake Anthropic Messages API that streams a tool call, hostile HTML, a stalled answer or an echo. They cover a new chat with a tool card (input and result), a follow-up, the conversation from the list after a reload, Stop ending the run `failed/cancelled` and its model request, hostile tool input, results and answers staying text with no `window.__xss` under the CSP, and a wrong token prompting with no request loop.
  - `go build ./cmd/agentkit` works from a fresh clone with no `node` on `PATH`.

### Mutations

Each mutation was applied by hand, the tests were run, and the source was restored. The browser-test column was run on a rebuilt bundle.

| Mutation | Unit tests | Browser tests |
|---|---|---|
| Markdown text inserted as raw HTML | fail (agent-ui, console) | fail (hostile output) |
| `javascript:` links allowed | fail (agent-ui, console) | not run |
| Tool result inserted as HTML | fail (agent-ui, console) | not run |
| Stop doesn't abort the request | fail (console) | fail (Stop) |
| Token in localStorage | fail (console) | not run |
| CSP header missing | fail (`go test ./pkg/web`) | fail (hostile output) |
| Dotted paths are files again | fail (`go test ./pkg/web`) | not run |

### Decisions

- **The fake model replaces cassettes for the browser tests.** A small Node server picks its answer from the last message, so a test can stall a run or send hostile text. Recorded cassettes can't do either. It speaks the API's wire shape (see `cmd/agentkit/testdata/e2e`).
- **Stop aborts the request; Resume and Retry run with an empty message** (T036). A new message to an interrupted conversation gets 409 `conversation_interrupted`: the message goes back to the composer and the notice offers Resume. When T037 lands, Stop should call `POST /v1/runs/{id}/stop`, Resume should call `/resume`, and a new message should cancel the interrupted run.
- **History has no usage or errors**, so this tab keeps both per exchange and lays them over the history. After a reload, answers show no usage line.
- **Delete uses `window.confirm`** for now. A Radix AlertDialog needs the scroll-lock change T078 describes.
- **Answers carry `data-run-id`**, for support and the Stop test.

### Deferred

- The `?` help and shortcuts dialog, and bottom-sheet menus on phones (both need the scroll-lock change).
- A combobox agent picker for many agents (a radio menu for now), and a ⋯ menu on list rows.
- Syntax highlighting in code blocks.

### For T064

- The run store (`chat/runs.ts`, via `useRuns`) is app-wide. An admin page that starts runs can use it, and `anyRunning()` says whether leaving loses one.
- `ListHead` takes a `below` slot (the Chats search sits there). `Page` takes `lead` (before the title) and `autoFocus`.
- `chat/shared.ts` has `runErrorText` (the docs/UI.md table) and `dateGroup`.
- The fake model (`e2e/fake-model.ts`) is shared: add a script keyed on a word in the message.

