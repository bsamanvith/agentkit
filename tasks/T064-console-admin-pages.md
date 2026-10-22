---
id: T064
title: "Console admin pages: agents, toolsets and tools, credentials"
epic: E11
status: done
depends_on: [T055, T062]
---

# T064: console admin pages

## Context

The admin half of the console (owner, 2026-10-12): see the agents, the MCP toolsets and their tools, and credentials. Built only on `/v1` (T062); admin-only content is enforced by the API (T061's roles). Design: [docs/UI.md](../docs/UI.md). Decisions: [E11](E11-clients.md).

## Scope

- **Agents:**
  - list (name, description, model, toolsets, skills, memory);
  - detail with the system prompt (admin);
  - a "Chat with this agent" button.
- **Toolsets and tools:**
  - each toolset with its status (ready, or skipped with the reason) and tool count;
  - its MCP URL with a copy button and a ready-to-paste `claude mcp add` command (token placeholder, never the real token);
  - each tool with its description and input and output schemas, as a readable field list plus raw JSON.
- **Credentials:**
  - v1: per toolset, status (configured, missing, keyless), the env var or slot name, never values; how to set it;
  - v2 (after T045): set, rotate and delete keys, and connect OAuth accounts.
- **Navigation:** admin pages are hidden for `user` roles; the API returns 403 anyway (tested).
- Browser tests (Playwright) for each page against `agentkit serve` with fakes.

## References

- [docs/UI.md](../docs/UI.md) and T070's approved mockup v3
- Vercel and Linear dashboards (quiet admin tables, detail panes)
- JSON Schema rendering in Swagger UI / Redoc for the tool field lists

## Acceptance criteria

- [x] **Agents page:** lists the configured agents; the detail shows the system prompt only to `admin`; "Chat with this agent" opens a new conversation with that agent.
- [x] **Toolsets page:** shows a skipped optional toolset with its reason; the copy command contains the right URL and no token.
- [x] **Tools:** each tool's schema renders as a readable field list.
- [x] **Credentials page:** shows status and variable names; a canary key value never reaches the page (DOM check).
- [x] **Roles:** a `user` session sees no admin navigation, and direct API calls to admin data return 403.
- [x] **Mutation list** recorded.

## Notes

### What was built

- **Agents** (`pages/agent.tsx`): the detail shows the description, the toolsets that aren't ready, model, toolsets (linked, with status), skills, memory and the answer format. The system prompt is in a closed "System prompt (admins only)" section, shown only when `GET /v1/agents/{name}` sends `system`. The API decides that (scope `admin:agents`); the page never infers it. "Chat with this agent" goes to `/new?agent=`.
- **Toolsets** (`pages/toolset.tsx`, `mcp.ts`, `schema.ts`):
  - callouts for skipped (with the reason), unavailable, review needed and a credential that needs attention;
  - the MCP URL and `claude mcp add … --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`, each with a copy button. The token is always the shell placeholder;
  - each tool as a `<details>` holding Input and Output tables. The tables come from the JSON Schema: nested `a.b` and `a[].b` rows, types (enum, const, anyOf, nullable, format, `$ref` within `$defs`, with cycle protection), required, and help from description, default and limits. A "Show raw schema" toggle shows the JSON.
- **Connections** (`pages/connections.tsx`, `connections-model.ts`, `credential.ts`): one row per credential an agent uses.
  - Server keys show their env var and status: "Using the server key", "Keyless", or "Server key not set" with how to set it.
  - Slots show Connected (with the date), Needs reconnect or Not connected.
  - A key is set in a dialog. The input is a password field with autocomplete off and a Show toggle; it is sent once by PUT and cleared.
  - Disconnect asks first.
  - OAuth opens the provider in a popup (opened in the click, given its URL after `POST …/connect`), and `/connections/done` reports back over a BroadcastChannel. If popups are blocked, the same tab is used and the return route is saved in sessionStorage.
  - With no vault (404), the page says how to turn one on.
- **Roles** (`access.ts`, `shell.tsx`, `admin.tsx`): the Agents and Toolsets navigation is hidden when `/v1/auth/me` says the caller is not an admin (the admin role, or a scope matching `agent:admin`, matched the way Go's `path.Match` does). It appears only once `/me` confirms an admin, never while it is loading (docs/UI.md "Rules"). A direct link still loads its list until the caller is known not to be an admin. The "Admins only" page is shown then, and also on a 403 from the list. Connections is for everyone.
- **API client** (`agent-ui`): `getAgent`, `listToolsets`, `listTools`, `listCredentials`, `putCredential`, `deleteCredential` and `connectCredential`, with their types.

### Tests

- Unit tests: `src/pages/admin.test.tsx` has 58 (with its tables' cases), and `agent-ui` `client.test.ts` adds 3.
- Browser tests: `e2e/admin.spec.ts` has 7, run against `agentkit serve` (`e2e/admin.json`: local users ada and uma, a memory vault, a skipped optional toolset, `EXA_API_KEY` set to a canary) and `e2e/fake-provider.ts` (OAuth2 authorize and token, whose tokens hold a canary).
- The canaries (the server key, a typed key, the provider's token) are checked against the page's HTML, its fields' values, its storage, the clipboard, the page's URL and `GET /v1/credentials`. In the connections tests they are also checked against every request URL and console line in every window, the popup included, and against every request body except the one PUT to `/v1/credentials/crm`.
- Ports: 18482 (server) and 18483 (provider), clear of T074's 18480 and 18481.

| Criterion | Tests |
|---|---|
| Agents page | unit "lists the agents and shows the detail…", "shows no system prompt when the API sends none", "Chat with this agent"; e2e "agents: …" |
| Toolsets page | unit "shows a skipped toolset with its reason", "gives the MCP URL and a claude mcp add command with a placeholder, never the token" (with a token canary, including the clipboard); e2e "toolsets: …" (it also POSTs `tools/list` to the URL) |
| Tools | unit "renders each tool's schema as a readable field list…", "shows a tool's untrusted description, title and schema text as text", plus the `fields` and `typeOf` tables; e2e "toolsets: …" |
| Credentials page | unit statuses, key set once, disconnect, no vault, `/connections/done` in the same tab (the saved route, and never another host), `isLocalPath`; e2e "connections: …" (key, OAuth popup, disconnect, reload), "Cancel at the provider…", "with popups blocked, the same tab…" |
| Roles | unit "hides the admin navigation from a user, and shows the 403 page…", "keeps the admin navigation hidden until /me says…", "shows the 403 page when the API refuses an admin list", `isAdmin` and `hasScope` tables; e2e "roles: …" (403 from versions, triggers, webhooks secret and review, and no `system` in the agent) |
| Phone | e2e "on a phone, the pages fit without sideways scroll" |

### Mutation list

Each bug below was planted and then reverted. The tests that failed are listed.

- `isAdmin` ignores the admin role: the `isAdmin` table and the session tests fail.
- `isAdmin` is always true: "hides the admin navigation from a user…" fails.
- The admin navigation is shown while `/me` is unknown (`me === null || isAdmin(me)`): "keeps the admin navigation hidden until /me says the caller is an admin" fails.
- The system prompt is shown without `system`: "shows no system prompt when the API sends none" fails.
- The real token goes into the command: "…with a placeholder, never the token" fails.
- A 403 from the API is not treated as forbidden: "shows the 403 page when the API refuses an admin list" fails.
- `required` is never set: the field-list tests fail.
- Rows that need you are not sorted first: the `connectionRows` ordering test fails.
- The BroadcastChannel handler is a plain closure instead of `useEffectEvent`: e2e "connections: …" fails. This was a real bug: the row stayed "Not connected" after the popup, because the handler reloaded with the credential from before sign-in.
- From the review:
  - `isLocalPath` without its `/\` check: "never leaves the console for a saved /\evil.example/x" and the `isLocalPath` table fail.
  - `connectCredential` takes any non-empty `authorize_url`: the client test with `javascript:` and `data:` URLs fails.
  - The key dialog sends `scope: "tenant"`: "shows statuses and variable names, sets a key once…" fails.
  - The key dialog logs the key (`console.debug`): e2e "connections: …" fails on its console lines.
  - `inPopup` is always false: e2e "connections: …" and "Cancel at the provider…" fail, because the popup never reports back or closes.
  - The field list renders `help` with `dangerouslySetInnerHTML`: "shows a tool's untrusted … as text" fails.
- Not caught: removing the clearing of the key input after a save. The dialog unmounts right after, so the value leaves the DOM either way. The clearing is defence in depth, and the canary DOM checks cover the result.

### Decisions

- One admin test, after rebasing on T074: `sections.ts`'s `mayAdmin` (the rail, the drawer, the Tasks view's Triggers tab) is `me !== null && isAdmin(me)`. Unknown is no, and scopes match as patterns. T074's provider reads `/me` once per sign-in; the shell only reads it again when the tab regains focus.
- Admin navigation shows only after `/me` confirms an admin. A server without credentials answers `/me` with no credential, and that counts as an admin. The API's 403 is the real gate.
- Connections lists only credentials that some agent uses, and puts what needs you first.
- Only API keys can be set from the UI. The vault's `basic` and `client_credentials` kinds wait for a connector that needs them.
- The popup reports back over a `BroadcastChannel`, not `postMessage` to `window.opener`. A channel is same-origin by construction, so no origin check is needed, and it still works when a provider's opener policy cuts the opener. The page reads only the `slot` and `result` strings from the message, and then reads the status again from the API. docs/UI.md says so.
- Hardening from the review:
  - `connectCredential` accepts only an http(s) `authorize_url`, because the popup is an about:blank window of the console's own origin until it navigates.
  - `/connections/done` follows only a saved route that `isLocalPath` accepts (no `//` or `/\`).
  - `inPopup` tests `window.opener` for truthiness, so an undefined opener (jsdom) counts as none, the same as `null`.
  - `claude mcp add` single-quotes a name or URL that a shell would read specially. Names are `[a-zA-Z0-9_-]` today, so the command is unchanged.
- Native `<dialog>` instead of a Radix modal (CSP, size). The admin pages are in the lazy `admin` chunk, so the console's first load is unchanged.

### Found out of scope (owner questions)

- `admin:*` does not match `credentials:admin`, so an admin cannot set the tenant's shared credentials from the console. The page offers only the caller's own (`scope: user`) slots and the server-key instructions. Should the admin role include `credentials:admin`?
- `GET /v1/toolsets` and `GET /v1/toolsets/{name}/tools` need only `agent:read`, so a user can read the catalog through the API even though the console hides the pages. Is that intended (users need it for Connections), or should tools need an admin scope?
- The API has no display name or help link for a connector or slot, so rows show the slot name.
- T063's MCP-only toolsets are not in `GET /v1/toolsets`, so they are not listed.

### For T077, T087 and T095

- Reuse `pages/parts.tsx` (`Callout`, `Facts`, `Section`, `CopyLine`, `Modal`, `Forbidden`) and `access.ts` (`isAdmin`, `hasScope`) instead of copying them.
- An editor for agents (T077) can put its form in `ItemPage`'s `detail`; `useForbidden` already handles the 403.
- A new credential kind adds a dialog next to `KeyDialog` and an action in `connectionRows`. The tests' canary pattern (`e2e/token.ts`) should cover it.
- The admin e2e server is in `playwright.config.ts`, which runs two webServers on 18482 and 18483. Add new admin fixtures to `e2e/admin.json` rather than starting another server.
