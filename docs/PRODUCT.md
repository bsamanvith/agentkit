# agentkit: product map

> How the ideas fit together (owner interview, 2026-10-12). [PLAN.md](PLAN.md) has the module design, [tasks/STATUS.md](../tasks/STATUS.md) the work. Deep dives on each idea come later; this page says what is a platform piece and what is a product.

## Layers

| Layer | What it is | Status |
|---|---|---|
| **Core harness** | The agent loop, tools and toolsets, MCP server, skills, memory, `flow`, the runner, the agent API. One core for every product. | Mostly built; store and runner in progress (E05), skills and memory planned (E12) |
| **Agent definitions** | An agent is **data**: model, system prompt, toolsets and their settings, skills, memory, limits, UI capabilities. Stored and **versioned** (every change is a version you can roll back), edited through the API and the admin console, exportable as files. | Planned: [E15](../tasks/E15-agent-definitions.md) |
| **Tool safety** | Tools carry kind (read, write, delete, send, execute) and risk; policies allow, flag, require approval for or deny calls; environments can only narrow; interaction modes decide whether an agent may stop to ask a person. | Planned: [E17](../tasks/E17-tool-safety-control.md) |
| **Agent packages** | A product's variation on the core is mostly configuration: dedicated tools (a connector or a toolset), skills, a prompt, settings, a few **plugins** (hooks on the harness: before/after tool calls, prompt additions). Packaged so they ship as a unit. | Planned with E15; plugins later |
| **Runtimes** | Where an agent runs: **(1)** the agentkit server (chat, background runs, triggers); **(2)** a **temporary environment** (a container or sandbox): run once, save the output and history, gone; **(3)** an **exported image** (the harness plus chosen tools and skills baked in) for ephemeral VMs. The harness inside (2) and (3) is **pluggable**: our Go binary first, the pi harness as a second option, through one small contract. | Planned: [E16](../tasks/E16-runtimes-jobs.md) |
| **Results** | Two collections: **conversations** (people chatting with agents) and **tasks** (every run that isn't a chat: background, triggered or ephemeral; input, output, a live transcript you can steer, history). Both render with the same components. | Conversations exist; tasks planned (E14, E16) |
| **UI** | **(a) Admin console**: desktop-first; configure agents, skills, toolsets and credentials; test them in chat; watch runs and jobs. **(b) Consumer apps**: mobile-first, one per product, built on a shared **agent-ui** component set (chat, tool steps, run and job viewer, A2UI surfaces). | Console: designing (T070) and building (E11); consumer apps later |

## Decisions from the interview (2026-10-12)

- **UI framework: React + TypeScript**, with **shadcn/ui and Radix** for the neutral look, built with Vite in a pnpm workspace under `web/`: `packages/ui` (tokens and primitives), `packages/agent-ui` (chat, tool steps, run viewer, A2UI renderer; shared by every app), `apps/console` (the admin console). Consumer apps are added as `apps/<name>`. The console's build output is embedded in the Go binary, so the image stays one binary. This replaces the earlier "no framework" decision ([E11](../tasks/E11-clients.md)).
- **First priority after the running work: the admin console**, including editing agents and skills.
- **Agent definitions live in the store, versioned** (E15), not only in config files; files stay possible for images and deployments (export/import).
- **Ephemeral harness: pluggable** (Go binary first, pi second) behind one contract (E16).

## Products and ideas (deep dives later)

Each is a set of dedicated tools and skills plus a prompt and a few plugins on the core harness; each gets its own consumer app on `agent-ui` when it is built.

| Idea | What it needs from the platform | When |
|---|---|---|
| **Ephemeral VM harness** | Exported images (E16), the runtime contract, results reported back as jobs | After E16's first tasks |
| **Farmer's assistant** | Connectors (weather, market prices, crop and soil data, maybe imagery), memory per farm (E12), a mobile consumer app, perhaps local-language support and offline-friendly UI | Later: needs a product deep dive |
| **Finance tools** | Connectors (market data, filings, spreadsheets), careful credential handling (E07), audit logs, A2UI tables and charts (E13) | Later: needs a product deep dive |

## Open questions for later deep dives

- Which sandbox for temporary environments and VMs (containers on one host, Firecracker, a cloud sandbox service), and who pays the start-up time?
- Do jobs need their own triggers and retention, or reuse E14's?
- Plugin model: Go hooks compiled in, or out-of-process (WASM, gRPC) so products don't rebuild the binary?
- Consumer app distribution: web (PWA) first, or native mobile (React Native shares code with React)?
