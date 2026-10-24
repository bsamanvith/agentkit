package rest

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
// calls Invalidate(token) when the API answers 401 to token, then asks for a
// token again and retries the request once.
type Invalidator interface {
	Invalidate(token string)
}

// ErrNoToken is what a [TokenSource] returns when the user has not connected
// an account (or the connection was revoked). The client reports it naming
// the account ([Config.Account]) and saying to connect one.
var ErrNoToken = errors.New("no account is connected")

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
// credential (an API key, a user name and password) than an OAuth2 token.
var ErrNotOAuth = errors.New("the connected credential is not an OAuth account")

// SlotToken returns the OAuth2 token of the user ctx's call runs for, from
// the toolset's credential slot src, refreshed by src as it expires. A
// caller who has not connected an account gets [ErrNoToken]; a slot that
// holds another kind of credential, [ErrNotOAuth] naming account (as in
// "connect the HubSpot account instead").
func SlotToken(ctx context.Context, src credentials.Source, account string) (credentials.OAuth2, error) {
	c, err := src.Get(ctx)
	if errors.Is(err, credentials.ErrNotConnected) {
		return credentials.OAuth2{}, ErrNoToken
	}
	if err != nil {
		return credentials.OAuth2{}, err
	}
	tok, ok := c.(credentials.OAuth2)
	if !ok {
		return credentials.OAuth2{}, fmt.Errorf("%w (it is %s): connect the %s instead", ErrNotOAuth, c.Kind(), account)
	}
	return tok, nil
}

// FromSource returns a source of the access token of the user each call
// runs for, from the toolset's credential slot, as [SlotToken].
func FromSource(src credentials.Source, account string) TokenSource {
	return TokenSourceFunc(func(ctx context.Context) (string, error) {
		tok, err := SlotToken(ctx, src, account)
		if err != nil {
			return "", err
		}
		return tok.AccessToken.Reveal(), nil
	})
}

// Static returns a source that always yields token: one call's token, read
// from the slot with [SlotToken] when the call also needs the token's other
// fields (Salesforce's instance_url).
func Static(token string) TokenSource {
	return TokenSourceFunc(func(context.Context) (string, error) { return token, nil })
}
