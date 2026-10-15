// Package oauth connects users' accounts to external MCP servers with OAuth
// 2.1, the way the MCP authorization spec describes (T095, E19).
//
// One shared core does what is the same for every server:
//   - discovery: the server's 401 and its protected resource metadata
//     (RFC 9728), then its authorization server's metadata (RFC 8414, or
//     OIDC discovery);
//   - client registration, in this order: a pre-registered client from the
//     [Profile], our Client ID Metadata Document (CIMD), then dynamic client
//     registration (RFC 7591), registered once per tenant and issuer;
//   - the authorization code flow with PKCE S256 and the resource parameter
//     (RFC 8707), with a state that is single-use, expires and is bound to
//     the [Owner] who started it;
//   - tokens kept per owner in a [TokenStore], refreshed once however many
//     calls need it at the same time (and compare-and-swap across replicas),
//     and "reconnect needed" when the refresh token is refused;
//   - [Connector.OAuthHandler], which the Go SDK's client transport calls on
//     every request: it adds the owner's token, and turns a 401 or 403 into
//     [ErrNotConnected], [ErrReconnectNeeded] or [ErrNeedsMoreAccess], never
//     into a sign-in nobody is there to finish.
//
// What differs from one provider to the next is data, in a [Profile]:
// a pre-registered client, fixed endpoints where there is no discovery,
// extra authorization parameters, scopes, whether to send resource, a token
// lifetime when the provider leaves expires_in out, and which extra token
// fields to keep. [Profiles] has presets for the spec ("mcp"), Salesforce,
// Google and Microsoft; a toolset's config overrides any field. A provider
// that needs code gets one hook, [Profile].OnToken.
//
// The flow is split in two because agentkit is a web server, not a desktop
// app waiting in one call: [Connector.Begin] returns the URL to send the
// browser to, and [Connector.Complete] takes what the authorization server
// sent back to the callback. The callback page is the console's: it posts
// the code and state to the API with the user's own credential, which is
// how the state is checked against the principal that started the flow.
// [Connector.Handler] serves the client metadata document.
//
// From the Go SDK it uses package oauthex (metadata types, protected
// resource and authorization server metadata, dynamic registration) and
// auth.GetAuthServerMetadata; from golang.org/x/oauth2 the PKCE helpers, the
// code exchange and the refresh. The SDK's AuthorizationCodeHandler is not
// used: it runs the whole flow inside one call and keeps one token per
// handler, where agentkit needs a redirect between two requests and a token
// per owner.
//
// Tokens are [Secret]s: they print, log and marshal as "[redacted]". The
// stores here keep them in memory; the vault (T042) seals them at rest.
package oauth
