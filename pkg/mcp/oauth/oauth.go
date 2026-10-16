package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// Errors the connector returns, checked with errors.Is.
var (
	// ErrNotConnected means the owner has no account connected to the
	// server: the console offers Connect.
	ErrNotConnected = errors.New("not connected")
	// ErrReconnectNeeded means the connection was made but no longer works:
	// the refresh token was refused or the server rejected the token. The
	// console offers Connect again.
	ErrReconnectNeeded = errors.New("reconnect needed")
	// ErrNeedsMoreAccess means the server answered insufficient_scope: the
	// owner connects again to grant more.
	ErrNeedsMoreAccess = errors.New("needs more access")
	// ErrBadState means the callback's state is unknown, used, expired or
	// belongs to another owner.
	ErrBadState = errors.New("unknown or expired state")
	// ErrDenied means the authorization server sent an error to the
	// callback, most often because the user declined.
	ErrDenied = errors.New("authorization denied")
	// ErrNoUser means a user-scoped connection was asked for by a principal
	// that is not a user (a service key).
	ErrNoUser = errors.New("a user connection needs a user")
	// ErrNoRegistration means the authorization server supports none of the
	// ways of registering a client the profile allows.
	ErrNoRegistration = errors.New("no supported client registration")
	// ErrDiscovery means the server's authorization metadata could not be
	// found or was refused (a resource or issuer mismatch, no PKCE).
	ErrDiscovery = errors.New("authorization discovery failed")
	// ErrConflict is returned by [TokenStore.Swap] when the stored token is
	// not the one the caller read.
	ErrConflict = errors.New("token changed")
	// ErrNotFound is returned by the stores for a missing entry.
	ErrNotFound = errors.New("not found")
)

// Secret is a credential value: a token, a refresh token or a client secret.
// It prints, logs and marshals as "[redacted]"; only [Secret.Reveal] gives the
// value, for the one request that needs it.
type Secret struct{ v string }

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{v} }

// Reveal returns the value.
func (s Secret) Reveal() string { return s.v }

// IsZero reports whether s is empty.
func (s Secret) IsZero() bool { return s.v == "" }

const redacted = "[redacted]"

// String redacts, as do the other ways of printing, logging and encoding a
// Secret below, so no such path shows the value.
func (s Secret) String() string { return redacted }

// GoString redacts.
func (s Secret) GoString() string { return redacted }

// Format redacts, whatever the verb.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

// LogValue redacts.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalText redacts, so JSON and other encodings never carry the value.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Scope says who a server's connection belongs to.
type Scope string

const (
	// ScopeUser means each user connects their own account (the default).
	ScopeUser Scope = "user"
	// ScopeTenant means one shared account for the whole tenant, connected by an
	// admin.
	ScopeTenant Scope = "tenant"
)

// Server is an external MCP server that signs users in with OAuth: a
// toolset of type mcp.
type Server struct {
	// Name is the toolset's name. Connections are stored under it.
	Name string
	// URL is the server's MCP endpoint, its canonical resource URI.
	URL string
	// Scope is who a connection belongs to; "" is [ScopeUser].
	Scope Scope
	// Profile is how this server's OAuth differs from the spec. The zero
	// value is the spec ("mcp").
	Profile Profile
}

// Owner is who a connection belongs to: a user of a tenant, or the tenant
// itself (User "") for a [ScopeTenant] server.
type Owner struct {
	Tenant string
	User   string
}

// OwnerFor returns the owner of srv's connection for principal p: p's user,
// or p's tenant for a tenant-scoped server. Who may connect a tenant's shared
// account is the API's decision, not this package's.
func OwnerFor(p auth.Principal, srv Server) (Owner, error) {
	o := Owner{Tenant: p.Tenant.ID()}
	if o.Tenant == "" {
		return Owner{}, errors.New("oauth: principal has no tenant")
	}
	if srv.Scope == ScopeTenant {
		return o, nil
	}
	if p.User == "" {
		return Owner{}, ErrNoUser
	}
	o.User = p.User
	return o, nil
}

// OwnerFrom is [OwnerFor] with the principal of ctx.
func OwnerFrom(ctx context.Context, srv Server) (Owner, error) {
	p, ok := auth.From(ctx)
	if !ok {
		return Owner{}, errors.New("oauth: no principal in context")
	}
	return OwnerFor(p, srv)
}

// Key names one stored connection.
type Key struct {
	Owner
	Server string
}

// Token is a connection: the tokens and what refreshing them needs, so a
// refresh never repeats discovery.
type Token struct {
	Access  Secret
	Refresh Secret
	// Expiry is when Access expires; zero if the provider gave no lifetime
	// and the profile sets none.
	Expiry time.Time
	// Scopes the authorization server granted, or requested if it did not
	// say.
	Scopes []string
	// Extra holds the token response fields the profile keeps
	// ([Profile].KeepExtras), such as Salesforce's instance_url. Not secret.
	Extra map[string]string

	// What refreshing needs.
	TokenURL     string
	ClientID     string
	ClientSecret Secret
	AuthStyle    int // oauth2.AuthStyle
	Resource     string
	Issuer       string

	// ReconnectNeeded is set when the refresh token was refused or the
	// server rejected the token: Token returns [ErrReconnectNeeded] until
	// the owner connects again.
	ReconnectNeeded bool
	// Version is the store's, for [TokenStore.Swap]; callers don't set it.
	Version int64
}
