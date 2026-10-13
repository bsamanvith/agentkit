# agentkit console: UI plan

> The web client is an **admin console** served by `agentkit` itself (component `web`). Every screen is built only on the public `/v1` API ([docs/api/openapi.yaml](api/openapi.yaml), T062), so Claude Code, scripts and other harnesses can do everything the console does. Decisions: [E11](../tasks/E11-clients.md). Layout, states, components and tokens: [T070](../tasks/T070-console-ux-design.md) (its Notes hold the reasons, the references, the contrast table and the review record). Clickable mockup v3 with sample data, both themes, three widths and every state: [ui/console-mockup.html](ui/console-mockup.html) (also published as an artifact for review). **Approved by the owner on 2026-10-12**, with the review changes in T070 Notes; T055 starts from it. The same day the owner added **Connections** (per-user credentials for connectors, org connections later; see "Connections"), which replaced the admin-only Credentials page. Skills, the agent builder, tool policies, interrupts and tasks are designed in T087 (part 2), on top of this.

## Information architecture

Three columns, each with one job:

| Column | What it holds |
|---|---|
| **Rail** (56 px, icons with labels in a tooltip and for screen readers) | The sections: **Chats**; **Activity** (later, T074; T087 renames it **Tasks**); **Connections** (everyone); a divider; **Agents** and **Toolsets** (admin only; T087 adds **Skills**). At the bottom: **Shortcuts** (`?`) and the **user menu** (name, role, tenant; theme; Memories later; sign out). |
| **List** (288 px desktop, 248 px laptop, collapsible) | The current section's list. In Chats it is **the conversation list**; in Agents the agents; in Toolsets the toolsets; in Activity the runs and threads. Connections has no list (one page). |
| **Content** | The chat, or the selected item's detail. |

Decisions (reasons in T070 Notes):

- **Conversations have one home: the Chats list.** No separate Conversations page and no "Recent" block. The list has **New chat**, **search**, an **agent filter**, date groups (Today, Yesterday, Previous 7 days, Older), a status mark on rows that need attention (running, interrupted, finished while you were elsewhere), and loads more by cursor as you scroll. Each row: the title, then the agent name in mono and the time. A **⋯** menu (Copy ID, Delete) shows on hover or focus, and always on touch.
  - **Search** matches titles. Until `GET /v1/conversations` gains a `q` parameter (asked of T062, see T070 Notes), it filters the loaded rows: its placeholder says "Search loaded chats", and with more pages on the server the results end with **Load older to search more**.
- **Connections are everyone's** (owner, 2026-10-12): each user connects their own key or account for each connector, so the page sits above the divider for every role. Admins add connectors in the server config; the console doesn't. An "Add connector" screen is asked of T087 (T070 Notes). Details: "Connections".
- **Admin pages live in the rail**, under a divider, with their own list column (list on the left, detail on the right, as in Linear and Vercel settings). A `user` sees Chats and Connections (and Activity later). Hiding them is a convenience; the API answers 403 (a direct link shows the 403 page).
- **The agent picker** sits in the chat header's left slot, in both cases:
  - **New chat:** a button showing the chosen agent with a chevron. It opens a searchable list (name, description, and readiness for you: "All 3 toolsets ready", or what is missing, such as "Connect Exa"). The default is the agent of your last new chat (kept in `localStorage`) if it still exists, else `default`, else the first. The empty chat shows the chosen agent's description and readiness above the composer.
  - **Existing conversation:** a **read-only chip** with the agent's name (a conversation belongs to one agent; continuing with another is `409 agent_mismatch`). The chip opens a small card (description, model) with **New chat with researcher**, **New chat with another agent…** and, for admins, **Open agent details**. There is no "switch agent" that silently starts a new chat.
  - **One agent configured:** no picker, just the chip.
  - **"Chat with this agent"** on the Agents page opens a new chat with the picker preset.
- **Later features have a slot now:**
  - **Activity** in the rail (T074): background runs, agent threads, triggers and notifications. Its Runs list is also where E16's jobs land, shown with the same run viewer. T087 renames it **Tasks** and designs the task list, task view and steering.
  - **Approvals** (T030/T037): an inline card in the chat, plus a "Needs you" filter and the Activity badge. T087 designs them with E17's interrupts.
  - **Memories** (E12): from the user menu, as a side panel.
  - **Skills** (T057): on the agent detail now; T087 gives them a rail entry and an editor.
  - **Editing agents and skills** (E15: T075, T077): T087 designs the agent builder. It is a **full-width page that hides the list**, because a builder with a test chat beside it doesn't fit next to the list (at 1200 px the content column is 856 px). The agent detail's **Edit** and **Versions** lead there.

## Layouts

| Width | Shell | Notes |
|---|---|---|
| **Desktop** (≥ 1200 px) | Rail · list 288 px · content | Chat column max 720 px, centred. Admin detail max 880 px. `Mod+Shift+S` or the header's toggle hides the list; the choice is remembered. |
| **Laptop** (768–1199 px, designed at 1024) | Rail · list 248 px · content | The same toggle. Below 900 px the list starts hidden (the content would be under 600 px), unless you showed it. |
| **Phone** (< 768 px, designed at 400) | Top bar · content | No rail. The top bar has the menu button, the agent chip or page title, and New chat. The menu opens a **drawer** with the sections as a **two-row grid of icon and label** (every section fits at 360 px) and the section's list below; picking an item closes it. Opening the drawer does not focus its search (`Mod+K` does), and opening a conversation does not focus the composer, so the keyboard doesn't cover the thread. Admin pages: the list is the page, an item opens its detail with a Back button (Connections has no detail: it is one page of cards); the detail header holds Back and the title, and the status and the page's action sit on a row below. Pickers and menus open as **bottom sheets** with a Close button; a sheet's search is not focused until tapped. 16 px side gutter; the composer sits above the safe area. |

Breakpoints are the same numbers in the console's CSS (Tailwind breakpoints at 768 and 1200; the mockup uses container queries on its frame so it can show all three widths in one page). The console is desktop-first; `agent-ui` components are designed to work at phone width on their own, because consumer apps are mobile-first.

## Routes

Every view has a URL, so links, Back and reloads work. Rail items, list rows and "Chat with this agent" are links (`<a>`), not buttons.

| Route | Shows |
|---|---|
| `/` | The last open conversation, else New chat |
| `/new`, `/new?agent=researcher` | New chat, with the picker preset |
| `/c/{conversation_id}` | A conversation. A new chat moves here when `run_start` gives its ID (replacing `/new` in history) |
| `/agents`, `/agents/{name}` | Agents list, agent detail |
| `/toolsets`, `/toolsets/{name}` | Toolsets list, toolset detail |
| `/connections` | Connections |
| `/connections/done` | Where an account connection returns from the provider. In a popup, it reports the result to the page that opened it and closes. In the same tab, it goes back to the saved route with the result. Opened with no connection pending, it goes to `/connections` (see "Connections") |
| `/activity/…` | Later (T074; T087 defines `/tasks/…`) |

- On a route change the document title becomes "{page} · agentkit" (a conversation's title, "New chat", the agent's name) and focus moves to the page's `h1`, so a screen reader hears where it landed. The exception is **New chat** on desktop (the button or `Mod+Shift+O`): focus goes to the message box, because typing is why you pressed it. Every page has one `h1`; in a chat it is the title in the header.
- On phones, opening a detail pushes a history entry, so the system Back returns to the list.
- An unknown route or ID shows the 404 page; an admin route as a user shows the 403 page.
- Signed out, a deep link shows the sign-in card and then opens the link. The route is kept in the router, not in a `?next=` parameter.
- Sign-in is an overlay over the route, not a route of its own.

## Screens

| Screen | Who | What it shows and does | API | Task |
|---|---|---|---|---|
| **Sign in** | everyone | A small card on the grey ground. The method comes from the server (below, "Sign in"): username and password, an **API token** prompt, or both. One generic error for a wrong password; throttling, empty fields and an unreachable server have their own messages. A 401 later shows the same card over the app with "Your session ended…". | `GET /v1/auth/config` (asked of T061), `POST /v1/auth/login`, `GET /v1/auth/me`, `POST /v1/auth/logout` | T061, T055 |
| **Chat** | user, admin | Header: agent picker or chip, title (`h1`), ⋯ (Copy ID, Delete). The thread: user messages as grey bubbles on the right, answers as plain text, tool calls as quiet steps (below), A2UI surfaces inline, then source chips and a usage line. Composer: Enter sends, Shift+Enter is a new line; while this conversation answers, the send button becomes **Stop** and Enter does nothing (the draft waits). How runs, Stop and drafts behave: "Runs and conversations". | `POST /v1/runs` (SSE), `GET /v1/conversations/{id}`, `GET /v1/agents` | T055 |
| **Chats list** | user, admin | The caller's conversations (see IA). Delete asks first; when the conversation is answering, the confirm says it also stops the answer. | `GET /v1/conversations?agent=&limit=&cursor=`, `DELETE /v1/conversations/{id}` | T055, T062 |
| **Agents** | admin | List: name (mono), description, a status dot with its word for screen readers (ready, or a warning when a toolset it uses is skipped or needs your connection). Detail: description; model and effort; toolsets with status; skills; memory; UI (text or A2UI); the system prompt (admin only, collapsed); **Chat with this agent**. T087 adds Edit and Versions. | `GET /v1/agents`, `GET /v1/agents/{name}` | T064 |
| **Toolsets** | admin | List: name, status dot, tool count. Detail: status (ready, or skipped with the reason), the credential it needs and who provides it ("API key · each user connects their own · server key `EXA_API_KEY` not set"; later the org connection), the MCP URL and a copyable `claude mcp add` command (token placeholder, never the token; a note says the token is an API token from your admin, `AGENTKIT_API_TOKEN` on a single-token server), then each tool as a disclosure with a chevron: name, description, a readable field list (name, type, required, help) and the raw schema on demand. A skipped toolset shows how to fix it instead of the command. The command wraps on phones. | `GET /v1/toolsets`, `GET /v1/toolsets/{name}/tools` | T064 |
| **Connections** | everyone | One row per connector that an agent you can use needs: connector and provider, kind (API key or account), your status, the agents that use it, and the action (Connect, Replace key, Reconnect, Disconnect). Rows that need you come first. Values are never sent to the page. On phones each row is a card. The rail badge counts connectors that need you (see "Connections"). | T045's `GET/PUT/DELETE /v1/credentials`, `POST /v1/credentials/{slot}/connect` (and the asks in T070 Notes) | T064, T045 |
| **Activity** (later) | user (own), admin | List: segments **Runs** and **Threads** (and **Triggers** for admins), a filter (Running, Needs you, Recent), unread dots. Run detail: agent, status (running, needs you, done, failed, **cancelled**), origin (API, schedule, webhook, another agent), elapsed, tokens and cost, **Cancel**, and the live transcript with the same tool steps as chat. Thread detail: participants, hops (3 of 8), budget used, the messages in order, each labelled with its sender agent and linked to its run. Triggers: name, agent, schedule or webhook, next firing, last outcome, Pause/Resume. T087 redesigns this as Tasks. | T071 `GET /v1/runs?background=true`, `GET /v1/runs/{id}/events`; T072; T073 | T074 |
| **403** | everyone | "Admins only": "This page shows server configuration. Ask an admin if you need access." and **Back to chats**. No request lines for users. | | T055 |
| **404** | everyone | "This conversation doesn't exist, was deleted or expired." and **Back to chats**; the list drops the row. | | T055 |

**Agent-made UI (A2UI)**: an agent can show a small form or set of choices inline (the mockup's "Narrow the news search"), rendered with the console's own components. Submitting sends the values back as a `ui_action` part in the next message; that message shows as a labelled summary ("Sent: published after 10 Feb 2026 · topic news · go.dev, news sites"), never as raw JSON. The surface then shows "Sent", keeps the values chosen, and is disabled. "Sent" comes from the conversation (a later `ui_action` for the same surface ID), so it survives a reload. A live surface is disabled while a run is active. If you type a reply instead, the surface shows "Not used" and is disabled. ([E13](../tasks/E13-agent-protocols.md): T065, T066.) Basic-catalog mapping: see Components.

### Sign in

- **Which method:** the console asks `GET /v1/auth/config` (public; asked of T061, see T070 Notes), which lists the methods the server accepts:
  - `password`: the username and password card;
  - `token` only: the token card ("Paste an API token; it's kept for this tab only", in `sessionStorage`);
  - both: the password card with **Use an API token instead**;
  - none (a loopback dev server without auth): no sign-in; the user menu says "Local, no sign-in".
- **Who you are** comes from `GET /v1/auth/me` for both methods (name, role, tenant in the user menu).
- **Errors** (password card):
  - wrong username or password: one generic message, the password is cleared and focused, and both fields are marked invalid;
  - an empty field: "Enter your username" or "Enter your password" under that field, with no request;
  - throttled (429): "Too many attempts. Try again in 30 s.", counting down from `Retry-After`, with the button disabled until then;
  - 5xx or no connection: "Couldn't reach the server. Try again."
- **Errors** (token card): "That token wasn't accepted." A later 401 in token mode returns to the token card with "Your token was rejected or has expired."
- **Submitting:** the button shows a spinner and "Signing in…", the inputs are disabled, and the form is `aria-busy`.
- **Session ended (401):** the card opens over the app with "Your session ended. Sign in again; your draft is kept." as the dialog's description. An error after a failed attempt shows under the fields, and the reason stays. The app behind is `inert`. If the same user signs in (compared with the last `/me`), the draft and route are restored; another user gets a fresh start. A message that got the 401 goes back into the composer as the draft.
- **Sign out** (or **Forget token** in token mode): if an answer is running it asks first, because signing out stops it. It calls `POST /v1/auth/logout`, then clears everything the console holds (drafts, cached lists and threads, the token, the last agent) except the theme, and unmounts the app. If logout fails, it still clears locally and says "Signed out on this device. The server session ends when it expires."

### Connections

A **connector** is a toolset that calls an outside service: Exa, Parallel, GitHub, Gmail. Admins add connectors in the server config. A **connection** is what a connector signs in with: an **API key** you paste, or an **account** you connect through the provider (OAuth).

- **Who owns a connection** (owner, 2026-10-12):
  - **Now: each user, one per connector.** Your connection is used only for your runs (chats and, later, your tasks). Nobody else sees it, admins included.
  - **Later: the org.** An admin can set an **org connection** for a connector, shared with every user by default. If you have your own, **yours wins** for you; disconnect it to fall back to the org's. Per connector, an admin can turn off "Members can use their own", and then only the org's is used.
  - **Server keys** from the environment (`EXA_API_KEY`) stay as the deployment's fallback, used when no one else provides a key. The console shows them as "Server key" and doesn't manage them.
  - **Order at call time:** yours, then the org's (later), then the server key; else the connector works keyless if it can (Tavily, with lower limits), or the call fails as not connected.
- **The page** (`/connections`): a table, one row per connector that an agent you can use needs. Connectors without credentials (the demo tools) aren't listed. Columns: connector (name and provider), kind (API key, account), status, used by (agents), action. Rows that need you come first.
  - **Statuses**, each a dot plus words:
    - **Connected**, with what the server knows: "Your key, added 12 Oct" or "Connected as @samanvith";
    - **Not connected**;
    - **Needs reconnect**: "Access expired or was revoked" (T044 marks it after `invalid_grant`);
    - **Using the server key**;
    - **Keyless**: "Works without a key, with lower limits";
    - later, **Using the org's**, or "Your key (used instead of the org's)".
  - **Actions:** Connect; Replace key; Reconnect; Disconnect. Disconnect confirms: "Exa stops working for you until you connect again." It names the fallback if there is one ("…falls back to the org's key").
  - **Admins, later:** an **Org** column with Set, Replace or Remove, the "Members can use their own" switch, and "4 of 9 members connected" (counts only, never whose).
  - **The rail badge** counts, among connectors an agent you can use needs: those you haven't connected that have no org, server-key or keyless fallback, plus any connection of yours that needs a reconnect.
- **Connecting an API key:** a dialog.
  - Contents: the connector's name; "Get a key at exa.ai" when the connector declares a help link; one password field with show and hide (`autocomplete="off"`, no spellcheck); **Save**.
  - The server checks the key with the provider when the connector can. A rejected key shows "Exa didn't accept this key." under the field, and the field stays for correcting. A connector that can't check saves the key, and a bad one fails at first use.
  - The key is sent once and never shown again, not even in part. The row then reads "Your key, added 12 Oct".
  - Replacing a key uses the same dialog.
- **Connecting an account (OAuth):**
  - **Connect** asks for `POST /v1/credentials/{slot}/connect`, with the console's `/connections/done` as the return URL, and opens the provider in a **popup**.
  - While it is open, the row reads "Waiting for GitHub… Finish in the window that opened", with **Cancel**.
  - The popup ends at `/connections/done`, which tells the page that opened it (`postMessage`, same origin only) and closes.
  - **Results:**
    - connected: "Connected as @samanvith";
    - denied at the provider: "You didn't allow access. Nothing was saved.";
    - expired or failed: "Connecting took too long or failed. Try again.";
    - the popup closed early: back to the previous status, with nothing said.
  - **On phones, or when the popup is blocked,** the same tab goes to the provider. Before it leaves, the console saves the route and drafts in `sessionStorage`. The provider returns to `/connections/done`, which restores that route and shows the result there.
- **In a chat:**
  - **Before sending:** the new-chat agent card and the picker say what is missing ("Connect Exa"), with **Connect** opening the dialog in place. The agent still works with its other tools.
  - **During a run:** a call that needs a connection you don't have fails with `not_connected` (or `needs_reconnect`). The step reads "Exa search needs your connection" in amber, not red. A card under it says "researcher couldn't use Exa because it isn't connected for you." with **Connect Exa**. The run goes on without that tool, because the model gets the error.
  - *API dependency:* these error codes don't exist yet. T036's `tool_result` has only `content` and `is_error`, and T044 raises one `ErrNotConnected`. The asks for T036/T044 are in T070 Notes. Until then, the console can't tell this failure from any other and shows a plain failed step.
  - **After connecting:** the card reads "Exa connected" and offers **Ask again**, which puts your last message back in the composer to send. With E17's interrupts (T087), the run can instead wait for the connection.
- **Never on the page:** values, partial values or key hints. A provider's error text is summarized, not copied. Connection details belong to their owner. Admins see only counts for other users.

## Runs and conversations

A run belongs to its conversation, not to the screen showing it. The console keeps runs in an app-level store keyed by conversation ID.

- **Leaving a conversation doesn't stop its run.** Opening another conversation, starting a new chat or going to an admin page leaves it running. Its row shows a spinner and "Answering". When it ends while you're elsewhere, the row gets an unread dot and the status region says "researcher finished in “Latest Go release”". Each conversation has its own Stop; only that conversation's Stop, or Esc while you're in it, stops it.
- **Closing or reloading the tab** ends the stream, which cancels the run on the server (T036). While a run streams, the browser's "Leave site?" prompt asks first.
- **Deleting a running conversation:** the confirm says "This also stops the running answer". The console stops the run, then deletes. If it was open, you land on New chat with the same agent, with focus on the page's `h1`. Deleting another row moves focus to the next row (or the previous one at the end).
- **Stop:**
  - The console aborts the request (`AbortController`); the server sees the request end and cancels the run (T036). There is no separate cancel call and no WebSocket. `POST /v1/runs/{id}/cancel` (T037) is for runs not tied to this request: another tab, or background tasks.
  - The partial answer stays on screen with "Stopped. This part isn't saved." (unfinished turns aren't saved, T036). Finished tool steps stay; a step that was still running reads "Stopped: searching the web" in neutral grey, not as a failure.
  - The composer stays ready and the next message is sent as usual. The note also offers **Resume** ("let researcher continue"), because the conversation is `interrupted` on the server until the next message.
  - *API dependency (owner decision, 2026-10-12):* today a new message to an interrupted conversation is `409 conversation_interrupted` (T036). The design needs the server to accept it by merging the new message into the open user turn. This is recorded for T036/T037 in T070 Notes. Until then, a 409 here keeps the message in the composer and shows the interrupted notice with Resume.
- **Interrupted:** any run that ended before its answer (a run error, a lost connection, `shutdown`; a user Stop shows the quieter note above). The row gets an amber mark, and the composer shows "The last answer didn't finish. Resume continues from the last saved step, or send a new message." **Resume** is `POST /v1/runs` with the conversation and an empty message (T037: `POST /v1/runs/{id}/resume`). It never adds a user bubble.
- **Retry** on a retryable run error is Resume: it never sends the text again.
- **Drafts are per conversation**, plus one for New chat that survives changing the picker's agent. They live in memory and `sessionStorage`, keyed by user and conversation. They are cleared on send and on sign-out.
- **Waiting:** after Send, a muted "Thinking…" line with a spinner sits under your message until the first event (a step or text).
- **The first message of a new chat:** the message shows pending until `run_start` gives the conversation's ID; then the route and the list update. If the request fails before `run_start` (401, 413, 503, no connection), the message goes back into the composer, the error shows above it, and nothing is added to the list.
- **Titles** are made by the server from the first message (T062). The list shows one line with an ellipsis. The chat header shows the full title (up to two lines on phones), and it is the row's accessible name.

## States

Each has a design in the mockup (the **State** menu in its top bar) or here.

| State | Where | Design |
|---|---|---|
| **No conversations** | Chats list, chat | List: "No conversations yet" and New chat. Content: the new-chat empty state. |
| **No agents** | Chat | "No agents are configured. An admin adds them to the server config (`agents:`)." Composer disabled. Admins get a link to the configuration docs. |
| **Agent removed** | Chat | A conversation whose agent is no longer configured: the chip reads "researcher (removed)", the composer is disabled with "This agent no longer exists, so this conversation is read-only.", and **New chat** is offered. |
| **Toolset skipped** | Toolsets, Agents, picker, new chat | A toolset the server couldn't start (an MCP server that didn't answer, a bad config): amber dot and "Skipped: the MCP server at … didn't answer". The agent shows a warning dot and "1 of 3 toolsets skipped"; the agent still works with the rest. A missing key is not a skip any more; it is a connection (next rows). |
| **Needs your connection** | Connections, picker, new chat, chat | "Not connected" with **Connect**; the picker and the agent card say "Connect Exa"; in a chat, the amber step and the Connect card (see "Connections"). |
| **Needs reconnect** | Connections, picker, chat | "Access expired or was revoked" with **Reconnect**. |
| **Connecting** | Connections | Key: the Save button is busy and the field disabled. Account: "Waiting for GitHub… Finish in the window that opened" with **Cancel**. |
| **Connect failed** | Connections | Key rejected: "Exa didn't accept this key." under the field. Account: "You didn't allow access. Nothing was saved." or "Connecting took too long or failed. Try again." |
| **Loading** | Every list and the thread | Skeleton rows the size of real rows (no spinners for page loads); a thread shows two skeleton messages. The region is `aria-busy` with a hidden "Loading". Nothing shifts when data arrives. |
| **Load failed** | Every list and the thread | A 5xx or no connection on a `GET`: "Couldn't load conversations." (or the page's name) and **Retry**, in place of the skeletons. |
| **Streaming** | Chat | The user message appears at once; "Thinking…" until the first event; a running step reads in the present tense with a spinner; text streams in; Stop replaces Send; `Esc` stops when no menu or sheet is open. Scroll follows only if you are at the bottom; otherwise a **Jump to latest** pill appears. |
| **Stopped** | Chat | "Stopped. This part isn't saved." in muted text after the partial answer, with **Resume**. The composer stays ready (see "Runs and conversations"). |
| **401** | Anywhere | The sign-in card over the app (see "Sign in"). No request loop. |
| **403** | Admin pages | The 403 page: a direct link, or a stale tab after a role change. On a 403 the console re-reads `/v1/auth/me`. If the role changed, it updates the rail and says "Your access changed". It also re-reads `/me` when the tab regains focus, so a promotion shows up. A **CSRF** 403 (a different error code, T061) is never shown as "Admins only": the console re-reads `/me` for a fresh token and retries once. |
| **409 busy** | Chat | A notice above the composer: "This conversation is answering in another tab or client. You can send when it finishes." The textarea stays editable; Send is disabled. **Check again** reads the conversation's status: "Still answering…" if it is running, else the notice goes. |
| **409 interrupted** | Chat, list (amber mark) | See "Runs and conversations": "The last answer didn't finish. Resume continues from the last saved step, or send a new message." **Resume**. |
| **409 agent mismatch** | Chat | Shouldn't happen (the chip prevents it). If it does: "This conversation belongs to researcher." with **New chat with researcher**, keeping the draft. |
| **413 too large** | Composer | "This message is too long to send (the limit is 1 MiB)." The draft is kept. |
| **400 invalid request** | Composer | "The server couldn't read this request." with the error's message. The draft is kept. This is a console bug, so it is logged. |
| **503 over capacity** | Chat | The message stays pending and a line under it says "The server is busy. Retrying in 2 s…" (`Retry-After` is a fixed 2 s). It retries once by itself, then says "Still busy." with **Retry now** and **Cancel** (Cancel returns the message to the composer and keeps any draft you typed meanwhile). The line is announced once; the countdown is `aria-hidden`; the buttons stay in place. Send is disabled while a message is pending. |
| **503 shutting down** | Chat | The same mechanics, with "The server is restarting. Retrying in 2 s…". |
| **Run error** | Chat | After any partial answer, a red-marked block: what happened and what to do, from the table below. A failed tool step reads "… failed" in red with the reason in its details; the run may still finish. |
| **Offline** | Anywhere | A thin bar at the top of the content: "You're offline. Messages will not send until the connection is back." The textarea stays editable; Send is disabled. "Back online" is announced when the bar goes. |
| **Connection lost mid-run** | Chat | Until T071 a closed stream cancels the run, so the honest message is: "Connection lost. The run was stopped on the server. Your messages and finished steps are saved; the unfinished part of this answer isn't." with **Reload conversation**. The server can report the run as running for up to a minute after; Reload then shows the 409 busy notice with Check again. With T071: "Reconnecting…", and the stream resumes from `Last-Event-ID`. |
| **Long content** | Chat, details | A long answer flows normally, code blocks scroll sideways (and take focus, so the keyboard can scroll them), a Jump to latest pill. Tool groups collapse (see Rules, "Tool calls"). A huge result shows the first 20 results or 4,000 characters, then "Show all (1.2 MB)" and **Copy raw JSON**. Long titles end in an ellipsis in the list. |
| **Not found** | Chat | The 404 page and Back to chats; the list drops the row. A 404 on send keeps the draft and offers **New chat with this message**. |

**Run errors** (`run_error` codes, T036). The button follows the error's `retryable` flag, and **Retry** means Resume. Codes that leave the conversation interrupted offer Resume too. An unknown code shows its message and the code, with no button.

| Code | Message | Action |
|---|---|---|
| `rate_limited` | The model provider is rate-limiting requests. Try again in a minute. | Retry |
| `overloaded` | The model provider is overloaded right now. | Retry |
| `timeout` | The answer took longer than the run limit. | Resume |
| `shutdown` | The server restarted while answering. | Resume |
| `unexpected_stop` | The model stopped unexpectedly. | Resume |
| `cancelled` | The answer was stopped. (Seen after a reload.) | Resume |
| `max_tokens` | The answer was cut off at the output limit. Ask it to continue. | — |
| `budget_exceeded` | This run used up its budget (turns or tokens). | — |
| `refused` | The model declined this request, plus its explanation. Rephrase it or ask something else. | — |
| `context_too_long` | This conversation is too long for the model. | New chat |
| `model_auth` | The server's model API key was rejected. Ask an admin. | (admins: a link to the setup docs) |
| `model_rejected` | The model rejected the request, plus the error's message. | — |
| `internal` | Something went wrong on the server. | Copy run ID |

## Components

Built with React, shadcn/ui and Radix (E11, T078). Each component has a home:

- **`ui`** (`web/packages/ui`): generic pieces, mostly shadcn components themed with the tokens below.
- **`agent-ui`** (`web/packages/agent-ui`): agent-specific pieces, shared with future consumer apps. These must work from 360 px up, by touch, without hover-only affordances. The console is desktop-first, but these components are not.
- **`console`** (`web/apps/console`): the console's own pages and layout.

"Ours" means no shadcn component fits, so we build it ourselves.

| Component | Package | Built from | Variants | States |
|---|---|---|---|---|
| **App shell** (rail + list + content) | console | shadcn `Sidebar` in its nested form (an icon rail plus a second sidebar, like the "sidebar-09" block), `SidebarProvider` for the collapse state | desktop; laptop (list collapsed or not); phone (our own drawer: a fixed panel with a transform, `inert` when closed; not shadcn's `Sheet`, whose scroll lock injects a `<style>` the CSP blocks; T078) | offline bar |
| **Skip links** | console | ours | "Skip to content"; in a chat also "Skip to message box" | shown on focus |
| **Rail item** | console | `SidebarMenuButton` as a link + `Tooltip`, `Badge` | icon + tooltip label; badge (count or dot) | default, hover, current (`aria-current="page"`), focus |
| **List item** (conversation) | console | `SidebarMenuItem` (in a `ul`, date groups as headings) + `SidebarMenuAction` (⋯) + `DropdownMenu` | title + agent + time; with a status mark (running spinner, interrupted amber dot, unread dot), each with an accessible name | default, hover, current, focus, menu open, skeleton (`Skeleton`). On touch the ⋯ is always visible |
| **List item** (admin) | console | `SidebarMenuButton` as a link | name + one-line description + status dot (with its word for screen readers) | as above |
| **Agent picker** | agent-ui | `Popover` + `Command` (a combobox: focus stays in the search, arrows move the highlight, Enter picks the highlighted option); a bottom `Drawer` with a Close button on phones | button (new chat), read-only chip with a card (existing; the card is a `Popover`, not a menu), sheet (phone) | closed, open, filtered, no match, one agent (static), agent removed |
| **Composer** | agent-ui | `InputGroup` + `Textarea` + `Button` | idle, with draft, running (Stop; Enter does nothing), Send disabled with a reason (typing still works), fully disabled (no agent, agent removed) | focus, multi-line (grows to 8 lines, then scrolls) |
| **Thread** | agent-ui | ours: a native scroll container (`role="log"`), stick-to-bottom, the Jump to latest pill | | at bottom, scrolled up (pill), loading (`Skeleton`), load failed |
| **Message** | agent-ui | ours; Markdown through an allowlist renderer to React elements | user bubble; assistant text; pending (muted text, no opacity); thinking line; stopped note (with Resume); origin label ("From schedule …"); `ui_action` summary | streaming (`aria-busy="true"`), actions (Copy, also on a stopped answer) |
| **Tool step** | agent-ui | ours, on Radix `Collapsible` | an icon per kind (search, page, calc, tool) | running (shimmer + `Spinner`), done, failed (red), stopped (neutral); collapsed, open (request, results, raw JSON) |
| **Step group** | agent-ui | ours, on `Collapsible` | one call is a plain step; a group forms at the second consecutive call; ≤ 3 calls open by default; > 3 collapsed with a summary | running (shows the latest 5, "Show N earlier"), done ("Show N more"), some failed (failed calls always shown); the open state is kept across updates |
| **Source chip** | agent-ui | ours (a link styled like `Badge`) | site initial + host + path | hover, focus |
| **Usage line** | agent-ui | ours | turns · tokens (cached) · time | |
| **Run notice** (above the composer) | agent-ui | `Alert` + `Button` | info (busy), warning (interrupted), agent removed, with an action | |
| **Inline error** (in the thread) | agent-ui | `Alert` (destructive) | Retry (retryable), Resume, final | |
| **Connect card** (in the thread) | agent-ui | `Alert` + `Button` | not connected, needs reconnect | waiting, connected ("Ask again") |
| **Connection row** | console | `Table` row; a card on phones | API key, account; later with the org column | connected, not connected, needs reconnect, server key, keyless, waiting, using the org's (later) |
| **Connect dialog** (API key) | console | `Dialog` + `Field` + password `Input` | connect, replace | default, saving, rejected |
| **A2UI surface** | agent-ui | ours, rendering the catalog with `ui` components (table below) | | live, disabled while a run is active, sent (keeps the values), not used |
| **Run viewer** (Activity, later jobs) | agent-ui | Thread + Message + Step group, read-only, plus a key/value header | | running, needs you, done, failed, cancelled |
| **Status** | ui | ours (dot + word, or dot + hidden word where space is tight) | ok (green), warn (amber), bad (red), neutral (grey), running (`Spinner`) | |
| **Tag** | ui | `Badge` (outline, mono) | agent or toolset name; with a status dot and its hidden word | at least 24 px tall when clickable |
| **Kbd** | ui | `Kbd` | single key, combination | |
| **Table** | ui | `Table` (no data-table chrome: no sorting or selection yet) | dense rows, header in small caps, status column, action column; on phones each row becomes a card (real cards, not a restyled `tr`) | row hover, empty, skeleton |
| **Detail pane** | console | ours: a header row (`Button`s), a key/value list, sections, `Collapsible` for the system prompt and for tools (with a chevron) | | loading, not found |
| **Empty state** | ui | `Empty` | icon, one sentence, one action | |
| **Toast** | ui | `Sonner` | text, text + action ("Open") | stays 8 s and pauses on hover and focus; announced politely; never the only way to reach something (badges and dots stay) |
| **Tooltip** | ui | `Tooltip` | label, label + shortcut | shown on hover and on keyboard focus; never on disabled controls (their reason is visible text) |
| **Menu** | ui | `DropdownMenu` (with a radio group for the theme); `Drawer` on phones | anchored (desktop), bottom sheet (phone) | Esc, Tab and outside click close it; focus returns to the trigger |
| **Dialog** | ui | `AlertDialog` (Delete, Disconnect), `Dialog` (shortcuts, Connect) | confirm, informational, form | focus trapped, Esc closes |
| **Sign-in form** | console | `Card` + `Field` + `Input` + `Button` | password, token, password with "Use an API token instead", session ended | default, submitting (inputs disabled), error (wrong, empty field, throttled with a countdown, server unreachable) |
| **Button** | ui | `Button` | primary (ink), secondary (`outline`), ghost, icon, link | hover, focus, disabled, busy (`Spinner`); at least 24 × 24 px |
| **Inputs** | ui | `Input`, `InputGroup` (search with `Kbd`), `Select`, `Textarea`, `Checkbox`, `ToggleGroup` (segments) | text, password (with show/hide), search, select, textarea, checkbox, segmented | focus, invalid, disabled |

T087 adds the components its pages need: a diff, a version list, a file tree and the editor fields.

**A2UI basic catalog (v0.9.1)** drawn with these components:

| A2UI | Console component |
|---|---|
| `Text` (`variant` h1–h5, body, caption) | text styles from the type scale (h1–h2 → 16 px semibold, h3–h5 → 14 px semibold, caption → 12 px muted) |
| `Row`, `Column`, `List` | flex rows and columns with the spacing scale |
| `Card` | a surface with a hairline border, 12 px radius |
| `Divider` | a 1 px `--line` rule |
| `Button` (`variant`) | primary or secondary button |
| `TextField` (`shortText`, `longText`, `number`, `date`, `obscured`) | input or textarea |
| `CheckBox` | checkbox |
| `DateTimeInput` | native date and time inputs |
| `ChoicePicker` | toggle chips (several) or a select (one, many options) |
| `Image` | an image from an allowlisted `https` URL, else dropped |
| `Icon` | the console's icon set by name; an unknown name draws nothing |
| `Slider`, `Tabs`, `Modal` | **deferred**: shown as "Unsupported component" until a real use appears |

## Tokens

T078 puts this block in `packages/ui`, prefixed `--ak-` (see the mapping after it). Light is the default; dark applies with the OS setting unless the user picked Light, or always when they picked Dark.

```css
:root {
  /* Surfaces */
  --bg: #f4f4f5;            /* ground: rail, list, sign-in */
  --surface: #ffffff;       /* working surfaces: content, cards, inputs */
  --surface-2: #fafafa;     /* raised inside a surface: details, code */
  --hover: #ececee;         /* row hover on --bg; on --surface use --surface-hover */
  --surface-hover: #f4f4f5;
  --select: #e7e7ea;        /* current row; user bubble */
  /* Text */
  --text: #0a0a0a;
  --text-2: #3f3f46;
  --muted: #63636b;         /* secondary text, meta, placeholders: >= 4.5:1 on every surface */
  --faint: #a1a1aa;         /* decoration only (skeletons), never text */
  /* Lines */
  --line: #e4e4e7;          /* dividers and card edges (decorative) */
  --line-strong: #d4d4d8;   /* secondary button edges (the label identifies the button) */
  --control: #8a8a93;       /* edges of text inputs, selects, checkboxes, the composer: >= 3:1 */
  --focus: #0a0a0a;
  /* Actions */
  --primary: #0a0a0a;
  --on-primary: #ffffff;
  /* Status (dot and the word next to it) */
  --ok: #137333;
  --warn: #9a5800;
  --bad: #b91c1c;
  /* Elevation (popovers, sheets, dialogs, toast) */
  --shadow: 0 8px 24px rgba(0,0,0,.10), 0 1px 3px rgba(0,0,0,.08);
  /* Type */
  --font-ui: "Geist", system-ui, -apple-system, "Segoe UI", sans-serif;
  --font-mono: "Geist Mono", ui-monospace, "SFMono-Regular", Menlo, monospace;
  --text-xs: 12px;  --text-sm: 13px;  --text-md: 14px;  --text-lg: 16px;  --text-xl: 20px;  --text-2xl: 24px;
  --leading: 1.55;  --leading-tight: 1.3;
  --weight: 400;  --weight-medium: 500;  --weight-strong: 600;
  /* Space (4 px grid) */
  --s-1: 4px; --s-2: 8px; --s-3: 12px; --s-4: 16px; --s-5: 20px; --s-6: 24px; --s-8: 32px; --s-10: 40px;
  /* Radius */
  --r-sm: 6px; --r-md: 8px; --r-lg: 12px; --r-full: 999px;
  /* Layout */
  --rail-w: 56px; --list-w: 288px; --list-w-laptop: 248px; --chat-w: 720px; --detail-w: 880px;
  /* Motion */
  --ease: cubic-bezier(.2, 0, 0, 1); --fast: 120ms; --slow: 240ms;
  color-scheme: light;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg: #0b0b0c; --surface: #141415; --surface-2: #1a1a1c;
    --hover: #1c1c1f; --surface-hover: #1c1c1f; --select: #232327;
    --text: #ededee; --text-2: #c8c8cc; --muted: #9a9aa2; --faint: #3a3a40;
    --line: #26262a; --line-strong: #34343a; --control: #6b6b73; --focus: #ededee;
    --primary: #ededee; --on-primary: #0b0b0c;
    --ok: #4ade80; --warn: #facc15; --bad: #f87171;
    --shadow: 0 8px 24px rgba(0,0,0,.5), 0 0 0 1px #26262a;
    color-scheme: dark;
  }
}
:root[data-theme="dark"] {
  --bg: #0b0b0c; --surface: #141415; --surface-2: #1a1a1c;
  --hover: #1c1c1f; --surface-hover: #1c1c1f; --select: #232327;
  --text: #ededee; --text-2: #c8c8cc; --muted: #9a9aa2; --faint: #3a3a40;
  --line: #26262a; --line-strong: #34343a; --control: #6b6b73; --focus: #ededee;
  --primary: #ededee; --on-primary: #0b0b0c;
  --ok: #4ade80; --warn: #facc15; --bad: #f87171;
  --shadow: 0 8px 24px rgba(0,0,0,.5), 0 0 0 1px #26262a;
  color-scheme: dark;
}
```

Every text colour clears **4.5:1** on every surface it can sit on, and control edges, status dots and the focus ring clear **3:1**, in both themes (WCAG 2.2 AA; the table is in T070 Notes). Nothing lowers those ratios with opacity: a dimmed state (a pending message, a sent surface) uses `--muted` text instead. Text is never smaller than `--text-xs` (12 px). Motion respects `prefers-reduced-motion` (no shimmer, no spinner rotation; the word "Running" stays).

**In `packages/ui`.** shadcn's components read their own variable names, so `packages/ui` sets those from ours.

Our names get an `--ak-` prefix there, because shadcn uses some of the same names differently. For example, shadcn's `--muted` is a background, while ours is a text colour.

Radii are set directly rather than derived from `--radius`: `--radius-sm` 6 px, `--radius-md` 8 px, `--radius-lg` 12 px.

| shadcn variable | Our token | Note |
|---|---|---|
| `--background` | `--surface` | the content column |
| `--foreground` | `--text` | |
| `--card`, `--popover` | `--surface` | |
| `--card-foreground`, `--popover-foreground` | `--text` | |
| `--primary` | `--primary` | |
| `--primary-foreground` | `--on-primary` | |
| `--secondary` | `--select` | |
| `--secondary-foreground` | `--text` | |
| `--muted` | `--surface-2` | a background in shadcn |
| `--muted-foreground` | `--muted` | |
| `--accent` | `--surface-hover` | menu and row hover |
| `--accent-foreground` | `--text` | |
| `--destructive` | `--bad` | |
| `--border` | `--line` | |
| `--input` | `--control` | 3:1 input edges |
| `--ring` | `--focus` | |
| `--sidebar` | `--bg` | rail and list |
| `--sidebar-foreground` | `--text-2` | |
| `--sidebar-accent` | `--select` | the current row; hover uses `--hover` |
| `--sidebar-accent-foreground` | `--text` | |
| `--sidebar-border` | `--line` | |
| `--sidebar-ring` | `--focus` | |
| `--sidebar-primary` | `--primary` | |
| `--sidebar-primary-foreground` | `--on-primary` | |

Status colours (`--ak-ok`, `--ak-warn`, `--ak-bad`) and `--ak-shadow` have no shadcn equivalent and stay ours.

## Keyboard

`Mod` is ⌘ on macOS and Ctrl elsewhere. The console matches only that key: `metaKey` on macOS and `ctrlKey` elsewhere, never both (Ctrl+K is "delete to end of line" in a macOS text field). A shortcut never fires with Alt held (AltGr on Windows is Ctrl+Alt) or on key repeat. Letters are matched by `KeyboardEvent.code`, so they work on non-Latin layouts. Controls with a shortcut carry `aria-keyshortcuts`, and the hints show the platform's key (⌘K or Ctrl+K).

| Keys | Does |
|---|---|
| `Mod+Shift+O` | New chat |
| `Mod+K` | Search conversations (opens the drawer on phones) |
| `/` | Focus the message box |
| `Enter` / `Shift+Enter` | Send / new line. Enter does nothing while the conversation is answering |
| `Esc` | Close the topmost menu, sheet, dialog or the drawer; otherwise, in a conversation that is answering, stop it |
| `Mod+Shift+S` | Show or hide the list |
| `?` or `Mod+/` | Keyboard shortcuts |
| `↑` `↓` `Enter` `Esc` | Move, pick and close in the agent picker and menus |

- **Single-key shortcuts** (`/`, `?`) fire only when focus is not in a text field. They can be turned off in the shortcuts dialog (WCAG 2.1.4); the setting is kept in `localStorage`.
- **While a dialog or sheet is open**, only Esc and that dialog's own keys work.
- **List rows** are one Tab stop each; arrow keys move only inside the picker and menus.

## Accessibility

- **Focus order:** rail, list (New chat, search, filter, rows), content (header, thread, composer). **Skip to content** is the first focusable element; in a chat, **Skip to message box** follows it. Every interactive element shows a 2 px `--focus` ring with a 2 px offset. Menus, sheets and dialogs return focus to their trigger; when the trigger is gone (a deleted row), focus goes where "Runs and conversations" says.
- **Routes:** a route change sets the document title and moves focus to the page's `h1` (see Routes).
- **Landmarks:** rail `nav` "Sections", the list column's `nav` named after its section ("Conversations", "Agents", "Toolsets"), content `main`, the thread `role="log"` (`aria-live="off"`) labelled with the conversation title. Conversation rows are a list (`ul`) with the date groups as headings.
- **Overlays:** a closed drawer is `inert`. While the sign-in card is open, the app behind is `inert`; after sign-out it is unmounted.
- **Streaming:** token deltas are **not** announced. One visually hidden `role="status"` region announces:
  - "researcher is answering";
  - each settled tool step ("Searched the web, 1.4 seconds"), and failed ones ("Reading pages failed");
  - "Answer complete", "Stopped", or the error;
  - a run that finished in another conversation.

  The streaming message has `aria-busy="true"` until it ends. `role="alert"` is kept for a run's final error only. Step groups and details are buttons with `aria-expanded`.
- **Status** is never colour alone: a dot plus a word, a visually hidden word where space is tight (list rows, toolset chips), and errors also have an icon.
- **Targets** are at least 24 × 24 px, including link-style buttons and clickable chips (WCAG 2.2 2.5.8); phone rows are 44 px tall.
- **Names:** icon-only buttons have an accessible name and a `Tooltip` that also shows on keyboard focus, not a `title` attribute. A disabled control's reason is visible text.
- **Scrollable regions** (code blocks, raw JSON) are focusable and labelled, so the keyboard can scroll them.
- **Connections:** after the key dialog, Disconnect or an account connection, focus returns to the row's action (in a chat, to the card's button: Ask again after connecting). The key field's error is linked with `aria-describedby` and `aria-invalid`. Waiting, connected, denied and failed are announced in the status region.
- **Contrast**: see Tokens.

## Rules

- **Roles are enforced by the API**, not by hiding pages: a `user` gets 403 on admin data even with a hand-made request. The console hides admin navigation for users only as a convenience.
- **Untrusted content stays text.** Tool results are third-party web pages, rendered as React text children (never `dangerouslySetInnerHTML`). Answers are Markdown, rendered to React elements by a renderer with a fixed element allowlist, no raw HTML and only `http(s)` links. A strict Content-Security-Policy (`default-src 'self'`, no inline scripts) is served with the page, and `/v1` responses carry `Cache-Control: no-store`.
- **Name things by what people recognize**: "Connections", "Toolsets", "Stop", "Resume"; errors say what happened and what to do ("Exa isn't connected for you. Connect Exa").
- **Look (owner, 2026-10-12): neutral.** No brand colour. **Light**: grey ground, white working surfaces, near-black text and primary buttons, used sparingly. **Dark**: not pure black; a near-black ground with slightly lighter surfaces for depth, off-white text and an off-white primary button, so contrast is high without glare. Status is a small coloured dot plus a word (green ready, amber needs you, skipped or interrupted, red error), never a coloured block. Geist for UI, Geist Mono for names, IDs and schemas. Theme follows the OS, with System/Light/Dark in the user menu. Exact values: Tokens.
- **Tool calls** (the part of chat people read most):
  - each call is **one quiet line**: an icon for its kind, a verb phrase people understand ("Searched the web", "Read 2 pages", "Calculated"), the key argument in muted text (the query in quotes, the page hosts), and the time it took. No borders, no JSON, no tool IDs at rest;
  - **while running** the verb reads in the present tense ("Searching the web") with a shimmer and a small spinner; **errors** read "… failed" in red with the reason in the details; a call cut short by Stop reads "Stopped: searching the web" in neutral grey, not as a failure;
  - **groups:** one call is a plain step. Consecutive calls with no text between them form a group under **"Used 3 tools · 2.6 s"** with a thin guide line, from the second call on. A group is open by default for up to 3 calls and collapsed with a summary above that ("Used 20 tools · 1 failed · 34.2 s · searched the web 12 times, read 8 pages"). Open, a finished group shows its first 5 calls and "Show 15 more"; a running group shows its latest 5 and "Show N earlier". Failed calls are always shown, even in a collapsed range. A group the user opened or closed keeps that state while the run goes on;
  - **details** open in place: the request as a key/value list, results as a compact list (site initial, title, host, one-line snippet), and "Show raw JSON" for the exact input and output;
  - the answer ends with **source chips** (host and path) for the `http(s)` links in the final answer, one per URL (tool results alone don't make chips), then a muted line with turns, tokens (cached) and time;
  - display text comes from tool metadata: each tool may declare a title, an icon kind and present/past verbs (MCP's `annotations.title` plus our own optional display fields on `tool.Definition`); tools without them fall back to "Ran `tool_name`" with the first argument.
- **Stack** (owner, 2026-10-12): React + TypeScript with shadcn/ui and Radix, in a `web/` workspace (`ui`, `agent-ui`, `apps/console`); the console build is embedded in the binary; Vitest and Playwright tests against a replayed model and faked connectors (E11, T078). The console is desktop-first; `agent-ui` components also serve mobile-first consumer apps ([PRODUCT.md](PRODUCT.md)).

## Open for the owner

- Brand: keep the neutral "agentkit" look, or apply a product name, logo and colours when the name is final (PLAN open item).
- More in T070 Notes ("Open questions").
