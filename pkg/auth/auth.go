package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Errors an [Authenticator] returns, checked with errors.Is.
var (
	// ErrNoCredentials means the request carries no credential the
	// authenticator reads.
	ErrNoCredentials = errors.New("no credentials")
	// ErrInvalidCredentials means a credential was presented and is wrong:
	// unknown, malformed, or in a form that is not accepted.
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// MaxAuthorizationLength is the longest Authorization header [BearerToken]
// reads, in bytes. A JWT with many claims fits; anything longer is refused.
const MaxAuthorizationLength = 8 << 10

// Principal is who is calling: the tenant it acts for, the user (if the
// credential belongs to one), what it may do and which toolsets it may use.
type Principal struct {
	Tenant tenancy.Tenant
	// User is the acting user's id, "" for a credential that belongs to no
	// user (a service key). It is a label for audit logs.
	User string
	// Scopes are what the principal may do, as path.Match patterns:
	// "agent:run", "admin:*", or "*" for everything.
	Scopes []string
	// Toolsets are the toolsets the principal may use, as names or path.Match
	// patterns ("crm-*"). Empty allows none.
	Toolsets []string
	// Via says which credential authenticated the request, for logs and for
	// checks that depend on its kind. It is never secret.
	Via Credential
}

// Credential names the credential behind a [Principal]. Neither field is
// secret: both appear in logs.
type Credential struct {
	// Kind is the kind of credential, such as [KindAPIKey].
	Kind string
	// ID identifies the credential among those of its kind: the entry id of
	// an API key, never the key.
	ID string
}

// KindAPIKey is the [Credential.Kind] of a principal authenticated by
// [APIKeys].
const KindAPIKey = "api_key"

// AllowsToolset reports whether the principal may use the toolset with the
// given name: whether one of its Toolsets is that name or a pattern matching
// it. A malformed pattern matches nothing.
func (p Principal) AllowsToolset(name string) bool { return anyMatch(p.Toolsets, name) }

// HasScope reports whether the principal holds scope s: whether one of its
// Scopes is s or a pattern matching it. A malformed pattern matches nothing.
func (p Principal) HasScope(s string) bool { return anyMatch(p.Scopes, s) }

func anyMatch(patterns []string, name string) bool {
	for _, pat := range patterns {
		if ok, err := path.Match(pat, name); ok && err == nil {
			return true
		}
	}
	return false
}

// Clone returns a copy of p that shares no memory with it.
func (p Principal) Clone() Principal {
	p.Scopes = slices.Clone(p.Scopes)
	p.Toolsets = slices.Clone(p.Toolsets)
	return p
}

type principalKey struct{}

// From returns the principal of ctx, set by [Middleware], and false if there
// is none. The principal is a copy: changing it changes nothing else.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	return p.Clone(), true
}

// Authenticator tells who sent a request. It returns the principal, or an
// error matching [ErrNoCredentials] or [ErrInvalidCredentials]; any other
// error is internal. It must not change the request, and its errors must not
// contain the credential or anything derived from it. It is called
// concurrently.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// AuthenticatorFunc is a function that is an [Authenticator].
type AuthenticatorFunc func(r *http.Request) (Principal, error)

// Authenticate calls f(r).
func (f AuthenticatorFunc) Authenticate(r *http.Request) (Principal, error) { return f(r) }

// Chain returns an [Authenticator] that tries each of as in order. The first
// principal wins. [ErrNoCredentials] moves on to the next; any other error
// (invalid credentials, an internal error) is returned at once, so a wrong
// credential is never retried as something else. With none left it returns
// ErrNoCredentials.
func Chain(as ...Authenticator) Authenticator { return chain(slices.Clone(as)) }

type chain []Authenticator

func (c chain) Authenticate(r *http.Request) (Principal, error) {
	for _, a := range c {
		p, err := a.Authenticate(r)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, ErrNoCredentials) || errors.Is(err, ErrInvalidCredentials) {
			return Principal{}, err
		}
	}
	return Principal{}, ErrNoCredentials
}

// BearerToken returns the token of the request's "Authorization: Bearer
// <token>" header (RFC 6750). The scheme is matched case-insensitively and may
// be followed by several spaces. With no Authorization header it returns
// [ErrNoCredentials]. It returns [ErrInvalidCredentials] for several
// Authorization headers, one longer than [MaxAuthorizationLength], another
// scheme, and an empty token or one with a byte outside visible ASCII.
func BearerToken(h http.Header) (string, error) {
	vs := h.Values("Authorization")
	switch {
	case len(vs) == 0:
		return "", ErrNoCredentials
	case len(vs) > 1:
		return "", fmt.Errorf("%w: %d Authorization headers", ErrInvalidCredentials, len(vs))
	case len(vs[0]) > MaxAuthorizationLength:
		return "", fmt.Errorf("%w: Authorization header longer than %d bytes", ErrInvalidCredentials, MaxAuthorizationLength)
	}
	scheme, token, ok := strings.Cut(vs[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("%w: not a Bearer credential", ErrInvalidCredentials)
	}
	token = strings.TrimLeft(token, " ")
	if token == "" {
		return "", fmt.Errorf("%w: empty bearer token", ErrInvalidCredentials)
	}
	if !visibleASCII(token) {
		return "", fmt.Errorf("%w: bearer token has a byte outside visible ASCII", ErrInvalidCredentials)
	}
	return token, nil
}

// visibleASCII reports whether s has only the bytes '!' to '~': no space, no
// control character, nothing outside ASCII.
func visibleASCII(s string) bool {
	for i := range len(s) {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}
