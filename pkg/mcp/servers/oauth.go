package servers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// MCP OAuth (T095): a toolset with auth "oauth" signs in to its server
// with an account connected through the agent API's connect flow (package
// credentials/connect, POST /v1/credentials/{name}/connect, then
// /oauth/callback), which [Manager.Dynamic] serves. With scope "user" each
// user connects their own account and every call uses the caller's; with
// "tenant" an admin connects one the tenant shares. The connections are
// kept by the [oauth.Connector] (the credentials vault), under the
// toolset's name as their slot.
//
// The tools are listed through a connection, so an admin connects one to
// review them. An approved tool is offered to every user, connected or
// not, as it was approved: a user who has not connected gets "not
// connected" from its call, never someone else's connection. Once
// connected, the user's own listing is pinned like any other.

// WithOAuth lets toolsets sign in with MCP OAuth (auth "oauth") through
// c, whose HTTP client must be the guard's (every request to an
// authorization server goes through it). Without it auth "oauth" is
// refused.
func WithOAuth(c *oauth.Connector) Option { return func(m *Manager) { m.oauth = c } }

// oauthServer is r's server as package oauth sees it, for the toolset
// name. A pre-registered client's secret is read from its variable now;
// the client is pinned to the issuer found when the toolset was added.
func (m *Manager) oauthServer(name string, r record) (oauth.Server, error) {
	srv := oauth.Server{Name: name, URL: r.URL, Scope: oauth.ScopeTenant}
	if r.Scope == scopeUser {
		srv.Scope = oauth.ScopeUser
	}
	srv.Profile.Issuer = r.Issuer
	if r.OAuthClientID != "" {
		srv.Profile.ClientID = r.OAuthClientID
		if r.OAuthClientSecret != "" {
			v, err := m.expand(r.OAuthClientSecret)
			if err != nil {
				return oauth.Server{}, fmt.Errorf("%w: oauth_client.client_secret: %w", agentapi.ErrServerUnreachable, err)
			}
			srv.Profile.ClientSecret = oauth.NewSecret(v)
		}
	}
	return srv, nil
}

// ownerIn is whose connection a call in ctx uses for a toolset of scope:
// the context's user, or for scope tenant the tenant. A user-scoped toolset
// called with no user (a service key) is not connected.
func ownerIn(ctx context.Context, scope string) (oauth.Owner, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return oauth.Owner{}, err
	}
	o := oauth.Owner{Tenant: t.ID()}
	if scope != scopeUser {
		return o, nil
	}
	if u, ok := tenancy.User(ctx); ok && u != "" {
		o.User = u
		return o, nil
	}
	return oauth.Owner{}, oauth.ErrNoUser
}

// connectionError is why a connection of an OAuth toolset cannot be used
// now: the owner has not connected, must reconnect, or must grant more.
// It is never cached, so a connection made meanwhile is used at once.
type connectionError struct {
	toolset, scope string
	err            error
}

func (e *connectionError) Error() string {
	who := "your account"
	if e.scope != scopeUser {
		who = "the tenant's account (an admin connects it)"
	}
	switch {
	case errors.Is(e.err, oauth.ErrReconnectNeeded):
		return fmt.Sprintf("reconnect needed: %s to %s no longer works; connect it again on the Connections page", who, e.toolset)
	case errors.Is(e.err, oauth.ErrNeedsMoreAccess):
		return fmt.Sprintf("needs more access: connect %s to %s again and grant what it asks for", who, e.toolset)
	case errors.Is(e.err, oauth.ErrNoUser):
		return fmt.Sprintf("not connected: %s needs each user's own account, and this call is not a user's", e.toolset)
	}
	return fmt.Sprintf("not connected: connect %s to %s on the Connections page, then try again", who, e.toolset)
}

func (e *connectionError) Unwrap() error { return e.err }

// asConnection is err as a connectionError, when it is one of a missing
// or broken connection.
func asConnection(err error, toolset, scope string) (*connectionError, bool) {
	var ce *connectionError
	if errors.As(err, &ce) {
		return ce, true
	}
	for _, e := range []error{oauth.ErrNotConnected, oauth.ErrReconnectNeeded, oauth.ErrNeedsMoreAccess, oauth.ErrNoUser} {
		if errors.Is(err, e) {
			return &connectionError{toolset: toolset, scope: scope, err: e}, true
		}
	}
	return nil, false
}

// connected checks, before connecting, that owner has a connection to
// use.
func (m *Manager) connected(ctx context.Context, name string, r record) (oauth.Owner, oauth.Server, error) {
	srv, err := m.oauthServer(name, r)
	if err != nil {
		return oauth.Owner{}, oauth.Server{}, err
	}
	owner, err := ownerIn(ctx, r.Scope)
	if err != nil {
		return oauth.Owner{}, srv, &connectionError{toolset: name, scope: r.Scope, err: err}
	}
	if err := m.oauth.Connected(ctx, owner, srv); err != nil {
		if ce, ok := asConnection(err, name, r.Scope); ok {
			return owner, srv, ce
		}
		return owner, srv, err
	}
	return owner, srv, nil
}

// connectionStatus is the caller's connection to an OAuth toolset, as the
// API shows it.
func (m *Manager) connectionStatus(ctx context.Context, name string, r record) string {
	if r.Auth != wire.AuthOAuth || m.oauth == nil {
		return ""
	}
	_, _, err := m.connected(ctx, name, r)
	switch {
	case err == nil:
		return wire.CredentialConnected
	case errors.Is(err, oauth.ErrReconnectNeeded):
		return wire.CredentialNeedsReconnect
	}
	return wire.CredentialNotConnected
}

// callError is the tool error of a call that cannot use its connection.
func callError(name string, ce *connectionError) error {
	return &tool.Error{Tool: name, Kind: tool.ErrToolFailed, Err: errors.New(ce.Error())}
}

// checkOAuth checks an add request's auth "oauth" into r.
func (m *Manager) checkOAuth(req wire.AddToolsetRequest, r *record) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{agentapi.ErrToolsetInvalid}, args...)...)
	}
	if m.oauth == nil {
		return bad(`auth "oauth" (MCP OAuth) needs the server's public URL (AGENTKIT_PUBLIC_URL) and a credentials vault`)
	}
	if len(req.Headers) > 0 {
		return bad(`auth "oauth" takes no headers`)
	}
	switch r.Transport {
	case "auto", "http":
		r.Transport = "http" // the HTTP+SSE transport has no OAuth
	default:
		return bad(`auth "oauth" needs transport "http" (Streamable HTTP)`)
	}
	if c := req.OAuthClient; c != nil {
		if c.ClientID == "" || len(c.ClientID) > 512 || strings.ContainsAny(c.ClientID, "\r\n\x00") {
			return bad("oauth_client.client_id is empty or not a client id")
		}
		if c.ClientSecret != "" {
			if !fullRefRE.MatchString(c.ClientSecret) {
				return bad(`oauth_client.client_secret must be "${NAME}", a variable the operator allows, never the secret itself`)
			}
			if _, err := m.expand(c.ClientSecret); err != nil {
				return bad("oauth_client.client_secret: %v", err)
			}
		}
		r.OAuthClientID, r.OAuthClientSecret = c.ClientID, c.ClientSecret
	}
	return nil
}

// addOAuth adds a toolset with auth "oauth": its authorization server is
// found (and pinned), but its tools wait for a connection.
func (m *Manager) addOAuth(ctx context.Context, by, name string, r record) (wire.ServerToolset, error) {
	srv, err := m.oauthServer(name, r)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	dctx, cancel := context.WithTimeout(ctx, m.connectTimeout)
	defer cancel()
	iss, err := m.oauth.Issuer(dctx, srv)
	if err != nil {
		return wire.ServerToolset{}, fmt.Errorf("%w: the server's MCP OAuth sign-in cannot be used: %w", agentapi.ErrServerUnreachable, err)
	}
	if u, err := url.Parse(iss); err != nil || u.Host == "" {
		return wire.ServerToolset{}, fmt.Errorf("%w: its authorization server has no usable issuer", agentapi.ErrServerUnreachable)
	}
	r.Issuer = iss
	rec, err := m.save(ctx, name, r, 0)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	t, _ := tenancy.Require(ctx)
	m.log.Info("mcp server added", "tenant", t.ID(), "toolset", name, "server", r.URL, "auth", r.Auth, "scope", r.Scope, "issuer", iss, "by", by)
	cn, err := m.connFor(ctx, name, r)
	return m.show(ctx, rec, r, cn, err), nil
}

// Dynamic is the connect flow of the toolsets with auth "oauth", for the
// agent API's credentials (connect.Config.Dynamic); nil without
// [WithOAuth].
func (m *Manager) Dynamic() connect.Dynamic {
	if m.oauth == nil {
		return nil
	}
	return dynamic{m}
}

type dynamic struct{ m *Manager }

// Slots implements [connect.Dynamic]: the tenant's toolsets with auth
// "oauth", each its own slot.
func (d dynamic) Slots(ctx context.Context) ([]connect.Slot, error) {
	all, err := d.m.store.ListToolsets(ctx)
	if err != nil {
		return nil, err
	}
	var out []connect.Slot
	for _, rec := range all {
		r, err := decode(rec.Data)
		if err != nil || r.Auth != wire.AuthOAuth {
			continue
		}
		scope := credentials.ScopeTenant
		if r.Scope == scopeUser {
			scope = credentials.ScopeUser
		}
		out = append(out, connect.Slot{Name: rec.Name, Scope: scope, Toolsets: []string{rec.Name}})
	}
	return out, nil
}

// RedirectURL implements [connect.Dynamic].
func (d dynamic) RedirectURL() string { return d.m.oauth.RedirectURL() }

// oauthRecord loads the toolset slot names, which must sign in with OAuth.
func (d dynamic) oauthRecord(ctx context.Context, slot string) (record, error) {
	_, r, err := d.m.load(ctx, slot)
	if err != nil {
		return record{}, err
	}
	if r.Auth != wire.AuthOAuth {
		return record{}, fmt.Errorf("%w: toolset %q does not sign in with OAuth", agentapi.ErrCredentialInvalid, slot)
	}
	return r, nil
}

// owner is the owner of a flow for user at a toolset of ctx's tenant.
func owner(ctx context.Context, user string, r record) (oauth.Owner, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return oauth.Owner{}, err
	}
	o := oauth.Owner{Tenant: t.ID(), User: user}
	if (r.Scope == scopeUser) != (user != "") {
		return oauth.Owner{}, fmt.Errorf("%w: toolset's scope is %q", agentapi.ErrCredentialInvalid, r.Scope)
	}
	return o, nil
}

// Begin implements [connect.Dynamic].
func (d dynamic) Begin(ctx context.Context, user, slot, state string) (string, []byte, error) {
	r, err := d.oauthRecord(ctx, slot)
	if err != nil {
		return "", nil, err
	}
	o, err := owner(ctx, user, r)
	if err != nil {
		return "", nil, err
	}
	srv, err := d.m.oauthServer(slot, r)
	if err != nil {
		return "", nil, err
	}
	p, err := d.m.oauth.Start(ctx, o, srv)
	if err != nil {
		return "", nil, fmt.Errorf("%w: toolset %q: signing in cannot start: %w", agentapi.ErrCredentialInvalid, slot, err)
	}
	data, err := oauth.EncodePending(p)
	if err != nil {
		return "", nil, err
	}
	return d.m.oauth.AuthCodeURL(p, state), data, nil
}

// Complete implements [connect.Dynamic]. The flow must be for this
// tenant, user and toolset; the new connection replaces the cached one.
func (d dynamic) Complete(ctx context.Context, user, slot string, data []byte, q url.Values) error {
	p, err := oauth.DecodePending(data)
	if err != nil {
		return err
	}
	r, err := d.oauthRecord(ctx, slot)
	if err != nil {
		return err
	}
	o, err := owner(ctx, user, r)
	if err != nil {
		return err
	}
	if p.Owner != o || p.Server != slot {
		return fmt.Errorf("%w: the flow is another's", oauth.ErrBadState)
	}
	srv, err := d.m.oauthServer(slot, r)
	if err != nil {
		return err
	}
	if err := d.m.oauth.Finish(ctx, p, oauth.CallbackFrom(q), srv); err != nil {
		return err
	}
	d.m.forget(ctx, slot, o.User)
	return nil
}

// forget closes the cached connection of a toolset of ctx's tenant for
// user, so the next use connects with the new token.
func (m *Manager) forget(ctx context.Context, name, user string) {
	k, err := key(ctx, name)
	if err != nil {
		return
	}
	k.user = user
	m.mu.Lock()
	cn := m.conns[k]
	delete(m.conns, k)
	m.mu.Unlock()
	if cn != nil {
		go func() { <-cn.ready; cn.close() }()
	}
}
