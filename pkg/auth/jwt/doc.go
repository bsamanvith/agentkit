// Package jwt is an auth.Authenticator of OAuth 2.0 / OpenID Connect bearer
// tokens: JWTs that an identity provider (Auth0, Okta, Google, Keycloak, ...)
// signs, verified here against the provider's published keys (its JWKS).
//
// agentkit is only a resource server. It verifies tokens and never issues
// them: no authorization server, no client registration, no introspection.
//
// A token is accepted when all of these hold:
//   - its signature verifies with a key of the JWKS, using one of the allowed
//     algorithms (RS256 and ES256 by default; never "none" or an HMAC);
//   - "iss" is exactly the configured issuer;
//   - "aud" names the configured audience, the resource URL of this server,
//     or a resource under it that holds the requested path (the MCP spec:
//     tokens are issued for this resource);
//   - "exp" is present and not past, "nbf" is not ahead and "iat" is not in
//     the future, each with the configured leeway (60 s by default);
//   - "kid" names a key, unless the set has exactly one key;
//   - the tenant is in its claim (or fixed in the configuration) and is a
//     valid tenant id.
//
// The principal's user is the "sub" claim, its scopes come from "scope" (a
// space-separated string) or "scp" (an array), and its toolsets from the
// scopes "mcp:<toolset>" and, if configured, a toolsets claim. Claim values are
// names, never patterns: a value with '*', '?', '[' or '\' is dropped, so a
// provider that lets clients ask for any scope cannot hand out "*".
//
// A bearer token that is not shaped like a JWT (three dot-separated base64url
// segments) is not this authenticator's: it answers auth.ErrNoCredentials, so
// in auth.Chain(jwt, apiKeys) an API key reaches the next authenticator. Every
// other failure is auth.ErrInvalidCredentials. Errors never hold the token or
// a claim's value.
//
// # Keys
//
// The JWKS is fetched from the configured URL, or from the jwks_uri of the
// issuer's OpenID configuration. It is fetched again when a token names an
// unknown key, or the set is older than its Cache-Control max-age (within
// [Config.MinRefreshInterval] and [Config.MaxCacheAge]), but never more than
// once per MinRefreshInterval, whatever the requests: a flood of tokens with
// made-up key ids cannot hammer the provider. A failed fetch keeps the last
// good set, so a provider outage only fails tokens signed by keys not seen
// yet, with 401. All times come from the injected clock.
package jwt
