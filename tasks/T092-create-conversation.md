---
id: T092
title: "POST /v1/conversations: create an empty conversation, then run in it"
epic: E11
status: done
depends_on: [T062, T053, T054]
---

# T092: create a conversation before its first message

## Context

Owner's request (2026-10-12). Until now a conversation existed only once a run started it: `POST /v1/runs` without `conversation_id` created one, and its ID arrived with `run_start`, mid-stream. A UI that addresses the chat page by conversation (`/c/{id}`) then has to change route while the answer streams, which is where remounts, lost streams and double sends come from. With an explicit create, the page has its ID first, loads by it, then sends.

Naming: **conversation**, not session. Everything already says conversation (`conv_` IDs, `/v1/conversations`, `wire.Conversation`, `-conversation`); "session" means the login session (T061) and MCP's `Mcp-Session-Id`.

## Scope

- `POST /v1/conversations` `{"agent"?}` (body optional) → `201` with the `Conversation` (no messages, `idle`) and `Location: /v1/conversations/{id}`. `400` bad body or no default agent, `401`, `404` unknown agent, `413`.
- `POST /v1/runs` with that `conversation_id` runs in it: its agent, an empty transcript; the first message sets the title. A run without `conversation_id` still creates one (both paths stay).
- `agentclient.CreateConversation(ctx, ConversationRequest{Agent})`.
- `agentkit create [-agent X] [-json]`: prints the ID (`-json`: the conversation); exit 0 or 2.
- `docs/api/openapi.yaml`, `pkg/agentapi` and `pkg/agentclient` docs, `docs/QUICKSTART.md`.

## Out of scope

- Persistence: conversations stay in memory (T036) until T037; the T037 task now says the store needs a conversation record of its own.
- Titles or metadata in the create request (the request is a struct, so they can be added).
- `agentkit chat` keeps creating on the first message, so opening and quitting chat leaves no empty conversation.

## Acceptance criteria

- [x] Create, then run: an empty idle conversation of the agent named, shown the same by GET and the list (untitled, 0 messages); the first run in it is that agent's, from an empty transcript, and titles it; a later run does not retitle it. — `TestCreateConversation`
- [x] Refusals change nothing: no default agent (no body, `{}`), unknown agent, unknown field, not an object, trailing data, wrong type; then in a created conversation, an empty message (400) and another agent (409 `agent_mismatch`). — `TestCreateConversationRefusals`
- [x] No body, blank body, `{}` and `{"agent":""}` give the default agent. — `TestCreateConversationDefaultAgent`
- [x] Tenants: another tenant cannot read, run in, delete or list it. — `TestCreatedConversationsAreTheCallersTenant`
- [x] Retention: past the count, empty conversations go first (oldest first), the one just created never; past the age, empty ones go like any other. — `TestRetentionDropsEmptyConversationsFirst`
- [x] Concurrent creates and runs (under `-race`): distinct IDs, each listed once. — `TestCreateConversationsConcurrently`
- [x] OpenAPI: every documented status of the new operation is produced, and its bodies (and a run in the created conversation) validate against the sealed spec. — `TestOpenAPIResponses`, `TestOpenAPISpec`
- [x] Client: request shape (`POST /v1/conversations`, JSON body), 201 decoded, a run in it, `ErrNotFound` for an unknown agent. — `TestRequests`, `TestCatalog`
- [x] CLI against the real binaries: `create` prints the ID, the first `ask` runs in it (`-v` shows that conversation); `create -json`; exit 2 for an unknown agent, an argument, server down, no token (with the hint), and no model call. — `TestE2EAsk`
- [x] Mutation list (Notes).

## Notes

### Decisions

- **Empty conversations are listed** (title `""`, `message_count: 0`), not hidden, and have **no shorter expiry**. When planning this I suggested hiding them and expiring them after an hour; I dropped both. Hiding needs a query parameter and makes the list disagree with `GET`, and a UI can filter on `message_count: 0` itself. An expiry needs a new option. Instead, **retention drops empty conversations first**, so a client that creates many and never uses them loses its own empty ones, not other conversations. The conversation just created is never the one dropped.
- **The body is optional**: none, or a blank one, is `{}`, so `curl -X POST .../v1/conversations` works. Unknown fields are refused, as on `POST /v1/runs`.
- **No `503` while draining**: creating a conversation starts no work. A run in it is refused as usual.
- **`touched`** (the eviction order) is set at creation, so a new empty conversation is the last empty one to go.
- **The CLI's `create`** registers `-server` and `-agent` only; `-conversation` would be meaningless.

### Files

- `pkg/agentapi`:
  - `catalog.go`: `createConversation`;
  - `server.go`: the route, `readBody` (shared with `POST /v1/runs`), the title set on the first message, `evict(now, keep)` with empty conversations first, `conversationView`;
  - `doc.go`;
  - tests: `create_test.go` (new) and the probes in `openapi_test.go`.
- `pkg/agentapi/wire/catalog.go`: `CreateConversationRequest`; the `Title` comment.
- `pkg/agentclient`:
  - `catalog.go`: `ConversationRequest`, `CreateConversation`;
  - `client.go`: `decodeStatus`;
  - `doc.go`;
  - tests: `catalog_test.go`, `client_test.go`.
- `cmd/agentkit`:
  - `create.go` (new);
  - `client.go`: `registerServer`;
  - `main.go`: dispatch and usage;
  - `cli_e2e_test.go`.
- `docs/api/openapi.yaml`, `docs/QUICKSTART.md`, `tasks/E11-clients.md`, `tasks/T037-agent-api-v1.md` (the store note).

### Mutation list

Each mutant was planted, the tests run (`pkg/agentapi` `TestCreate|TestRetention|TestOpenAPI`, all of `pkg/agentclient`, `cmd/agentkit` `TestE2EAsk`), and the source restored. All caught:

| Mutant | Caught by |
|---|---|
| 200 instead of 201 | TestCreateConversation (and every create test), TestOpenAPIResponses, TestCatalog, TestE2EAsk |
| no `Location` | TestCreateConversation (and every create test), TestOpenAPIResponses |
| the agent named ignored | TestCreateConversation, TestCreateConversationRefusals, TestOpenAPIResponses, TestCatalog, TestE2EAsk |
| tenant not recorded | TestCreatedConversationsAreTheCallersTenant |
| unknown fields accepted | TestCreateConversationRefusals, TestOpenAPIResponses |
| an empty body refused | TestCreateConversationDefaultAgent |
| no eviction on create | TestRetentionDropsEmptyConversationsFirst |
| eviction may drop the new conversation | TestRetentionDropsEmptyConversationsFirst |
| empty conversations not dropped first | TestRetentionDropsEmptyConversationsFirst |
| title never set by the first message | TestCreateConversation |
| title reset by every message | TestCreateConversation |
| client sends no body | TestCatalog, TestRequests, TestE2EAsk |
| `agentkit create` drops `-agent` | TestE2EAsk |
| `agentkit create` always prints JSON | TestE2EAsk |
| `agentkit create` exits 0 on an error | TestE2EAsk |

### For T055

The chat page should create, then navigate, then send:

1. On the first send of a new chat: `POST /v1/conversations {"agent": <picked>}`.
2. Navigate to `/c/{id}` (or replace the route).
3. `POST /v1/runs {"conversation_id": id, "message": ...}` and stream.

Create on the first send rather than on clicking "New chat", so an abandoned new chat leaves nothing behind. The page for `/c/{id}` always loads by ID. An empty conversation (`message_count: 0`) can show as "New chat" in the list, or be filtered out by the console.
