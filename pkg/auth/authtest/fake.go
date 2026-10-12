// Package authtest has a fake [auth.Authenticator] for tests of code behind
// auth.Middleware, and [RunAuthenticator], the conformance suite every
// Authenticator passes.
package authtest

import (
	"crypto/subtle"
	"fmt"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// Fake is an [auth.Authenticator] of bearer tokens, each granting a fixed
// principal. It reads the header with auth.BearerToken, like auth.APIKeys,
// but keeps the tokens themselves. It passes [RunAuthenticator].
type Fake struct {
	tokens     [][]byte
	principals []auth.Principal
}

// NewFake returns a Fake that answers each token of tokens with its
// principal. Use [Bearer] to put a token on a request.
func NewFake(tokens map[string]auth.Principal) *Fake {
	f := &Fake{}
	for tok, p := range tokens {
		f.tokens = append(f.tokens, []byte(tok))
		f.principals = append(f.principals, p.Clone())
	}
	return f
}

// Authenticate returns the principal of the request's bearer token.
func (f *Fake) Authenticate(r *http.Request) (auth.Principal, error) {
	tok, err := auth.BearerToken(r.Header)
	if err != nil {
		return auth.Principal{}, err
	}
	found := -1
	for i, want := range f.tokens {
		found = subtle.ConstantTimeSelect(subtle.ConstantTimeCompare(want, []byte(tok)), i, found)
	}
	if found < 0 {
		return auth.Principal{}, fmt.Errorf("%w: unknown token", auth.ErrInvalidCredentials)
	}
	return f.principals[found].Clone(), nil
}

// Bearer sets the request's Authorization header to "Bearer <token>". It is a
// [Credential.Apply] for bearer authenticators.
func Bearer(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) }
