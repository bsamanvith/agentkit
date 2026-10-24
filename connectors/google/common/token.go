package common

import (
	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// TokenSource yields the OAuth 2.0 access token to send. It is called for
// every request, so an implementation caches and refreshes. It must be safe
// for concurrent use.
type TokenSource = rest.TokenSource

// TokenSourceFunc adapts a function to a [TokenSource].
type TokenSourceFunc = rest.TokenSourceFunc

// Invalidator is implemented by a [TokenSource] that can refresh. The client
// calls Invalidate(token) when Google answers 401 to token, then asks for a
// token again and retries the request once.
type Invalidator = rest.Invalidator

// ErrNoToken is what a [TokenSource] returns when the user has not connected
// a Google account (or the connection was revoked). It reaches the model as a
// tool error saying to connect the account.
var ErrNoToken = rest.ErrNoToken

// EnvTokens returns a source for the token held in the environment variable
// name, read now through lookup. It fails when the variable is not set or is
// empty, naming the variable and never its value. The token is static: when
// it expires, requests fail with a 401 until the process restarts with a new
// one.
func EnvTokens(lookup func(string) (string, bool), name string) (TokenSource, error) {
	return rest.EnvTokens(lookup, name)
}

// ErrNotOAuth reports a credential slot that holds another kind of
// credential (an API key, a user name and password) than the OAuth2 token
// of a Google account.
var ErrNotOAuth = rest.ErrNotOAuth

// FromSource returns a source of the access token of the user each call
// runs for, from the toolset's credential slot (T116): the slot's OAuth2
// token, refreshed by src as it expires. A caller who has not connected an
// account gets [ErrNoToken].
func FromSource(src credentials.Source) TokenSource { return rest.FromSource(src, account) }
