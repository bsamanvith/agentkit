---
id: T131
title: "Serve every connector in the k8s and Compose deployments"
epic: E22
status: done
depends_on: [T120]
---

# T131: Connectors in the deployed configs

## Context

[T120](T120-console-connectors-page.md) made `deploy/local/agent/search.json` serve `gmail`, `calendar`, `drive`, `sheets`, `hubspot`, `salesforce`, `exa` and `parallel` with per-user slots, optional `credentials.oauth2` entries whose client ids and secrets come from the environment, and an `assistant` agent with tool search. `deploy/k8s` and `deploy/compose` run the split deployment (agent API, runner, toolsets as separate components), which T120 couldn't run in a cloud session.

## Scope

- `deploy/k8s/base/config.json` (which Compose mounts too): the same toolsets, `credentials.oauth2` entries and `assistant` agent.
- The toolsets image has no store and no vault (`nopostgres`, `noagent`), so it reads each call's credential from the agent API over RPC, the way the runner reads memory: package `pkg/credentials/credrpc`, served at `/rpc/credentials/` for the slots the served toolsets name, dialed with `AGENTKIT_CREDENTIALS_URL`. The API keeps refresh tokens and client secrets; only the access token, key or password crosses.
- `credentials.backend` `"store"`: the vault goes where `$AGENTKIT_STORE` is (memory, SQLite or Postgres), and there is none without a key ring, so one config serves every deployment.
- A `redirect_url` or `return_urls` entry that is a path is on `$AGENTKIT_PUBLIC_URL` (`http://localhost:30080` on k8s, `:30081`/`:30082` on Compose).
- The key ring and the provider variables as optional entries of the Secret (k8s, `scripts/k8s-local.sh` adds `credentials-key` and copies `GOOGLE_`, `HUBSPOT_`, `SALESFORCE_OAUTH_CLIENT_ID`/`_SECRET` from the environment) and `.env` (Compose, `scripts/compose-local.sh` adds `AGK_CREDENTIALS_KEY`); unset, the slots show "Not set up".
- The provider presets move to the leaf package `pkg/credentials/providers`, so the API image (built `nomcp`) knows `profile: "google"` without the MCP SDK.

## Out of scope

- A real provider sign-in in CI (no accounts).

## Acceptance criteria

- [x] The deployed config loads with no provider variables and lists every connector slot; with them every slot connects, back to `$AGENTKIT_PUBLIC_URL/oauth/callback` — test: `cmd/agentkit.TestLocalConfigServesConnectors/deployed`
- [x] Backend `"store"` follows `$AGENTKIT_STORE`, keeps no vault without a key ring, refuses a path — test: `cmd/agentkit.TestStoreBackend`
- [x] Path URLs are on the public URL; a redirect path without one is refused by name — test: `cmd/agentkit.TestCredentialsOnPublicURL`
- [x] On the split deployment a user's key saved at the API reaches the toolsets component, each user their own — test: `cmd/agentkit.TestUsersConnectTheirOwnExaKey/credentials_from_the_API`
- [x] The refresh token never leaves the API; only the slots served, at their scope, with the RPC token — tests: `pkg/credentials/credrpc.TestRefreshTokenStaysWithTheVault`, `TestOnlyServedSlots`, `TestNeedsTheToken`, `TestRegisterChecksSlots`, `TestRemoteSourceUsesTheCallersKey`
- [x] The API serves `/rpc/credentials/`, the runner and toolsets don't — test: `test/e2e.TestComponents` (`make smoke`)

## Notes

- `oauth-callback` is now on the API's `/readyz` in the deployments: it has a vault.
