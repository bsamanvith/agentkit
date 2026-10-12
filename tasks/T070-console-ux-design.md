---
id: T070
title: "Console UX design: layout, navigation, components and states, before any console code"
epic: E11
status: todo
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

- [ ] **Conversations** have exactly one home, and the doc says why.
- [ ] **Layouts** for chat, the conversation list, the agents, toolsets and credentials pages, and sign-in, at all three widths, in both themes, in mockup v3.
- [ ] **States:** every one listed in Scope 3 has a design in the mockup or the doc.
- [ ] **Components:** the inventory and tokens are written down. Contrast ratios for text, muted text, borders and status colours meet WCAG 2.2 AA in both themes (the table is in Notes).
- [ ] **Shortcuts:** the keyboard shortcuts are listed and work in the mockup.
- [ ] **Owner review:** the owner approves mockup v3 (record the date and any changes asked for). T055 starts from it.
