// Package connect gets credentials into the vault (T045, E07): it is the
// agent API's [agentapi.Credentials] (GET /v1/credentials, PUT and DELETE
// /v1/credentials/{slot}) and connects accounts with the OAuth2
// authorization code flow and PKCE (POST /v1/credentials/{slot}/connect,
// then the provider, then GET /oauth/callback).
//
// The slots it knows are the ones the server's toolsets name ([Slot]),
// each with the scope the toolsets use it with: "tenant" for one
// credential the tenant shares, which needs the scope credentials:admin to
// set, and "user" for each user's own. Listing shows every slot and whether
// it is connected for the caller, never a value.
//
// A connect flow is one [App] per slot: the provider's authorization and
// token endpoints, the client (its secret from the environment, never from
// config), the scopes and the redirect URI. Connect makes a random 256-bit
// state and a PKCE verifier and keeps them, with the tenant, the user, the
// slot, the return URL and an expiry (10 minutes), sealed in the vault's
// BlobStore under a reserved tenant (flows.go): any replica can finish the
// flow, whichever one the browser comes back to. The callback needs no bearer
// token, since the browser has none: the state alone names the flow, and its
// stored tenant and user, not anything in the callback's request, say where
// the tokens go. A state is used once: it is deleted when the callback reads
// it, before anything else is checked, so a failed callback cannot be
// retried. The return URL must be on an allowlist when the flow starts, so
// the callback is not an open redirect.
//
// The callback answers with a small page of its own, or, when the flow
// named a return URL, redirects there with "result" (connected, denied,
// expired, failed) and "slot" in its query. Neither carries a value, and
// both tell the browser not to send the callback's URL (which holds the
// code) on as a Referer.
//
// The provider presets of the shared OAuth core (package mcp/oauth, T095:
// "google", "salesforce", "microsoft") are applied by the config
// (cmd/agentkit) to an App's authorization parameters and scopes; the flow
// here differs from that package's in that the callback is the server's,
// not the console's, and the tokens go to the vault for a slot instead of
// to an MCP server's token store.
package connect
