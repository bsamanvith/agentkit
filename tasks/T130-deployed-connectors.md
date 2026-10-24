---
id: T130
title: "Serve every connector in the k8s and Compose deployments"
epic: E22
status: todo
depends_on: [T120]
---

# T130: Connectors in the deployed configs

## Context

[T120](T120-console-connectors-page.md) made `deploy/local/agent/search.json` serve `gmail`, `calendar`, `drive`, `sheets`, `hubspot`, `salesforce`, `exa` and `parallel` with per-user slots, optional `credentials.oauth2` entries whose client ids and secrets come from the environment, and an `assistant` agent with tool search. `deploy/k8s` and `deploy/compose` run the split deployment (agent API, runner, toolsets as separate components), which T120 couldn't run in a cloud session.

## Scope

- `deploy/k8s/base` and `deploy/compose`: the same toolsets, `credentials.oauth2` entries and `assistant` agent, with the vault on Postgres (`credentials.backend` `postgres`) shared by the agent API and the toolsets component, and the redirect URL of each deployment's console (`http://localhost:30080/oauth/callback` on k8s).
- The provider variables as optional entries of the existing Secret (k8s) and `.env` (Compose); unset, the slots show "Not set up".
- `make k8s-smoke` and the Compose smoke check `/v1/credentials` lists the six account slots.

## Out of scope

- A real provider sign-in in CI (no accounts).

## Acceptance criteria

- [ ] The k8s and Compose configs load with no provider variables and list every connector slot — test: `cmd/agentkit.TestDeployedConfigsServeConnectors`
- [ ] On the split deployment a connected slot's token reaches the toolsets component — test: the smoke suite, with the fake token server

## Notes

-
