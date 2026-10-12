---
id: T067
title: "A2A server: Agent Cards, message and task methods over our runs, streaming"
epic: E13
status: todo
depends_on: [T062]
---

# T067: A2A server

## Context

Other agent systems (and orchestrators) talk A2A. Serving each of our agents as an A2A agent makes them usable from any of them. Decisions: [E13](E13-agent-protocols.md) (mapping: task = run, contextId = conversation; library choice).

## Scope

- Read the A2A v1.0 spec (a2a-protocol.org); evaluate `a2aproject/a2a-go` (record the decision and why).
- An `a2a` component: per agent, an Agent Card (name, description, skills from the agent's tools and skills, capabilities: streaming yes, push notifications no for now, security schemes from E07) at `/a2a/{agent}/.well-known/agent-card.json`, plus a directory at `/.well-known/agent-card.json` when one agent is the default.
- Methods (JSON-RPC binding first, HTTP+JSON if cheap): `SendMessage`, `SendStreamingMessage` (SSE from our run events), `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`. Task states from run statuses; `input-required` only once the runner (T030) can suspend runs, until then not advertised.
- Text, data and file parts mapped to our messages (files: refused with the spec's error until supported).

## References

- [A2A specification v1.0](https://a2a-protocol.org/latest/specification/) (Agent Card, methods, task states, bindings, errors)
- [a2aproject/a2a-go](https://github.com/a2aproject/a2a-go) (official Go SDK; evaluate first)
- [A2UI A2A extension](https://a2ui.org/) (surfaces in A2A messages)

## Acceptance criteria

- [ ] The spec's examples (requests and responses) pass as golden tests; an A2A client (the SDK's, or our T068 client) completes a streamed task against our agent with a fake model.
- [ ] A task started over A2A appears in `GET /v1/conversations` and its run in `GET /v1/runs/{id}`.
- [ ] Cancel maps to our run cancel; unknown task is `TaskNotFoundError`; a message to a terminal task is `UnsupportedOperationError`.
- [ ] Auth: with T040, A2A paths require credentials and respect tenants.
- [ ] Live (`-tags live`): real Claude answers an A2A `SendStreamingMessage`. Mutation list recorded.
