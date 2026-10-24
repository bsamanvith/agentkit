---
id: T124
title: "Memory API and the console's Memory page"
epic: E23
status: todo
depends_on: [T122]
---

# T124: see and delete what an agent remembers

## Context

E12: "users must be able to see what is remembered". Memory is plain text
(T121), so it can be shown as is.

## Scope

- Agent API: `GET /v1/agents/{agent}/memory?path=` (a listing, or a file's
  text, with sizes and the agent's limits) and `DELETE` of a file or
  directory, for the caller's scope. User-scoped memory: the caller's own,
  `agent:read` to view and `agent:run` to delete. Tenant-scoped: `agent:read`
  to view, `agent:admin` to delete. An agent without memory is 404.
- Console: a Memory page per agent: files, usage against the caps, a file's
  text, delete with confirmation. No editing in v1 (the agent edits).
- Coordinate the console's navigation with E22's Connectors page.

## Acceptance criteria

- [ ] View and delete through the API, scoped: user A never sees user B's memory. — test: `agentapi.TestMemoryEndpoints`
- [ ] Scopes are enforced as above. — test: `TestMemoryEndpointScopes`
- [ ] Usage and limits are reported. — test: `agentapi.TestMemoryUsage`
- [ ] The Memory page lists, shows and deletes. — test: console `memory.test.tsx`
