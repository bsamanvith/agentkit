---
id: T070
title: "Console UX design: layout, navigation, components and states, before any console code"
epic: E11
status: review
depends_on: []
---

# T070: console UX design

## Context

The owner wants the console's layout designed properly in its own session, before T055 builds it (2026-10-12). The planner's mockup ([docs/ui/console-mockup.html](../docs/ui/console-mockup.html), v2, also published as an artifact) settles the look and the tool-call style, but not the layout. Known problem: **conversations appear in two places**, the sidebar's "Recent" list and a separate Conversations page. Pick one.

This is a design task: the output is a spec and a clickable mockup the owner approves. No Go or TypeScript in `pkg/web`.

## Inputs (decided; keep unless you find a real problem, and say why)

- The screens and roles in [docs/UI.md](../docs/UI.md): sign-in, chat with an agent picker, conversations, agents, MCP toolsets and tools, credentials. Admin-only pages are enforced by the API.
- **Look:** neutral. Light is grey, white and minimal black; dark is near-black with lighter surfaces. Geist and Geist Mono. Status is a dot plus a word.
- **Tool calls:** quiet one-line steps; a group for several; details in place; source chips (docs/UI.md "Tool calls").
- **A2UI surfaces** inline in the chat ([E13](E13-agent-protocols.md)).
- **Background agents and agent threads** ([E14](E14-background-agents-messaging.md)): where running background runs, agent-to-agent threads, triggers and completion notifications live (T074 builds them). Approvals and memories come later; leave room for them.
- Every screen uses only `/v1` ([E11](E11-clients.md), T062's endpoints).

**Framework (owner, 2026-10-12): React + TypeScript with shadcn/ui and Radix.** Map the component inventory to shadcn/Radix components where they exist (Dialog, DropdownMenu, Command, Tabs, Table, Tooltip, Toast…) and say which are ours (tool step, step group, source chip, agent picker, run viewer). Split components into `ui` (generic) and `agent-ui` (agent-specific, shared with future mobile-first consumer apps): the console is desktop-first; agent-ui components must also work at phone width. [docs/PRODUCT.md](../docs/PRODUCT.md) has the bigger picture.

## Scope

1. **Information architecture:**
   - one home for conversations (recommendation: the left sidebar *is* the conversation list, with search, an agent filter and "New chat"; no separate page);
   - where admin pages live (a settings area or a separate "Admin" section, reached from the user menu or a rail);
   - how the agent picker works for a new chat versus an existing one (a conversation is bound to its agent).
2. **Layouts** at desktop (≥1280), laptop (~1024) and phone (~400) widths: chat, the conversation list, each admin page, sign-in.
3. **States** for each screen:
   - empty (no conversations, no agents, a toolset skipped);
   - loading and streaming;
   - errors (401, 403, 409 busy or interrupted, 503 over capacity, run errors such as rate-limited or refused);
   - offline or reconnecting stream;
   - long content (very long answers, 20 tool calls, huge results).
4. **Components:** the inventory with variants and states:
   - app shell, sidebar list item, agent picker, composer (with Stop), message (user and assistant), tool step and step group, source chip;
   - A2UI basic-catalog components; tables; status; empty state; toast;
   - the sign-in form;
   - the token set (colours for both themes, type scale, spacing, radius) as CSS custom properties T055 copies.
5. **Keyboard and accessibility:**
   - shortcuts (new chat, focus composer, search conversations, stop);
   - focus order, ARIA for streaming regions;
   - contrast checked to WCAG 2.2 AA in both themes (record the ratios).
6. **Mockup v3:** update `docs/ui/console-mockup.html` (one clickable file, sample data, both themes) and republish it as an artifact, or hand the HTML to the planner to publish.
7. Update `docs/UI.md` to match. Record decisions and their reasons in this file's Notes.

## References (study these; note what you take from each)

- **Chat layouts and conversation lists:**
  - Claude.ai and ChatGPT (sidebar conversation list, project and agent switching, composer);
  - [Open WebUI](https://github.com/open-webui/open-webui) and [LibreChat](https://github.com/danny-avila/LibreChat) (model and agent pickers, admin settings in a self-hosted chat);
  - [LobeChat](https://github.com/lobehub/lobe-chat) (agent marketplace and picker);
  - [Vercel AI Chatbot template](https://github.com/vercel/ai-chatbot) (minimal neutral chat, streaming, tool-call rendering).
- **Tool calls and steps:** Claude Code's terminal rendering of tool calls ("Read 2 files", collapsed groups); ChatGPT's and Perplexity's "searched N sites" rows and source chips; Cursor's agent steps.
- **Our framework:** [shadcn/ui](https://ui.shadcn.com/) (blocks such as sidebar, data table, chat-like layouts), [Radix Primitives](https://www.radix-ui.com/primitives).
- **Neutral design systems:** [Geist](https://vercel.com/geist/introduction) (Vercel; the type and grey scale we use); [shadcn/ui](https://ui.shadcn.com/) themes (the zinc and neutral palettes, light and dark); [Radix Colors](https://www.radix-ui.com/colors) (accessible grey scales, how dark-mode steps differ).
- **Admin pages:** Linear and Vercel dashboard settings (dense, quiet tables and detail panes).
- **Agent-made UI:** [A2UI component gallery and basic catalog](https://a2ui.org/) (what surfaces can contain); [CopilotKit A2UI renderer](https://www.copilotkit.ai/blog/a2ui-whats-new-in-google-generative-ui-spec).
- **Accessibility:** [WCAG 2.2](https://www.w3.org/TR/WCAG22/) contrast and focus; [WAI-ARIA live regions](https://www.w3.org/WAI/ARIA/apg/) for streamed text.

## Acceptance criteria

- [x] **Conversations** have exactly one home, and the doc says why.
- [x] **Layouts** for chat, the conversation list, the agents, toolsets and credentials pages, and sign-in, at all three widths, in both themes, in mockup v3.
- [x] **States:** every one listed in Scope 3 has a design in the mockup or the doc.
- [x] **Components:** the inventory and tokens are written down. Contrast ratios for text, muted text, borders and status colours meet WCAG 2.2 AA in both themes (the table is in Notes).
- [x] **Shortcuts:** the keyboard shortcuts are listed and work in the mockup.
- [ ] **Owner review:** the owner approves mockup v3 (record the date and any changes asked for). T055 starts from it.

## Notes

### Files

- **`docs/UI.md`**: rewritten as the console spec. It covers:
  - the information architecture and its decisions;
  - layouts at three widths, screens, every state;
  - the component inventory, with each component's package (`ui`, `agent-ui`, `console`) and the shadcn/Radix part it is built from, plus the A2UI mapping;
  - tokens as CSS custom properties for both themes, with the mapping to shadcn's variables;
  - keyboard, accessibility, rules, open questions.
- **`docs/ui/console-mockup.html`**: mockup v3. It is one self-contained file: Google Fonts only, no external scripts, sample data. The mockup bar along the top is not part of the console. It has:
  - **Theme**: System, Light or Dark.
  - **Width**: Fit, or a 1280, 1024 or 400 px frame. The layout uses container queries on the frame, so the widths are real layouts, not scaled screenshots.
  - **Role**: Admin or User.
  - **State**: every state in Scope 3, from 21 options.
  - **Show later features**: shows or hides Activity, the background-run row and the Memories item.

  The tokens are copied verbatim from UI.md.
- **Not changed:** code, `make check` inputs, other task files. No images were added; the mockup is the picture.

### Decisions

1. **Conversations live in one place: the list column next to the rail.** v2's separate Conversations page is gone.
   - The list has New chat, search (`Mod+K`), an agent filter, and date groups (Today, Yesterday, Previous 7 days, Older).
   - Status marks: a spinner while a run is in progress, a dot when the last run was interrupted.
   - Older conversations load by cursor as you scroll.
   - Each row has a ⋯ menu with Copy ID and Delete.

   Why:
   - Every reference studied keeps exactly one list beside the chat: Claude.ai, ChatGPT, Open WebUI, LibreChat, LobeChat and the Vercel template.
   - A second page showed the same rows with more columns (agent, date). Those fit in the row's subline.
   - Two homes meant two places to keep fresh after a run, a delete or a new chat.
2. **Admin pages live in a 56 px icon rail.** It runs: Chats, Activity (later), a divider, then Agents, Toolsets and Credentials (admin only). Shortcuts and the account menu sit at the bottom. On a phone, the rail becomes tabs at the top of the drawer.

   Rejected:
   - **An "Admin" entry in the user menu**, which hides pages admins use daily.
   - **Swapping the sidebar for a settings tree (Linear-style)**, which takes the conversation list away while you look at an agent.

   Why the rail:
   - It costs 56 px.
   - It gives T074's Activity a badge slot.
   - It makes "admin only" a matter of omitting three buttons, while the API still enforces it (403 page).

   Agents and Toolsets use list + detail in the same list column. Credentials is one full-width table, because there is nothing to drill into.
3. **The agent picker belongs to new chats only.**
   - **New chat:** the header's left slot is a button with a searchable list. Each option shows the agent's description and its readiness ("1 of 3 toolsets ready"). The default agent is, in order: the agent of the last new chat (browser storage); else `default`; else the first.
   - **Existing conversation:** the header shows a read-only chip, because continuing with another agent is `409 agent_mismatch`. The chip's card offers "New chat with X" and "New chat with another agent…", and "Open agent details" for admins.
   - **One agent:** there is no picker.
   - **Agent detail:** "Chat with X" presets the picker.

   This is how Open WebUI and LibreChat handle models, with one difference: they allow switching mid-conversation, which our API forbids, so the chip says so instead of failing on send.
4. **Connection loss is described as it is.** Closing the stream cancels the run (T036), and nothing resumes it until T071. So "Connection lost" says the run was stopped and keeps what was saved; it offers "Reload conversation", not "Reconnecting…". With T071 it becomes a reconnect (`Last-Event-ID`). The offline bar disables sending and keeps the draft.
5. **Automatic retries are limited.**
   - **503 `over_capacity`:** the console counts down from `Retry-After` and retries once by itself; the user can Retry now or Cancel (Cancel returns the message to the composer).
   - **Retryable run errors** (`rate_limited`, `overloaded`): a Retry button, no automatic retry, because a run may already have spent tokens.
   - **`refused`, `max_turns`, `budget_exceeded`:** no button.
6. **Tool-call groups:** up to 3 calls show open; more collapse to one summary line ("Used 20 tools · 1 failed · 34.2 s · searched the web 12 times, read 8 pages"). Opening the group shows the first 5 calls and "Show 15 more".

   A huge result shows the first 20 results or 4,000 characters, then "Show all (1.2 MB)" and Copy raw JSON. A tool failure inside a successful run marks only that step, plus "1 failed" in the group header; the answer is still the answer.
7. **v2's grey ramp failed contrast on tinted surfaces; it was fixed rather than replaced.**
   - `--muted` was `#71717a` and is now `#63636b`.
   - `--warn` was `#a16207` and is now `#9a5800`.
   - `--ok` was `#15803d` and is now `#137333`.
   - `--faint` is decoration only; v2 used it for timings and API lines.
   - A new `--control` token gives input edges 3:1 (WCAG 1.4.11). Button edges stay decorative, because the label identifies the button.
8. **Later features get slots but no clutter.** These are visible in the mockup, behind "Show later features":
   - **Activity** (T074): Runs, Threads, Triggers, run detail with Cancel, thread detail, a completion toast with Open.
   - **Approvals** (T030/T037): an inline card, the "Needs you" filter and the badge.
   - **Memories** (E12): in the user menu.
   - **Skills** (T057): on the agent detail.

   Two more slots are written in UI.md only, because they arrived on main with the product map during this task:
   - **Editing agents and skills** (E15, T075 and T077): Edit and Versions on the agent detail.
   - **E16's jobs:** they appear in Activity's Runs, with the same run viewer.

   The console is admin-first (PRODUCT.md), and the rail already puts the admin pages one click away.

   A background run's conversation also appears in Chats, with the run's origin labelled ("From schedule daily-digest").
9. **Framework mapping** (the owner's React + shadcn/ui + Radix decision, which arrived on main during this task). The design didn't change; the inventory now names a package and a building block for every component. Main points:
   - **The shell is shadcn's `Sidebar` in its nested form:** an icon rail plus a second sidebar for the list, like the "sidebar-09" block. That is the IA of decision 2, so no custom layout is needed.
   - **The agent picker is `Popover` + `Command`.** On phones, menus and the picker become a bottom `Drawer`. Toasts are `Sonner`, and the delete confirm is `AlertDialog`.
   - **What is ours, in `agent-ui`:**
     - thread, message, tool step and step group (on Radix `Collapsible`);
     - source chip, usage line, A2UI surface, run viewer;
     - the agent picker and composer, assembled from `ui` parts.

     These must work from 360 px up, by touch, with nothing hover-only (the ⋯ on a row is always visible on touch).
   - **Tokens keep their values.** `packages/ui` prefixes them `--ak-` and sets shadcn's variables from them (table in UI.md), because shadcn's `--muted` is a background and ours is text. Its default `muted-foreground` (`#71717a`) is the value that failed contrast in v2, so the mapping matters.
10. **A2UI:** the console renders the basic catalog with its own components: Text h1–h5, caption and body; Image, Icon, Divider, Row, Column, List, Card, Button, TextField, CheckBox, ChoicePicker and DateTimeInput. Slider, Tabs and Modal are deferred: an unsupported component renders as a labelled placeholder showing its type, never as raw JSON. A sent surface is disabled and says it was sent.

### For T055 (console)

- **Tokens:** `packages/ui` (T078) owns the token block, prefixed `--ak-`, and sets shadcn's variables from it. T055 uses them through shadcn's variables and the `--ak-` names; it doesn't copy the block again. Use the breakpoints 768 and 1200 as media queries; the mockup's container queries exist only so the frame can show three widths.
- **Thread semantics:** use `role="log"` with `aria-live="off"`. Keep one hidden `role="status"` region that announces "X is answering", each settled step ("Searched the web, 1.4 seconds") and the end. Never announce token deltas. Set `aria-busy` on the streaming message.
- **Scrolling:** stick to the bottom only while the user is there; otherwise show "Jump to latest". The mockup's `atBottom` threshold is 40 px.
- **Shortcuts:** use `metaKey || ctrlKey`. Single keys (`/`, `?`) don't fire inside fields. Esc stops a run before it closes anything else only when no overlay is open. Match `?` by `key === "?"`; one test harness sent Shift+/ with an empty `key`, so also accept `/` with Shift.
- **Rendering untrusted text:** tool output and model text are React text children, never `dangerouslySetInnerHTML`. Markdown goes through an allowlist renderer to React elements: no raw HTML, `http(s)` links only. The mockup builds every node with `textContent` for the same reason.
- **The mockup is plain HTML and JS**, so it opens anywhere. Take its behaviour and measurements, not its code. The component-to-shadcn mapping is in UI.md's Components.

### For T064 (admin pages)

- **Agents:** list + detail with:
  - model and effort;
  - toolsets as chips with readiness, each linking to the toolset;
  - skills, memory, "Answers with";
  - the system prompt in a disclosure, for admins only.

  When toolsets are skipped, the agent shows a warning notice.
- **Toolsets:** list + detail with:
  - type, credentials, "Used by";
  - the MCP URL and a `claude mcp add` command, which has a token placeholder and never the token;
  - the tools as disclosures with a field table and "Show raw schema".
- **Credentials:** one table with Toolset, Provider, Status, Source, Scope and an action. Set key and Rotate are disabled until E07 (T045). On a phone, each row becomes a card.

### For T066 (A2UI renderer)

Use the "A2UI → console" table in UI.md's component inventory as the mapping, not a separate design.

### For T074 (Activity)

- The rail slot, badge, list segments, run and thread details, and trigger table are in the mockup ("Show later features").
- Cancel uses `POST /v1/runs/{id}/cancel`.
- Run detail streams from `GET /v1/runs/{id}/events`.
- Notifications are a toast with Open, plus the unread dot and badge. Desktop notifications are out of scope.

### References: what we took and what we left

| Reference | Took | Left |
|---|---|---|
| Claude.ai | One conversation list beside the chat; date groups; a quiet composer that becomes Stop | Projects (no equivalent yet) |
| ChatGPT | "Searched N sites" summary rows; source chips under the answer; a ⋯ menu per conversation | Model switching mid-conversation (our API forbids it) |
| Open WebUI | Admin settings inside the same app; the header picker for a new chat | A full settings modal; per-message model switching; dense config forms |
| LibreChat | An agent picker with descriptions; a self-hosted admin model | A sidebar that mixes presets, files and bookmarks |
| LobeChat | Agent cards showing what each agent can do (our readiness line) | The marketplace and avatars: too loud for a neutral console |
| Vercel AI Chatbot | Typed message parts each with its own renderer; a minimal neutral look; streaming caret | Artifacts side panel (out of scope) |
| Claude Code tool rendering | One line per call: verb, key argument, time; consecutive calls grouped | Terminal colour coding |
| Perplexity | Favicon source chips | Numbered inline citations (needs model support we don't have) |
| Cursor agent steps | Compact expandable step rows; a shimmer on the running step | Inline diffs |
| shadcn/ui blocks and components | The nested `Sidebar` (icon rail + list, "sidebar-09") as the shell; `Command` for the agent picker; `Drawer` for bottom sheets; `Sonner`, `AlertDialog`, `Empty`, `Kbd`, `Spinner`, `InputGroup` | The data-table chrome (sorting, selection, pagination) until a page needs it; the floating and inset sidebar variants |
| Radix Primitives | `Collapsible` for tool steps and groups; Radix's focus return, dismiss and typeahead behaviour in menus and dialogs (the mockup imitates it by hand) | Radix `Toast` (Sonner instead, as shadcn does) |
| Geist | Font pair, grey scale, density, 6/8/12 radii | Geist's blue accent (we use near-black) |
| shadcn/ui (zinc, neutral) | Light and dark neutral palettes as the starting ramp | `muted-foreground` `#71717a` as-is: it fails 4.5:1 on tinted surfaces (decision 7) |
| Radix Colors | Dark steps are not inverted light steps (dark surfaces get lighter as they rise) | The 12-step scale (we need 5 surfaces) |
| Linear and Vercel dashboards | Quiet tables, list + detail, status dot plus word | Linear's sidebar swap for settings (decision 2) |
| A2UI v0.9.1 basic catalog | Its 16 components as the renderer's inventory | Slider, Tabs and Modal for now; agent-supplied styling |
| CopilotKit A2UI renderer | Surfaces inline in the thread, drawn with the host's own components | Its floating chat widget |
| WCAG 2.2 | 1.4.3 (4.5:1 text), 1.4.11 (3:1 controls and focus), 2.4.11 (focus not obscured), 2.5.8 (24×24 targets) | — |
| WAI-ARIA APG | `log` + polite `status` for streaming; listbox picker; menu and dialog patterns with focus return | Live-announcing streamed text (too noisy) |

### Contrast (WCAG 2.2 AA)

Every ratio below was computed with the WCAG relative-luminance formula from the token values in UI.md. A ✗ marks a pair the design never uses.

**Light:**

| Token | Value | surface `#ffffff` | bg `#f4f4f5` | surface-2 `#fafafa` | hover `#ececee` | select `#e7e7ea` | Needs |
|---|---|---|---|---|---|---|---|
| `--text` | `#0a0a0a` | 19.80 | 18.01 | 18.97 | 16.78 | 16.04 | 4.5:1 |
| `--text-2` | `#3f3f46` | 10.44 | 9.50 | 10.01 | 8.85 | 8.46 | 4.5:1 |
| `--muted` | `#63636b` | 5.95 | 5.42 | 5.70 | 5.05 | 4.82 | 4.5:1 |
| `--ok` | `#137333` | 5.95 | 5.42 | 5.70 | 5.05 | 4.82 | 4.5:1 |
| `--warn` | `#9a5800` | 5.57 | 5.07 | 5.34 | 4.72 | 4.51 | 4.5:1 |
| `--bad` | `#b91c1c` | 6.47 | 5.89 | 6.20 | 5.48 | 5.24 | 4.5:1 |
| `--control` | `#8a8a93` | 3.42 | 3.11 | 3.28 | 2.90 ✗ | 2.77 ✗ | 3:1 |
| `--line` | `#e4e4e7` | 1.27 | 1.15 | 1.22 | 1.08 | 1.03 | decorative |
| `--line-strong` | `#d4d4d8` | 1.48 | 1.34 | 1.42 | 1.25 | 1.20 | decorative |
| `--faint` | `#a1a1aa` | 2.56 | 2.33 | 2.46 | 2.17 | 2.08 | decorative |

**Dark:**

| Token | Value | surface `#141415` | bg `#0b0b0c` | surface-2 `#1a1a1c` | hover `#1c1c1f` | select `#232327` | Needs |
|---|---|---|---|---|---|---|---|
| `--text` | `#ededee` | 15.74 | 16.82 | 14.85 | 14.53 | 13.38 | 4.5:1 |
| `--text-2` | `#c8c8cc` | 11.04 | 11.79 | 10.42 | 10.19 | 9.39 | 4.5:1 |
| `--muted` | `#9a9aa2` | 6.59 | 7.04 | 6.22 | 6.09 | 5.61 | 4.5:1 |
| `--ok` | `#4ade80` | 10.57 | 11.29 | 9.97 | 9.76 | 8.99 | 4.5:1 |
| `--warn` | `#facc15` | 12.02 | 12.85 | 11.35 | 11.10 | 10.22 | 4.5:1 |
| `--bad` | `#f87171` | 6.66 | 7.11 | 6.28 | 6.15 | 5.66 | 4.5:1 |
| `--control` | `#6b6b73` | 3.49 | 3.72 | 3.29 | 3.22 | 2.96 ✗ | 3:1 |
| `--line` | `#26262a` | 1.22 | 1.31 | 1.15 | 1.13 | 1.04 | decorative |
| `--line-strong` | `#34343a` | 1.49 | 1.59 | 1.41 | 1.37 | 1.27 | decorative |
| `--faint` | `#3a3a40` | 1.63 | 1.74 | 1.54 | 1.51 | 1.39 | decorative |

**Other pairs:**

| Pair | Light | Dark |
|---|---|---|
| On-primary (buttons, badges, toast) | 19.80 | 16.82 |
| Focus ring (`--focus`) on surface / bg | 19.80 / 18.01 | 15.74 / 16.82 |

Rules that keep these true:

- Inputs, selects and the composer are always `--surface`, so the `--control` edge is measured against `--surface` inside and `--bg` or `--surface` outside; inputs never sit on `--hover` or `--select`.
- Lines and `--faint` carry no information. Status is always a dot plus a word, or a dot with an accessible name where space is tight (list rows).

v2's failures, for the record:

- muted `#71717a`: 4.40 on bg, 4.10 on hover;
- warn `#a16207`: 4.48 on bg;
- faint `#a1a1aa` used as text: 2.56 on white.

### What changed from v2

- **One home for conversations:** the list column. The Conversations page and the "Recent" list are gone.
- **A 56 px icon rail** replaces v2's 240 px nav sidebar. Admin pages are rail items, not an "Admin" group, and Activity has a slot.
- **The agent picker rule:** a picker for a new chat, a read-only chip with "New chat with…" for an existing one, and no picker with one agent.
- **Agents is list + detail** instead of cards. Credentials is one table instead of cards. Toolsets gained the copyable MCP URL and command, field tables and raw schema.
- **States:** v2 had none besides sign-in. v3 has all 21, from a menu.
- **Layouts:** three widths. On a phone there is a top bar, a drawer with section tabs, and bottom sheets for menus and the picker. The laptop width has a collapsible list.
- **Tokens:** contrast fixes (decision 7) and `--control`. The dark theme's text tokens were re-checked.
- **Keyboard shortcuts and a shortcuts dialog.** There is a skip link, ARIA for streaming, and reduced motion is respected.
- **Framework:** the components are mapped to shadcn/Radix and split into `ui`, `agent-ui` and `console` packages, and the tokens are mapped to shadcn's variables (decision 9).
- **Tool-call groups:** v2 showed one group of 2. v3 has the more-than-3 collapse, a summary line, "Show N more", the huge-result truncation and failed steps.

### Open questions for the owner

1. **Reconnecting without losing the run.** Until T071, a dropped stream stops the run. Is "Connection lost, run stopped, Reload" acceptable for T055, or should T071 come first?
2. **Renaming conversations.** There is no rename endpoint, so titles are the first message (truncated). Should a `PATCH /v1/conversations/{id}` with a title be added, or a server-generated title?
3. **Tenant switcher.** An admin of several tenants has no way to switch in this design. Is it needed for the first version?
4. **Brand.** The mark is "ak" in a black square, a placeholder. Is there a name or logo to use?
5. **A2UI Link.** E13 lists "Link", which is not in the v0.9.1 basic catalog (Heading and Select map to Text variants and ChoicePicker). Should E13 drop it, or render links inside Text?
6. **Activity for users.** The design shows users their own background runs and threads, and Triggers only to admins. Is that right?

### Found out of scope (not changed)

- **E13 component names:** E13's A2UI list says Heading, Select/ChoicePicker and Link. The catalog names are Text (h1–h5), ChoicePicker, and no Link (open question 5). E13 needs a wording fix.
- **T055 wording:** T055 says a "tool call appears as a card", which predates the quiet-steps style. It should say "a step row (docs/UI.md, Components)".
- **T066 references:** T066 should point at UI.md's "A2UI → console" table as its mapping.
- **T078's token wording:** T078 says `packages/ui` takes "the token set from docs/UI.md and T070 as CSS variables". It should also name the `--ak-` prefix and the shadcn mapping, or a straight copy will clash on `--muted`.

### Checks run

- **Widths and themes:** the mockup was opened in a browser and looked at:
  - in light at 1280, 1024 and 400;
  - in dark at 1024 and 400.
- **States:** all 21 State options were switched through at each of the three widths (63 runs) with no script errors.
- **360 px:** the frame was set to 360 px. Neither the content nor the thread overflows sideways, and the ⋯ on conversation rows is visible at phone width.
- **Shortcuts:** each was exercised with key events: `?`, `Mod+/`, Esc closing a dialog, `Mod+Shift+O`, `/`, `Shift+Esc`, Enter to send, Esc to stop, `Mod+Shift+S`, `Mod+K`.
- **Bugs fixed during the check:**
  - `[hidden]` was overridden by `display` rules;
  - a hidden list column swallowed the content column in the grid;
  - the search field showed a double border;
  - repeated hosts appeared in "Read N pages".
- **Contrast:** computed with the script that produced the tables above.
- **Repository:** `make check` exited 0 on the final tree, rebased onto `a4846cf`, before the push.
