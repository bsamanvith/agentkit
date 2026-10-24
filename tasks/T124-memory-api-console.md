---
id: T124
title: "Memory API and the console's Memory page"
epic: E23
status: done
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
- Console: a Memory section on the agent's page (`/agents/{name}`): files,
  usage against the caps, a file's text, delete with confirmation. No
  editing in v1 (the agent edits). It is a section, not a new navigation
  item, so E22's shell and navigation work is untouched.

## Acceptance criteria

- [x] View and delete through the API, scoped: user A never sees user B's memory. — test: `agentapi.TestMemoryEndpoints`
- [x] Scopes are enforced as above. — test: `agentapi.TestMemoryEndpointScopes`
- [x] Usage and limits are reported, from the real store, and a delete frees them. — test: `cmd/agentkit.TestMemoryAPI`
- [x] The endpoints are in the OpenAPI document with every status. — test: `agentapi.TestOpenAPIResponses` (probes `get_memory*`, `delete_memory*`)
- [x] The TS client views and deletes, refusing what is not a view. — test: `client.test.ts` "views and deletes an agent's memory"
- [x] The Memory section lists, shows and deletes after asking; tenant memory's Delete is hidden without `agent:admin`. — test: console `admin.test.tsx` "an agent's memory"

## Follow-up

The agents pages are for admins, so a user without the admin role sees
their memory through the API only. A user-facing view (from the chat's
agent menu, say) is a follow-up to plan with E22's chat work.
