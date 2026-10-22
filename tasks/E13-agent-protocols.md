---
id: E13
title: "Agent protocols: A2UI (agent-driven UI), A2A (agent to agent), AG-UI and MCP Apps"
status: todo
---

# E13: agent protocols

Owner request (2026-10-12): look at how other agent harnesses let agents **stream UI elements** (Google's **A2UI**) and talk to other agents (**A2A**), and plan for them.

The agent-UI space has settled into complementary layers ([A2UI's comparison](https://a2ui.org/introduction/agent-ui-ecosystem/)):

| Layer | Protocol | What it does | Our plan |
|---|---|---|---|
| Tools | **MCP** | Agent ↔ tools and data. | Done (E03); connectors (E10). |
| Agent ↔ agent | **A2A** v1.0 (Linux Foundation) | Agent Cards for discovery (`/.well-known/agent-card.json`). Methods: `SendMessage`, `SendStreamingMessage`, `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`. Tasks have states, including `input-required`. JSON-RPC, gRPC and HTTP+JSON bindings. | Serve our agents as A2A agents (T067); call remote A2A agents as tools and flow steps (T068). |
| Agent → UI (what to draw) | **A2UI** (Google, Apache-2.0; v0.9.1 current, v1.0 candidate) | Declarative JSON describing components from a **client-side catalog**, their properties and a data model. No code is sent: the client renders with its own components and styling. Components form a flat list that references each other by ID, so a model can **stream** them and the UI renders progressively. User actions go back to the agent. Transport-agnostic (A2A extension, AG-UI, SSE, JSONL). | Agents emit A2UI surfaces through our event stream (T065); the console renders the basic catalog (T066). |
| App ↔ agent session | **AG-UI** (CopilotKit, MIT) | An event stream (about 16 standard event types) between an agent backend and a frontend, with shared state and human-in-the-loop interrupts. | Optional adapter so AG-UI and CopilotKit frontends can use our agents (T069, later). |
| Tool-provided UI | **MCP Apps** (SEP-1865) | A tool returns a `ui://` resource rendered in a sandboxed iframe; the server controls its look. | Later: render in the console in a sandboxed iframe, only for trusted toolsets. |

Our own agent API (`/v1`, HTTP + SSE) stays the primary interface. The protocols are adapters on the same runs, so a run started over A2A shows up in the console's conversation list like any other.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| How an agent produces UI | Through a **tool**, `ui_render(surface)`, whose input is an A2UI message (surface ID, components, data model). The harness validates it against **our catalog** (an allowlist of component types and properties) and emits a `ui` event on the run's stream. An invalid surface is a tool error the model can fix. Agents opt in per config (`agents[].ui: "a2ui"`); others never see the tool. |
| Which A2UI version | Target the **current stable spec** at implementation time (v0.9.1 now; v1.0 is a candidate) and record it. The catalog is versioned with it. Message and field names come from the spec text, not from secondary write-ups. |
| Catalog | Start with a subset of A2UI's **basic catalog**: Text, Heading, Row, Column, Card, List, Button, TextField, Select/ChoicePicker, DateTime, Checkbox, Divider, Image (URL allowlist), Link. Unknown components are rejected, never rendered as raw data. |
| User actions | A click or submit on a surface becomes a **user message with a structured `ui_action` part** (surface ID, action name, data-model values). It is appended to the conversation like any message, so the agent continues normally and the transcript stays append-only. |
| Rendering safety | The console renders A2UI with its own components only: no HTML, no script, no styles from the agent. Text is text. Image and link URLs pass the same `http(s)` allowlist as Markdown. |
| A2A mapping | An A2A **task** is one of our **runs**; an A2A **contextId** is a **conversation**. `input-required` maps to a run waiting for an approval or a `ui_action`, which needs the runner (T030) to be durable. A2A streaming maps from our SSE events. Each configured agent gets an Agent Card, whose auth schemes come from E07. |
| A2A library | Evaluate the official Go SDK (`a2aproject/a2a-go`) first, and use it if it implements v1.0 cleanly and its dependencies are acceptable (archtest allowlist). Otherwise implement the JSON-RPC and HTTP+JSON bindings ourselves against the spec, with the spec's examples as golden tests. |
| Remote agents | A remote A2A agent becomes a **tool** (`a2a` toolset type: one tool per configured remote agent, built from its Agent Card) and a **`flow` step**. Its output is third-party data (prompt-injection rule from E10). |

## Tasks

- [ ] [T065](T065-a2ui-events.md) A2UI in the harness: `ui_render` tool, catalog validation, `ui` events, `ui_action` messages
- [ ] [T066](T066-console-a2ui-renderer.md) Console: render A2UI surfaces with the console's own components, send actions back
- [ ] [T067](T067-a2a-server.md) A2A server: Agent Cards, message and task methods over our runs, streaming
- [x] [T068](T068-a2a-client.md) A2A client: remote agents as tools and flow steps
- [ ] [T069](T069-ag-ui-adapter.md) AG-UI adapter (later, if a CopilotKit-style frontend is wanted)

Order:
- all of this comes after the console basics (T055) and the agents API (T062);
- T065, then T066;
- T067 needs T062, plus T030 for a durable `input-required`;
- T068 after T067's types;
- MCP Apps rendering is a later task, once a connector ships a UI resource.
