package common

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// TokenSource yields the OAuth 2.0 access token to send. It is called for
// every request, so an implementation caches and refreshes. It must be safe
// for concurrent use.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to a [TokenSource].
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token calls f.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// Invalidator is implemented by a [TokenSource] that can refresh. The client
// calls Invalidate(token) when Google answers 401 to token, then asks for a
// token again and retries the request once.
type Invalidator interface {
	Invalidate(token string)
}

// ErrNoToken is what a [TokenSource] returns when the user has not connected
// a Google account (or the connection was revoked). It reaches the model as a
// tool error saying to reconnect the account.
var ErrNoToken = errors.New("no Google account is connected")

// EnvTokens returns a source for the token held in the environment variable
// name, read now through lookup. It fails when the variable is not set or is
// empty, naming the variable and never its value. The token is static: when
// it expires, requests fail with a 401 until the process restarts with a new
// one.
func EnvTokens(lookup func(string) (string, bool), name string) (TokenSource, error) {
	var v string
	if lookup != nil {
		v, _ = lookup(name)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, fmt.Errorf("$%s is not set", name)
	}
	return TokenSourceFunc(func(context.Context) (string, error) { return v, nil }), nil
}

// ErrNotOAuth reports a credential slot that holds another kind of
// credential (an API key, a user name and password) than the OAuth2 token
// of a Google account.
var ErrNotOAuth = errors.New("the connected credential is not a Google account")

// FromSource returns a source of the access token of the user each call
// runs for, from the toolset's credential slot (T116): the slot's OAuth2
// token, refreshed by src as it expires. A caller who has not connected an
// account gets [ErrNoToken].
func FromSource(src credentials.Source) TokenSource {
	return TokenSourceFunc(func(ctx context.Context) (string, error) {
		c, err := src.Get(ctx)
		if errors.Is(err, credentials.ErrNotConnected) {
			return "", ErrNoToken
		}
		if err != nil {
			return "", err
		}
		tok, ok := c.(credentials.OAuth2)
		if !ok {
			return "", fmt.Errorf("%w (it is %s): connect the Google account instead", ErrNotOAuth, c.Kind())
		}
		return tok.AccessToken.Reveal(), nil
	})
}
