---
id: T120
title: "Console: the Connectors page, and deployed configs that serve every connector"
epic: E22
status: done
depends_on: [T116, T117, T118, T119]
---

# T120: Connectors page

## Context

[E22](E22-connectors-and-tool-picker.md): one page where each person connects their own Google products, Salesforce, HubSpot, Exa and Parallel. The Connections page ([docs/UI.md](../docs/UI.md) "Connections") shows only slots of agents the person uses, as table rows.

## Scope

- **Page:** "Connections" becomes **Connectors** (route `/connections` kept). A card per connector the server serves with a credential slot, grouped by provider (Google, CRM, Search, Other), each with its name, a line on what it does, the tools it brings, status, and Connect / Replace key / Reconnect / Disconnect as today (OAuth in a popup, keys in a dialog). Slots no agent uses are listed too. Server keys stay as a section below. A filter box when there are more than eight.
- **Connector words** live in the console (`connectors-catalog.ts`: type → name, provider, description); an unknown type falls back to its name.
- **Deployed config:** `deploy/local/agent/search.json` serves `gmail`, `calendar`, `drive`, `sheets`, `hubspot`, `salesforce`, `exa` and `parallel`, each optional with a user-scope slot, and an `assistant` agent with all of them and tool search on. Each OAuth slot gets a `credentials.oauth2` entry whose client id and secret come from the environment; with them unset the slot is listed and says the server isn't set up to connect it (`client_id_env` and `optional` on a `credentials.oauth2` entry). `deploy/k8s` and `deploy/compose` follow in [T131](T131-deployed-connectors.md).
- **docs:** UI.md's Connections section, QUICKSTART (how to register the Google, HubSpot and Salesforce apps and set their variables).

## Acceptance criteria

- [x] Every slot the server lists is a card, grouped by provider, including slots no agent uses — test: `admin.test.tsx` "the connectors page" › "lists every slot by provider"
- [x] An OAuth card connects through the popup and a key card through the dialog, as before — tests: the existing connections tests in `admin.test.tsx` and `app.test.tsx`
- [x] A slot the server can't connect (no OAuth app) says so and has no Connect button — test: `admin.test.tsx` "says an unconfigured OAuth slot can't be connected here, with no Connect button"
- [x] More than eight cards get a filter — test: `admin.test.tsx` "filters the cards when there are more than eight"
- [x] The server lists an OAuth slot whose app is not configured, with `oauth: false`; with the app's variables set, each slot connects asking for the scopes its toolset's settings need — test: `cmd/agentkit.TestLocalConfigServesConnectors`
- [x] The local config loads with no provider variables set, and every connector toolset is optional with a user slot; the `assistant` agent has them all with tool search — test: `cmd/agentkit.TestLocalConfigServesConnectors`
- [x] `client_id_env` reads the client id from the environment; an `optional` entry with its id or secret unset is left out instead of failing startup, even with no vault — test: `cmd/agentkit.TestOAuth2AppFromEnv`

## Notes

-
- The page tests live in `admin.test.tsx` beside the other admin pages' (its fake server already serves `/v1/credentials` and `/v1/toolsets`), not a new `connectors.test.tsx`.
- The k8s and Compose configs were split out to T131: they can't be run in a cloud session (no Docker), and their vault is shared by the split components, which needs checking on a real cluster.
- The agent builder (T077) landed with its own toolset list first, so the planned shared `ToolsetChecklist` was dropped; the builder shows each connector's name from `connectors-catalog.ts` instead.
