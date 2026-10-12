# agentkit console: UI plan

> The web client is an **admin console** served by `agentkit` itself (component `web`). Every screen is built only on the public `/v1` API ([docs/api/openapi.yaml](api/openapi.yaml), T062), so Claude Code, scripts and other harnesses can do everything the console does. Decisions: [E11](../tasks/E11-clients.md). Layout, states, components and tokens: [T070](../tasks/T070-console-ux-design.md) (its Notes hold the reasons, the references and the contrast table). Clickable mockup v3 with sample data, both themes, three widths and every state: [ui/console-mockup.html](ui/console-mockup.html) (also published as an artifact for review).

## Information architecture

Three columns, each with one job:

| Column | What it holds |
|---|---|
| **Rail** (56 px, icons with labels on hover and for screen readers) | The sections: **Chats**; **Activity** (later, T074); a divider; **Agents**, **Toolsets**, **Credentials** (admin only). At the bottom: **Shortcuts** (`?`) and the **user menu** (name, role, tenant; theme; Memories later; sign out). |
| **List** (288 px desktop, 248 px laptop, collapsible) | The current section's list. In Chats it is **the conversation list**; in Agents the agents; in Toolsets the toolsets; in Activity the runs and threads. Credentials has no list (one table). |
| **Content** | The chat, or the selected item's detail. |

Decisions (reasons in T070 Notes):

- **Conversations have one home: the Chats list.** No separate Conversations page and no "Recent" block. The list has **New chat**, **search** (titles), an **agent filter**, date groups (Today, Yesterday, Previous 7 days, Older), a status mark on rows that need attention (running, interrupted), and loads more by cursor as you scroll. Each row: the title, then the agent name in mono and the time. Hover or focus shows a **⋯** menu (Copy ID, Delete).
- **Admin pages live in the rail**, under a divider, with their own list column (list on the left, detail on the right, as in Linear and Vercel settings). A `user` sees only Chats (and Activity later). Hiding them is a convenience; the API answers 403 (a direct link shows the 403 page).
- **The agent picker** sits in the chat header's left slot, in both cases:
  - **New chat:** a button showing the chosen agent with a chevron. It opens a searchable list (name, description, toolset readiness such as "2 of 3 toolsets ready"). The default is the agent of your last new chat (kept in `localStorage`), else `default`, else the first. The empty chat shows the chosen agent's description and readiness above the composer.
  - **Existing conversation:** a **read-only chip** with the agent's name (a conversation belongs to one agent; continuing with another is `409 agent_mismatch`). The chip opens a small card (description, model) with **New chat with researcher** and **New chat with another agent…**. There is no "switch agent" that silently starts a new chat.
  - **One agent configured:** no picker, just the chip.
  - **"Chat with this agent"** on the Agents page opens a new chat with the picker preset.
- **Later features have a slot now:**
  - **Activity** in the rail (T074): background runs, agent threads, triggers and notifications. Its Runs list is also where E16's jobs land, shown with the same run viewer.
  - **Approvals** (T030/T037): an inline card in the chat, plus a "Needs you" filter and the Activity badge.
  - **Memories** (E12): from the user menu, as a side panel.
  - **Skills** (T057): on the agent detail.
  - **Editing agents and skills** (E15: T075, T077): the agent detail gains **Edit** and a **Versions** list (each change is a version you can roll back). The page keeps its list + detail shape.

## Layouts

| Width | Shell | Notes |
|---|---|---|
| **Desktop** (≥ 1200 px) | Rail · list 288 px · content | Chat column max 720 px, centred. Admin detail max 880 px. |
| **Laptop** (768–1199 px, designed at 1024) | Rail · list 248 px · content | `Mod+Shift+S` or the header's toggle hides the list; the choice is remembered. |
| **Phone** (< 768 px, designed at 400) | Top bar · content | No rail. The top bar has the menu button, the agent chip or page title, and New chat. The menu opens a **drawer** with the sections as a row of tabs at the top and the section's list below; picking an item closes it. Admin pages: the list is the page, an item opens its detail with a Back button. Pickers and menus open as **bottom sheets**. 16 px side gutter; the composer sits above the safe area. |

Breakpoints are the same numbers in the console's CSS (Tailwind breakpoints at 768 and 1200; the mockup uses container queries on its frame so it can show all three widths in one page). The console is desktop-first; `agent-ui` components are designed to work at phone width on their own, because consumer apps are mobile-first.

## Screens

| Screen | Who | What it shows and does | API | Task |
|---|---|---|---|---|
| **Sign in** | everyone | A small card on the grey ground: username, password, Sign in; one generic error ("Wrong username or password."). Shown when the server has users; otherwise an **API token** prompt (kept in `sessionStorage`, cleared with the tab). A 401 later shows the same card with "Your session ended. Sign in again; your draft is kept." | `POST /v1/auth/login`, `GET /v1/auth/me`, `POST /v1/auth/logout` | T061, T055 |
| **Chat** | user, admin | Header: agent picker or chip, title, ⋯ (Copy ID, Delete). The thread: user messages as grey bubbles on the right, answers as plain text, tool calls as quiet steps (below), A2UI surfaces inline, then source chips and a usage line. Composer: Enter sends, Shift+Enter is a new line; the send button becomes **Stop** while a run streams. | `POST /v1/runs` (SSE), `GET /v1/conversations/{id}`, `GET /v1/agents` | T055 |
| **Chats list** | user, admin | The caller's conversations (see IA). Delete asks first. | `GET /v1/conversations?agent=&limit=&cursor=`, `DELETE /v1/conversations/{id}` | T055, T062 |
| **Agents** | admin | List: name (mono), description, a status dot (ready, or a warning when a toolset it uses is skipped). Detail: description; model and effort; toolsets with status; skills; memory; UI (text or A2UI); the system prompt (admin only, collapsed); **Chat with this agent**. | `GET /v1/agents`, `GET /v1/agents/{name}` | T064 |
| **Toolsets** | admin | List: name, status dot, tool count. Detail: status (ready, or skipped with the reason), the MCP URL and a copyable `claude mcp add` command (token placeholder, never the token), then each tool: name, description, a readable field list (name, type, required, help) and the raw schema on demand. | `GET /v1/toolsets`, `GET /v1/toolsets/{name}/tools` | T064 |
| **Credentials** | admin | One table: toolset, provider, status (configured, missing, keyless, not needed), source (env var or slot), scope, and how to fix a missing one. Values are never sent to the page. v2: set, rotate and delete keys, connect OAuth accounts (the buttons are drawn disabled until then). | `GET /v1/toolsets` (v1); T045's `/v1/credentials` (v2) | T064, T045 |
| **Activity** (later) | user (own), admin | List: segments **Runs** and **Threads** (and **Triggers** for admins), a filter (Running, Needs you, Recent), unread dots. Run detail: agent, status, origin (API, schedule, webhook, another agent), elapsed, tokens and cost, **Cancel**, and the live transcript with the same tool steps as chat. Thread detail: participants, hops (3 of 8), budget used, the messages in order, each labelled with its sender agent and linked to its run. Triggers: name, agent, schedule or webhook, next firing, last outcome, Pause/Resume. | T071 `GET /v1/runs?background=true`, `GET /v1/runs/{id}/events`; T072; T073 | T074 |
| **403** and **404** | everyone | "Admins only" with the signed-in role and Back to chats; "This conversation doesn't exist or was deleted." | | T055 |

**Agent-made UI (A2UI)**: an agent can show a small form or set of choices inline (the mockup's "Narrow the news search"), rendered with the console's own components; submitting sends the values back as the next message, and the surface then shows "Sent" and is disabled ([E13](../tasks/E13-agent-protocols.md): T065, T066). Basic-catalog mapping: see Components.

## States

Each has a design in the mockup (the **State** menu in its top bar) or here.

| State | Where | Design |
|---|---|---|
| **No conversations** | Chats list, chat | List: "No conversations yet" and New chat. Content: the new-chat empty state. |
| **No agents** | Chat | "No agents are configured. An admin adds them to the server config (`agents:`)." Composer disabled. Admins get a link to the docs. |
| **Toolset skipped** | Toolsets, Agents, picker, new chat | Amber dot and "Skipped: EXA_API_KEY is not set". The agent shows a warning dot and "1 of 3 toolsets skipped"; the agent still works with the rest. |
| **Loading** | Every list and the thread | Skeleton rows the size of real rows (no spinners for page loads); a thread shows two skeleton messages. Nothing shifts when data arrives. |
| **Streaming** | Chat | The user message appears at once; a running step reads in the present tense with a spinner; text streams in; Stop replaces Send; `Esc` stops. Scroll follows only if you are at the bottom; otherwise a **Jump to latest** pill appears. |
| **Stopped** | Chat | "Stopped. The conversation keeps everything up to here." in muted text after the partial answer. |
| **401** | Anywhere | The sign-in card over the app, "Your session ended…"; the composer's draft and the route are kept and restored after sign-in. No request loop. |
| **403** | Admin pages | The 403 page (a direct link or a stale tab after a role change). |
| **409 busy** | Chat | A notice above the composer: "This conversation is answering in another tab or client. You can send when it finishes." Composer disabled, **Check again**. |
| **409 interrupted** | Chat, list (amber mark) | A notice: "The last run was interrupted (the server restarted). Resume continues from the last saved turn." **Resume**. |
| **503 over capacity** | Chat | The message stays pending (dimmed) and a line under it: "The server is busy. Retrying in 8 s" counting down from `Retry-After`, with **Retry now** and **Cancel**. One automatic retry, then manual. |
| **Run error** | Chat | After any partial answer, a red-marked block: what happened and what to do. Retryable codes (`rate_limited`, `overloaded`, provider timeouts) get **Retry**; `refused`, `max_turns`, `budget_exceeded` explain and offer none. A failed tool step reads "… failed" in red with the reason in its details; the run may still finish. |
| **Offline** | Anywhere | A thin bar at the top of the content: "You're offline. Messages will not send until the connection is back." Send disabled. |
| **Connection lost mid-run** | Chat | Until T071 a closed stream cancels the run, so the honest message is: "Connection lost. The run was stopped on the server; everything saved up to that point is kept." **Reload conversation**. With T071: "Reconnecting…" and the stream resumes from `Last-Event-ID`. |
| **Long content** | Chat, details | A long answer flows normally, code blocks scroll sideways, a Jump to latest pill. More than 3 tool calls collapse to a summary ("Used 20 tools · 34.2 s · searched the web 12 times, read 8 pages"); opened, it shows the first 5 and "Show 15 more". A huge result shows the first 20 results or 4,000 characters, then "Show all (1.2 MB)" and **Copy raw JSON**. Long titles end in an ellipsis with the full title in a tooltip. |
| **Not found** | Chat | The 404 message and Back to chats; the list drops the row. |

## Components

Built with React, shadcn/ui and Radix (E11, T078). Each component has a home:

- **`ui`** (`web/packages/ui`): generic pieces, mostly shadcn components themed with the tokens below.
- **`agent-ui`** (`web/packages/agent-ui`): agent-specific pieces, shared with future consumer apps. These must work from 360 px up, by touch, without hover-only affordances. The console is desktop-first, but these components are not.
- **`console`** (`web/apps/console`): the console's own pages and layout.

"Ours" means no shadcn component fits, so we build it ourselves.

| Component | Package | Built from | Variants | States |
|---|---|---|---|---|
| **App shell** (rail + list + content) | console | shadcn `Sidebar` in its nested form (an icon rail plus a second sidebar, like the "sidebar-09" block), `SidebarProvider` for the collapse state | desktop; laptop (list collapsed or not); phone (`Sheet` from the left as the drawer) | offline bar |
| **Rail item** | console | `SidebarMenuButton` + `Tooltip`, `Badge` | icon + tooltip label; badge (count or dot) | default, hover, current (`aria-current="page"`), focus |
| **List item** (conversation) | console | `SidebarMenuItem` + `SidebarMenuAction` (⋯) + `DropdownMenu` | title + agent + time; with a status mark (running spinner, interrupted amber dot, unread dot) | default, hover, current, focus, menu open, skeleton (`Skeleton`). On touch the ⋯ is always visible |
| **List item** (admin) | console | `SidebarMenuButton` | name + one-line description + status dot | as above |
| **Agent picker** | agent-ui | `Popover` + `Command` (a combobox); a bottom `Drawer` on phones | button (new chat), read-only chip with a card (existing), sheet (phone) | closed, open, filtered, no match, one agent (static) |
| **Composer** | agent-ui | `InputGroup` + `Textarea` + `Button` | idle, with draft, running (Stop), disabled with a reason | focus, multi-line (grows to 8 lines, then scrolls) |
| **Thread** | agent-ui | ours: a native scroll container (`role="log"`), stick-to-bottom, the Jump to latest pill | | at bottom, scrolled up (pill), loading (`Skeleton`) |
| **Message** | agent-ui | ours; Markdown through an allowlist renderer to React elements | user bubble; assistant text; pending (dimmed); stopped note; origin label ("From schedule …") | streaming (`aria-busy="true"`), actions (Copy) |
| **Tool step** | agent-ui | ours, on Radix `Collapsible` | an icon per kind (search, page, calc, tool) | running (shimmer + `Spinner`), done, failed; collapsed, open (request, results, raw JSON) |
| **Step group** | agent-ui | ours, on `Collapsible` | ≤ 3 calls open by default; > 3 collapsed with a summary; "Show N more" | running, done, some failed ("Used 4 tools · 1 failed") |
| **Source chip** | agent-ui | ours (a link styled like `Badge`) | site initial + host + path | hover, focus |
| **Usage line** | agent-ui | ours | turns · tokens (cached) · time | |
| **Run notice** (above the composer) | agent-ui | `Alert` + `Button` | info (busy), warning (interrupted), with an action | |
| **Inline error** (in the thread) | agent-ui | `Alert` (destructive) | retryable (Retry), final | |
| **A2UI surface** | agent-ui | ours, rendering the catalog with `ui` components (table below) | | live, sent (disabled) |
| **Run viewer** (Activity, later jobs) | agent-ui | Thread + Message + Step group, read-only, plus a key/value header | | running, needs you, done, failed |
| **Status** | ui | ours (dot + word) | ok (green), warn (amber), bad (red), neutral (grey), running (`Spinner`) | |
| **Tag** | ui | `Badge` (outline, mono) | agent or toolset name | |
| **Kbd** | ui | `Kbd` | single key, combination | |
| **Table** | ui | `Table` (no data-table chrome: no sorting or selection yet) | dense rows, header in small caps, status column, action column; on phones each row becomes a card | row hover, empty, skeleton |
| **Detail pane** | console | ours: a header row (`Button`s), a key/value list, sections, `Collapsible` for the system prompt | | loading, not found |
| **Empty state** | ui | `Empty` | icon, one sentence, one action | |
| **Toast** | ui | `Sonner` | text, text + action ("Open") | auto-hides after 4 s; announced politely |
| **Menu** | ui | `DropdownMenu` (with a radio group for the theme); `Drawer` on phones | anchored (desktop), bottom sheet (phone) | Esc and outside click close it; focus returns to the trigger |
| **Dialog** | ui | `AlertDialog` (Delete), `Dialog` (shortcuts) | confirm, informational | focus trapped, Esc closes |
| **Sign-in form** | console | `Card` + `Field` + `Input` + `Button` | password, token, session ended | default, submitting, error |
| **Button** | ui | `Button` | primary (ink), secondary (`outline`), ghost, icon | hover, focus, disabled, busy (`Spinner`) |
| **Inputs** | ui | `Input`, `InputGroup` (search with `Kbd`), `Select`, `Textarea`, `Checkbox`, `ToggleGroup` (segments) | text, password, search, select, textarea, checkbox, segmented | focus, invalid, disabled |

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
  :root:not([data-theme="light"]) { /* the dark block below */ }
}
:root[data-theme="dark"] {
  --bg: #0b0b0c; --surface: #141415; --surface-2: #1a1a1c;
  --hover: #1c1c1f; --surface-hover: #1c1c1f; --select: #232327;
  --text: #ededee; --text-2: #c8c8cc; --muted: #9a9aa2; --faint: #3a3a40;
  --line: #26262a; --line-strong: #34343a; --control: #6b6b73; --focus: #ededee;
  --primary: #ededee; --on-primary: #0b0b0c;
  --ok: #4ade80; --warn: #facc15; --bad: #f87171;
  color-scheme: dark;
}
```

Every text colour clears **4.5:1** on every surface it can sit on, and control edges, status dots and the focus ring clear **3:1**, in both themes (WCAG 2.2 AA; the table is in T070 Notes). Motion respects `prefers-reduced-motion` (no shimmer, no spinner rotation; the word "Running" stays).

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

Status colours (`--ak-ok`, `--ak-warn`, `--ak-bad`) have no shadcn equivalent and stay ours.

## Keyboard

`Mod` is ⌘ on macOS and Ctrl elsewhere. Single-key shortcuts work only when focus is not in a text field.

| Keys | Does |
|---|---|
| `Mod+Shift+O` | New chat |
| `Mod+K` | Search conversations (opens the drawer on phones) |
| `/` or `Shift+Esc` | Focus the composer |
| `Enter` / `Shift+Enter` | Send / new line |
| `Esc` | Stop the running answer; otherwise close the open menu, sheet, dialog or drawer |
| `Mod+Shift+S` | Show or hide the conversation list |
| `?` or `Mod+/` | Keyboard shortcuts |
| `↑` `↓` `Enter` `Esc` | Move, pick and close in the agent picker, menus and the list |

## Accessibility

- **Focus order:** rail, list (New chat, search, filter, rows), content (header, thread, composer). A **Skip to composer** link is the first focusable element. Every interactive element shows a 2 px `--focus` ring with a 2 px offset. Menus, sheets and dialogs return focus to their trigger.
- **Landmarks:** rail `nav` "Sections", list `nav` "Conversations", content `main`, the thread `role="log"` (`aria-live="off"`) labelled with the conversation title.
- **Streaming:** token deltas are **not** announced. One visually hidden `role="status"` region announces "researcher is answering", each settled tool step ("Searched the web, 1.4 seconds"), and "Answer complete" or the error. The streaming message has `aria-busy="true"` until it ends. Step groups and details are buttons with `aria-expanded`.
- **Status** is never colour alone: a dot plus a word, and errors also have an icon.
- **Targets** are at least 24 × 24 px (WCAG 2.2 2.5.8); phone rows are 44 px tall.
- **Contrast**: see Tokens.

## Rules

- **Roles are enforced by the API**, not by hiding pages: a `user` gets 403 on admin data even with a hand-made request. The console hides admin navigation for users only as a convenience.
- **Untrusted content stays text.** Tool results are third-party web pages, rendered as React text children (never `dangerouslySetInnerHTML`). Answers are Markdown, rendered to React elements by a renderer with a fixed element allowlist, no raw HTML and only `http(s)` links. A strict Content-Security-Policy (`default-src 'self'`, no inline scripts) is served with the page.
- **Name things by what people recognize**: "Credentials", "Toolsets", "Stop", "Resume"; errors say what happened and what to do ("EXA_API_KEY is not set: set it in the server's environment and restart").
- **Look (owner, 2026-10-12): neutral.** No brand colour. **Light**: grey ground, white working surfaces, near-black text and primary buttons, used sparingly. **Dark**: not pure black; a near-black ground with slightly lighter surfaces for depth, off-white text and an off-white primary button, so contrast is high without glare. Status is a small coloured dot plus a word (green ready, amber missing, skipped or interrupted, red error), never a coloured block. Geist for UI, Geist Mono for names, IDs and schemas. Theme follows the OS, with System/Light/Dark in the user menu. Exact values: Tokens.
- **Tool calls** (the part of chat people read most):
  - each call is **one quiet line**: an icon for its kind, a verb phrase people understand ("Searched the web", "Read 2 pages", "Calculated"), the key argument in muted text (the query in quotes, the page hosts), and the time it took. No borders, no JSON, no tool IDs at rest;
  - **while running** the verb reads in the present tense ("Searching the web") with a shimmer and a small spinner; **errors** read "… failed" in red with the reason in the details;
  - several calls in one turn group under **"Used 3 tools · 2.6 s"** with a thin guide line: open by default for up to 3 calls, collapsed with a summary above that (see Long content);
  - **details** open in place: the request as a key/value list, results as a compact list (site initial, title, host, one-line snippet), and "Show raw JSON" for the exact input and output;
  - the answer ends with **source chips** (host and path) for the URLs it cites, then a muted line with turns, tokens (cached) and time;
  - display text comes from tool metadata: each tool may declare a title, an icon kind and present/past verbs (MCP's `annotations.title` plus our own optional display fields on `tool.Definition`); tools without them fall back to "Ran `tool_name`" with the first argument.
- **Stack** (owner, 2026-10-12): React + TypeScript with shadcn/ui and Radix, in a `web/` workspace (`ui`, `agent-ui`, `apps/console`); the console build is embedded in the binary; Vitest and Playwright tests against a replayed model and faked connectors (E11, T078). The console is desktop-first; `agent-ui` components also serve mobile-first consumer apps ([PRODUCT.md](PRODUCT.md)).

## Open for the owner

- Brand: keep the neutral "agentkit" look, or apply a product name, logo and colours when the name is final (PLAN open item).
- More in T070 Notes ("Open questions").
