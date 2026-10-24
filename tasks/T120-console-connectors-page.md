---
id: T120
title: "Console: the Connectors page, and deployed configs that serve every connector"
epic: E22
status: todo
depends_on: [T116, T117, T118, T119]
---

# T120: Connectors page

## Context

[E22](E22-connectors-and-tool-picker.md): one page where each person connects their own Google products, Salesforce, HubSpot, Exa and Parallel. The Connections page ([docs/UI.md](../docs/UI.md) "Connections") shows only slots of agents the person uses, as table rows.

## Scope

- **Page:** "Connections" becomes **Connectors** (route `/connections` kept). A card per connector the server serves with a credential slot, grouped by provider (Google, CRM, Search, Other), each with its name, a line on what it does, the tools it brings, status, and Connect / Replace key / Reconnect / Disconnect as today (OAuth in a popup, keys in a dialog). Slots no agent uses are listed too. Server keys stay as a section below. A filter box when there are more than eight.
- **Connector words** live in the console (`connectors-catalog.ts`: type → name, provider, description); an unknown type falls back to its name.
- **Deployed configs:** `deploy/local/agent/search.json`, `deploy/k8s` and `deploy/compose` serve `gmail`, `calendar`, `drive`, `sheets`, `hubspot`, `salesforce`, `exa` and `parallel`, each optional with a user-scope slot, and an `assistant` agent with all of them and tool search on. Each OAuth slot gets a `credentials.oauth2` entry whose client id and secret come from the environment; with them unset the slot is listed and says the server isn't set up to connect it.
- **docs:** UI.md's Connections section, QUICKSTART (how to register the Google, HubSpot and Salesforce apps and set their variables).

## Acceptance criteria

- [ ] Every slot the server lists is a card, grouped by provider, including slots no agent uses — test: `connectors.test.tsx` "lists every slot by provider"
- [ ] An OAuth card connects through the popup and a key card through the dialog, as before — tests: the existing connections tests, moved
- [ ] A slot the server can't connect (no OAuth app) says so and has no Connect button — test: `connectors.test.tsx` "unconfigured OAuth slot"
- [ ] The server lists an OAuth slot whose app is not configured, with `oauth: false` — test: `cmd/agentkit.TestCredentialsListUnconfiguredSlot`
- [ ] The local config loads with no provider variables set, and every connector toolset is listed as optional — test: `cmd/agentkit.TestLocalConfigServesConnectors`

## Notes

-
