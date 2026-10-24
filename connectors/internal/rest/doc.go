// Package rest is the HTTP client of connectors that act for one user's
// account with an OAuth 2.0 access token ("Authorization: Bearer"): the
// Google family (through connectors/google/common), HubSpot and Salesforce.
// It is a library, not a connector: it makes no tools of its own and is not
// run through the connector test kit.
//
// # Access tokens
//
// The client asks a [TokenSource] for the token on every request: the
// caller's own from the toolset's credential slot ([FromSource]), or one
// static token from an environment variable for development ([EnvTokens]).
// The token never leaves this package except in the Authorization header;
// text taken from a response has it replaced with "[redacted]".
//
// # The client
//
// [Client.Do] sends one request and returns the body of a 2xx. It reads at
// most 8 MiB, applies a timeout to each attempt, and maps failures to the
// errors tools return: 400, 409 and 422 give [tool.ErrInvalidInput] (through
// [Invalid]) with the provider's message, at most 300 characters; every other
// failure gives [tool.ErrToolFailed] with a cause the model or the user can
// act on ("rejected the access token", "rate limited"). A 429, or what the
// provider's [Config.RateLimited] says is a rate limit, is retried once after
// its Retry-After (up to 10 s, and only while the context allows). A 401 is
// retried once with a fresh token when the source can refresh
// ([Invalidator]). Nothing else is retried: a request that sends mail or
// creates a record may already have run.
//
// Each provider says how to read its error bodies ([Config.ParseError]).
package rest
