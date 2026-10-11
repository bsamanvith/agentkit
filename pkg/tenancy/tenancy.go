package tenancy

import (
	"context"
	"errors"
	"fmt"
)

// Errors returned by this package, checked with [errors.Is].
var (
	// ErrNoTenant means the context carries no tenant.
	ErrNoTenant = errors.New("no tenant in context")
	// ErrInvalidTenant means a tenant id is empty, too long, or has characters
	// outside the allowed set.
	ErrInvalidTenant = errors.New("invalid tenant id")
)

// MaxIDLength is the longest tenant id, in bytes.
const MaxIDLength = 64

// Tenant identifies one customer of the deployment. The zero Tenant is not
// valid: make one with [NewTenant]. Tenants can be compared with ==.
type Tenant struct{ id string }

// NewTenant returns the tenant with the given id. An id has 1 to [MaxIDLength]
// bytes, each an ASCII letter, digit, '.', '_' or '-', and starts with a
// letter or digit. That makes it safe as a key, a file or URL path segment and
// a metric label, and rules out "." and "..".
func NewTenant(id string) (Tenant, error) {
	if len(id) == 0 || len(id) > MaxIDLength {
		return Tenant{}, fmt.Errorf("%w: length %d, want 1 to %d", ErrInvalidTenant, len(id), MaxIDLength)
	}
	for i := range len(id) {
		c := id[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return Tenant{}, fmt.Errorf("%w: byte %d is not allowed here (letters and digits, then also . _ -)", ErrInvalidTenant, i)
		}
	}
	return Tenant{id: id}, nil
}

// ID returns the tenant's id, "" for the zero Tenant.
func (t Tenant) ID() string { return t.id }

// scope is everything the context carries. It is immutable: every With*
// function stores a new one, so contexts derived from each other never share
// or change a scope.
type scope struct {
	tenant  Tenant
	user    string
	request string
}

type scopeKey struct{}

func scopeOf(ctx context.Context) scope {
	s, _ := ctx.Value(scopeKey{}).(scope)
	return s
}

// With returns a context for tenant t. It starts a new scope: a user and a
// request id set for another tenant do not carry over. With the zero Tenant it
// clears the scope, so [From] reports no tenant.
func With(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{tenant: t})
}

// From returns the tenant of ctx, and false if there is none.
func From(ctx context.Context) (Tenant, bool) {
	t := scopeOf(ctx).tenant
	return t, t != Tenant{}
}

// Require returns the tenant of ctx, or an error matching [ErrNoTenant]. Code
// that serves requests (a store method, say) uses it to refuse a context with
// no tenant.
func Require(ctx context.Context) (Tenant, error) {
	t, ok := From(ctx)
	if !ok {
		return Tenant{}, ErrNoTenant
	}
	return t, nil
}

// MustFrom returns the tenant of ctx and panics with [ErrNoTenant] if there is
// none. Use it only where a missing tenant is a programming error.
func MustFrom(ctx context.Context) Tenant {
	t, ok := From(ctx)
	if !ok {
		panic(ErrNoTenant)
	}
	return t
}

// WithUser returns a context that also carries the id of the acting user. The
// id is an opaque label for audit logs; it is not checked.
func WithUser(ctx context.Context, id string) context.Context {
	s := scopeOf(ctx)
	s.user = id
	return context.WithValue(ctx, scopeKey{}, s)
}

// User returns the user id of ctx, and false if none was set.
func User(ctx context.Context) (string, bool) {
	u := scopeOf(ctx).user
	return u, u != ""
}

// WithRequest returns a context that also carries a request id, an opaque
// label for correlating logs.
func WithRequest(ctx context.Context, id string) context.Context {
	s := scopeOf(ctx)
	s.request = id
	return context.WithValue(ctx, scopeKey{}, s)
}

// Request returns the request id of ctx, and false if none was set.
func Request(ctx context.Context) (string, bool) {
	r := scopeOf(ctx).request
	return r, r != ""
}
