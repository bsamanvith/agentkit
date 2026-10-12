---
id: T068
title: "A2A client: remote agents as tools and flow steps"
epic: E13
status: todo
depends_on: [T067]
---

# T068: A2A client

## Context

Our agents should be able to delegate to other teams' agents. Decisions: [E13](E13-agent-protocols.md) ("Remote agents").

## Scope

- `pkg/a2a/client`: fetch and validate an Agent Card, `SendMessage`/`SendStreamingMessage`, poll or subscribe to a task, cancel.
- An `a2a` toolset type: settings list remote agents (card URL, auth from env or a credential slot); one tool per agent (`ask_<name>`, description from the card), returning the agent's final text and artifacts as text; long tasks time out with the task ID.
- A `flow` step that runs a remote A2A agent with checkpointing of the task ID (resume polls the same task).
- Built on T049's kit where it fits (cassettes of a remote agent, hermetic client, canary).

## References

- [A2A specification v1.0](https://a2a-protocol.org/latest/specification/) (client side: discovery, streaming, `SubscribeToTask`)
- [a2aproject/a2a-go](https://github.com/a2aproject/a2a-go) client
- T049 kit (`docs/CONNECTORS.md`): a remote agent is an outbound dependency like a connector

## Acceptance criteria

- [ ] Against our own A2A server (T067) in process: a tool call delegates and returns the answer; the flow step resumes an interrupted remote task by its ID instead of starting a new one.
- [ ] Remote output is labelled as third-party data in the tool result.
- [ ] Card validation rejects malformed cards; auth headers never leak (canary).
- [ ] Mutation list recorded.
