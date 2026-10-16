package common

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
