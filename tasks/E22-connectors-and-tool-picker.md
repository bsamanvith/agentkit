---
id: E22
title: "Connectors page, per-chat tool picker and tool search"
status: in-progress
---

# E22: Connectors page, per-chat tool picker and tool search

Owner request (2026-10-24): "Let's add a connectors page where they can create credentials for Gmail, Sheets/Drive/..., Parallel, Exa, Salesforce, HubSpot etc. In the chat, they can pick which tools are available. And then we either expose these toolsets directly or via search: enable tool search in the chat."

Three parts, each usable on its own:

1. **Connectors**: every person connects their own accounts and keys, on one page of the console. What exists: the vault ([T042](T042-credentials-core.md)), credential slots on toolsets, the connect flow for OAuth slots (`credentials.oauth2`, profiles from [T095](T095-mcp-oauth-client.md)), and the Connections page, which shows only the slots of the agents a person uses. Missing: the Google connectors read their token from the environment only (not from a slot), there is no Sheets, HubSpot or Salesforce connector, no deployment config serves the connectors with slots, and the page is a table of rows rather than a catalog of what can be connected.
2. **Tool picker**: a chat picks which of its agent's toolsets the model gets. Today every run gets all of the agent's tools.
3. **Tool search**: a chat chooses whether its toolsets' tools are sent directly or held back behind `tool_search`, which loads them on demand. The harness already defers tools per agent (`defer_loading`, native or emulated search); a run cannot choose yet.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| What a chat can pick | A **subset of its agent's toolsets**, never more: an agent's tools, policy and credentials are set up when the server starts, and a run only narrows them, as `tool_policy` does. The deployed configs give the default assistant every connector (optional), so the picker has them all. |
| How a run says it | `POST /v1/runs` takes `"tools": {"toolsets": [...], "search": "direct" \| "search"}`. Absent fields keep the agent's. Unknown toolset names are 400. The run keeps the choice on resume, like `tool_policy`. Tools that belong to no toolset (skills, memory, `ask_user`) are always kept and never deferred. |
| Search mode | `search` defers every tool of the chosen toolsets behind `tool_search` (native for Claude, emulated for other providers, as the agent's `model.tool_search`); `direct` sends them all and defers none, overriding the agent's `defer_loading`. |
| Where the console keeps the choice | Per conversation in the browser (localStorage), sent with every run; a new chat starts from the agent's defaults. The server keeps it per run, so another client may choose differently. |
| One slot per Google product or one per account | **One per product** (`gmail`, `calendar`, `drive`, `sheets`): each connect asks for that product's scopes only (the narrowest-scope rule of E21). Several products of one account are several connects. |
| HubSpot and Salesforce | Our own connectors over their REST APIs with plain `net/http` (E21's rule), per-user OAuth through a slot. HubSpot gets an OAuth profile preset; Salesforce uses the existing one and the token's `instance_url`. |
| The page | Renamed **Connectors**, same route (`/connections`, so connect flows in flight still land). A card per connector the server serves with a slot, grouped by provider, whether or not an agent uses it yet; server keys stay listed as today. |
| Cassettes | Synthetic, from the providers' references (owner: real-account cassettes not required). The new connectors stay `review` only for the mutation list, like the others. |

## Tasks

- [x] [T114](T114-run-tool-selection.md) Run API: a run picks its toolsets and the tool search mode
- [x] [T115](T115-console-tool-picker.md) Console: the tool picker in the chat composer
- [x] [T116](T116-google-slot-tokens.md) Google connectors take the user's token from a credential slot
- [ ] [T117](T117-connector-google-sheets.md) `connectors/google/sheets`
- [ ] [T118](T118-connector-hubspot.md) `connectors/hubspot`
- [ ] [T119](T119-connector-salesforce.md) `connectors/salesforce`
- [ ] [T120](T120-console-connectors-page.md) Console: the Connectors page, and the deployed configs that serve every connector

T114 then T115; T116 then T117; T118 and T119 alongside; T120 last (it lists what the others add). Each image change of 100 KB or more gets a row in [docs/SIZES.md](../docs/SIZES.md); a budget is raised by the minimum.
