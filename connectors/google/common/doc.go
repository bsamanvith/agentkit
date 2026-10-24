// Package common is the code the Google connectors (connectors/google/gmail,
// calendar and drive) share: one HTTP client for Google's REST APIs, how a
// connector gets its access token, settings every Google connector has, paging
// and text limits. It is a library, not a connector: it makes no tools of its
// own and is not run through the connector test kit.
//
// # Access tokens
//
// Google's APIs take an OAuth 2.0 access token for one user's account
// ("Authorization: Bearer"). A connector gets it from a [TokenSource]:
//
//   - a factory option (WithTokenSource in each connector) supplies one, which
//     is how the OAuth core (pkg/mcp/oauth, the "google" profile) and the
//     credential vault will hand each user's token to the tools;
//   - without one, the "access_token_env" setting names an environment
//     variable that holds a token ([EnvTokens]). That is for development and
//     for a single operator-owned account; it is a static token that expires
//     in about an hour.
//
// A literal token in settings is refused, as an API key is: config files get
// committed. The token never leaves this package except in the Authorization
// header; text taken from a response has it replaced with "[redacted]".
//
// # The client
//
// [Client] is the token connectors' shared client (connectors/internal/rest)
// reading Google's error bodies: [Client.Do] sends one request and returns
// the body of a 2xx, reads at most 8 MiB, and maps failures to the errors
// tools return. Google's 400, 409 and 422 give [tool.ErrInvalidInput] with
// Google's message (at most 300 characters); every other failure gives
// [tool.ErrToolFailed] with a cause the model or the user can act on ("gmail
// rejected the access token", "rate limited"). A 429, or a 403 whose reason
// is a rate or quota limit, is retried once after its Retry-After (up to
// 10 s, and only while the context allows). A 401 is retried once with a
// fresh token when the source can refresh ([Invalidator]). Nothing else is
// retried: a request that sends mail or deletes a file may already have run.
//
// # Paging and text
//
// [Paginate] collects items across pages up to a limit and returns the token
// for the next page. [Truncate] and [TruncateContent] cut text at a rune
// boundary with a marker that tells the model how to get the rest.
package common
