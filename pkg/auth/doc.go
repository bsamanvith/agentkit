// Package auth authenticates requests at the edge and turns each caller into
// a [Principal]: the tenant it acts for, the user, the scopes it holds and the
// toolsets it may use.
//
// One [Middleware] sits in front of every surface (the agent API under /v1
// and the MCP endpoints under /mcp/), so both see the same principal, the same
// tenant rules and the same 401 behavior. It asks an [Authenticator] who is
// calling, then puts the principal in the request context ([From]) together
// with the tenant, user and request id of package tenancy, which is what the
// store and the logs read. Handlers below never parse credentials: the
// middleware removes the Authorization header from the request they get.
//
// An Authenticator answers one of three ways:
//   - a principal: the request is authenticated;
//   - [ErrNoCredentials]: the request carries nothing this authenticator
//     reads, so the next one in a [Chain] may try;
//   - [ErrInvalidCredentials]: a credential was presented and is wrong. The
//     chain stops there, and the middleware answers 401 with
//     error="invalid_token".
//
// Any other error is internal: the middleware answers 500 and logs it.
// Authenticators never put a credential, or anything derived from one such as
// a hash, in an error, and never change the request.
//
// [APIKeys] is the first Authenticator: bearer keys whose SHA-256 hashes are
// configured, each granting a fixed principal. Package authtest has a fake
// and the conformance suite every Authenticator passes.
//
// This package depends on tenancy and never the reverse: tenancy carries
// labels and authenticates nobody.
package auth
