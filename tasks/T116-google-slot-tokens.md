---
id: T116
title: "Google connectors take the user's token from a credential slot"
epic: E22
status: done
depends_on: [T108]
---

# T116: Google connectors read a credential slot

## Context

Gmail, Calendar and Drive ([E21](E21-google-connectors.md)) take a `TokenSource`, but the server builds them with `access_token_env` only: there is no way for each user to connect their Google account. Exa and Parallel already read a slot (`connectors/internal/apikey`). The connect flow for a slot exists (`credentials.oauth2` with profile `google`).

## Scope

- `common.FromSource(src credentials.Source)`: a `TokenSource` that returns the caller's OAuth2 access token (refreshed by the source), `ErrNoToken` wording for a caller with none, a clear error for a slot holding an API key.
- `common.Settings` resolution: a spec with a `credential` slot uses it; `access_token_env` and a slot together are refused (two places for one token).
- Each Google package exports `Scopes(settings)` (Drive has it; Gmail and Calendar get one), so a deployment's `credentials.oauth2` entry can name them.
- `credentialOf` in `cmd/agentkit/agent.go` reports `access_token_env` for Google toolsets (the T111 note).

## Acceptance criteria

- [x] A Google toolset with a slot sends the calling user's token, and another user's call sends theirs — test: `common.TestFromSourcePerCaller`
- [x] A user with no connected account gets "not connected" and no request is sent — test: `common.TestFromSourceNotConnected`
- [x] A slot and `access_token_env` together fail `Build` — test: `common.TestSlotAndEnvRefused` (`Env.TokensForSpec`, which every Google connector's `Build` calls)
- [x] `/v1/toolsets` shows a Google toolset without a token as missing `GOOGLE_ACCESS_TOKEN` — test: `cmd/agentkit.TestGoogleToolsetCredentialLine`

## Notes

- `Env.TokensForSpec(service, settings, in.Credentials)` replaces `TokensFor` in each `Build`: the slot when the spec names one, else the injected source or the environment, as before.
- No `Invalidator` on the slot source: `credentials.Source` has no "refresh now", it refreshes a token that expires within its margin. A 401 is "reconnect the account", as for a revoked token.
- `credentialEnv` had no `gmail` row, so Gmail toolsets showed "not needed": fixed.
