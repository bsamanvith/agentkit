package servers_test

// MCP OAuth for toolsets added at run time (T095, and T096's "Scopes"):
// a fake authorization server, an SDK server behind it, the agent API's
// connect flow over the vault, and a browser that keeps the binding cookie.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth/oauthtest"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// protected is an SDK server behind a fake authorization server: whoami
// answers with a hash of the bearer token it was called with.
type protected struct {
	as    *oauthtest.AuthServer
	srv   *httptest.Server
	url   string
	calls atomic.Int64
}

func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:8])
}

func newProtected(t *testing.T, opts oauthtest.AuthServerOptions) *protected {
	t.Helper()
	p := &protected{as: oauthtest.NewAuthServer(t, opts)}
	s := mcp.NewServer(&mcp.Implementation{Name: "crm", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", Description: "Who the token is.", InputSchema: object}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p.calls.Add(1)
		tok, _ := strings.CutPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "token " + tokenHash(tok)}}}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	p.srv = oauthtest.Protect(t, p.as, "/mcp", h, oauthtest.ResourceOptions{Scopes: []string{"crm.read"}})
	p.url = p.srv.URL + "/mcp"
	p.as.SetResource(p.url)
	t.Cleanup(func() { p.srv.CloseClientConnections() })
	return p
}

type oauthRig struct {
	*rig
	p     *protected
	vault credentials.Vault
	svc   *connect.Service
	cb    *httptest.Server
	conn  *oauth.Connector
	clk   *clocktest.Fake
}

func newOAuthRig(t *testing.T) *oauthRig {
	t.Helper()
	o := &oauthRig{p: newProtected(t, oauthtest.AuthServerOptions{DCR: true, Scopes: []string{"crm.read", "offline_access"}, TTL: time.Hour})}
	o.clk = clocktest.NewFake(time.Now())
	cb := http.NotFoundHandler()
	o.cb = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cb.ServeHTTP(w, r) }))
	t.Cleanup(o.cb.Close)
	ring, err := credentials.ParseKeyRing("k1:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
	if err != nil {
		t.Fatal(err)
	}
	blobs := memory.New()
	o.vault = credentials.NewVault(blobs, credentials.NewSealer(ring))
	store := oauth.NewVaultStore(o.vault)
	g, err := netguard.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if o.conn, err = oauth.New(oauth.Config{PublicURL: o.cb.URL, Tokens: store, Clients: store, HTTPClient: g.Client(nil), Clock: o.clk}); err != nil {
		t.Fatal(err)
	}
	o.rig = newRig(t, servers.WithOAuth(o.conn))
	if o.svc, err = connect.New(connect.Config{
		Vault: o.vault, Store: blobs, Sealer: credentials.NewSealer(ring), Dynamic: o.m.Dynamic(), Clock: o.clk,
	}); err != nil {
		t.Fatal(err)
	}
	cb = o.svc.Callback()
	return o
}

func userCtx(t *testing.T, tenant, user string) context.Context {
	t.Helper()
	ctx := ctxOf(t, tenant)
	if user != "" {
		ctx = tenancy.WithUser(ctx, user)
	}
	return ctx
}

// connectAs runs the connect flow for user ("" for the tenant's) in its
// browser: the API's connect, the authorization server, the callback.
func (o *oauthRig) connectAs(t *testing.T, tenant, user, slot string) {
	t.Helper()
	resp, cookie, err := o.svc.Connect(userCtx(t, tenant, user), user, slot, wire.ConnectRequest{})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := noFollow.Get(resp.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d", r.StatusCode)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, r.Header.Get("Location"), nil)
	req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	r, err = noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("callback: %d", r.StatusCode)
	}
}

// tokenOf is the hash of the access token stored for user.
func (o *oauthRig) tokenOf(t *testing.T, tenant, user, slot string) string {
	t.Helper()
	c, _, err := o.vault.Get(ctxOf(t, tenant), user, slot)
	if err != nil {
		t.Fatal(err)
	}
	return tokenHash(c.(credentials.OAuth2).AccessToken.Reveal())
}

func (o *oauthRig) addOAuth(ctx context.Context, t *testing.T, name, scope string) wire.ServerToolset {
	t.Helper()
	ts, err := o.m.Add(ctx, "key-admin", wire.AddToolsetRequest{Type: "mcp", Name: name, URL: o.p.url, Auth: wire.AuthOAuth, Scope: scope})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return ts
}

func invoke(ctx context.Context, reg tool.Registry, name string) (string, error) {
	out, err := reg.Invoke(ctx, name, json.RawMessage(`{}`))
	return string(out), err
}

// Criterion (T096 Scopes): a user-scoped server uses each user's own
// connection; a user who has not connected gets "not connected" as a tool
// error, never another user's token.
func TestOAuthUserScope(t *testing.T) {
	o := newOAuthRig(t)
	admin := userCtx(t, "acme", "admin")
	ts := o.addOAuth(admin, t, "crm", "user")
	if ts.Auth != wire.AuthOAuth || ts.Scope != "user" || ts.Transport != "http" || ts.Issuer != o.p.as.URL ||
		ts.Connection != wire.CredentialNotConnected || ts.Status != wire.ToolsetUnavailable || !strings.Contains(ts.Reason, "not connected") {
		t.Fatalf("added: %+v", ts)
	}
	if _, err := o.m.Review(admin, "crm", "key-admin", wire.ReviewRequest{Tools: []wire.ToolDecision{{Name: "whoami", Decision: "approve"}}}); !errors.Is(err, agentapi.ErrAuthRequired) {
		t.Fatalf("reviewing before connecting: %v, want ErrAuthRequired", err)
	}
	list, err := o.svc.List(userCtx(t, "acme", "alice"), "alice")
	if err != nil || len(list) != 1 || list[0].Slot != "crm" || list[0].Scope != "user" || !list[0].OAuth || list[0].Status != wire.CredentialNotConnected {
		t.Fatalf("alice's credentials: %+v, %v", list, err)
	}

	// The admin connects their own account, which lists the tools to review.
	o.connectAs(t, "acme", "admin", "crm")
	ts, err = o.m.Get(admin, "crm")
	if err != nil || ts.Connection != wire.CredentialConnected || ts.Status != wire.ToolsetReviewNeeded || ts.Server.Name != "crm" {
		t.Fatalf("after the admin connects: %+v, %v", ts, err)
	}
	o.decide(admin, "crm", approve(toolByName(t, ts, "whoami")))
	reg := o.m.Registry("crm")
	adminTok := o.tokenOf(t, "acme", "admin", "crm")
	if out, err := invoke(admin, reg, "crm__whoami"); err != nil || !strings.Contains(out, adminTok) {
		t.Fatalf("admin's call: %s, %v", out, err)
	}

	// alice has not connected: offered the approved tool, and its call is
	// "not connected", reaching no server.
	alice := userCtx(t, "acme", "alice")
	if n := listNames(alice, t, reg); len(n) != 1 || n[0] != "crm__whoami" {
		t.Fatalf("alice is offered %v", n)
	}
	before := o.p.calls.Load()
	out, err := invoke(alice, reg, "crm__whoami")
	var te *tool.Error
	if !errors.As(err, &te) || !errors.Is(err, tool.ErrToolFailed) || !strings.Contains(err.Error(), "not connected") || strings.Contains(out, adminTok) {
		t.Fatalf("alice's call: %q, %v", out, err)
	}
	if o.p.calls.Load() != before {
		t.Fatal("alice's call reached the server")
	}
	if ts, _ := o.m.Get(alice, "crm"); ts.Connection != wire.CredentialNotConnected {
		t.Errorf("alice's connection: %s", ts.Connection)
	}
	// A key with no user is nobody's connection.
	if _, err := invoke(ctxOf(t, "acme"), reg, "crm__whoami"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("a call with no user: %v", err)
	}

	// alice connects: her calls use her token, the admin's still theirs.
	o.connectAs(t, "acme", "alice", "crm")
	aliceTok := o.tokenOf(t, "acme", "alice", "crm")
	if aliceTok == adminTok {
		t.Fatal("alice got the admin's token")
	}
	if out, err := invoke(alice, reg, "crm__whoami"); err != nil || !strings.Contains(out, aliceTok) {
		t.Fatalf("alice's call after connecting: %s, %v", out, err)
	}
	if out, err := invoke(admin, reg, "crm__whoami"); err != nil || !strings.Contains(out, adminTok) {
		t.Fatalf("admin's call: %s, %v", out, err)
	}
	if out, err := invoke(userCtx(t, "acme", "bob"), reg, "crm__whoami"); err == nil || strings.Contains(out, "token") {
		t.Fatalf("bob's call: %s, %v", out, err)
	}

	// The same user id in another tenant is another owner.
	other := userCtx(t, "globex", "alice")
	o.addOAuth(other, t, "crm", "user")
	if _, err := invoke(other, reg, "crm__whoami"); err == nil {
		t.Fatal("globex's alice used acme's alice's connection")
	}

	// Disconnecting (DELETE /v1/credentials/crm) is not connected at once.
	if err := o.svc.Delete(alice, "alice", "crm"); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(alice, reg, "crm__whoami"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("after disconnecting: %v", err)
	}
}

// invalid_grant on refresh: "reconnect needed", as the tool error and the
// connection's status; connecting again fixes it.
func TestOAuthReconnect(t *testing.T) {
	o := newOAuthRig(t)
	alice := userCtx(t, "acme", "alice")
	o.addOAuth(alice, t, "crm", "user")
	o.connectAs(t, "acme", "alice", "crm")
	ts, _ := o.m.Get(alice, "crm")
	o.decide(alice, "crm", approve(toolByName(t, ts, "whoami")))
	reg := o.m.Registry("crm")
	if _, err := invoke(alice, reg, "crm__whoami"); err != nil {
		t.Fatal(err)
	}
	o.p.as.RevokeRefresh()
	o.p.as.RevokeAccess()
	o.clk.Advance(2 * time.Hour)
	_, err := invoke(alice, reg, "crm__whoami")
	if err == nil || !strings.Contains(err.Error(), "reconnect needed") {
		t.Fatalf("after invalid_grant: %v", err)
	}
	if ts, _ := o.m.Get(alice, "crm"); ts.Connection != wire.CredentialNeedsReconnect {
		t.Errorf("connection = %s", ts.Connection)
	}
	if st, _ := o.svc.Status(alice, "alice", "crm"); st != wire.CredentialNeedsReconnect {
		t.Errorf("credential status = %s", st)
	}
	o.connectAs(t, "acme", "alice", "crm")
	if out, err := invoke(alice, reg, "crm__whoami"); err != nil || !strings.Contains(out, o.tokenOf(t, "acme", "alice", "crm")) {
		t.Fatalf("after reconnecting: %s, %v", out, err)
	}
}

// Scope tenant: an admin connects the tenant's account; every user's call
// uses it.
func TestOAuthTenantScope(t *testing.T) {
	o := newOAuthRig(t)
	admin := userCtx(t, "acme", "admin")
	o.addOAuth(admin, t, "crm", "tenant")
	if _, _, err := o.svc.Connect(admin, "admin", "crm", wire.ConnectRequest{}); err == nil {
		t.Fatal("a user connected the tenant's account as their own")
	}
	o.connectAs(t, "acme", "", "crm")
	ts, _ := o.m.Get(admin, "crm")
	o.decide(admin, "crm", approve(toolByName(t, ts, "whoami")))
	tenantTok := o.tokenOf(t, "acme", "", "crm")
	reg := o.m.Registry("crm")
	for _, u := range []string{"alice", "bob", ""} {
		if out, err := invoke(userCtx(t, "acme", u), reg, "crm__whoami"); err != nil || !strings.Contains(out, tenantTok) {
			t.Fatalf("%q's call: %s, %v", u, out, err)
		}
	}
	if _, err := invoke(userCtx(t, "globex", "alice"), reg, "crm__whoami"); !errors.Is(err, tool.ErrNotFound) {
		t.Fatalf("another tenant: %v", err)
	}
}

// A toolset removed and added again under its name, at another server:
// the connections kept under the name are never sent to it.
func TestOAuthReAdded(t *testing.T) {
	o := newOAuthRig(t)
	alice := userCtx(t, "acme", "alice")
	o.addOAuth(alice, t, "crm", "user")
	o.connectAs(t, "acme", "alice", "crm")
	if err := o.m.Delete(alice, "crm"); err != nil {
		t.Fatal(err)
	}
	other := newProtected(t, oauthtest.AuthServerOptions{DCR: true, Scopes: []string{"offline_access"}})
	ts, err := o.m.Add(alice, "k", wire.AddToolsetRequest{Type: "mcp", Name: "crm", URL: other.url, Auth: wire.AuthOAuth, Scope: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if ts.Connection != wire.CredentialNotConnected || other.calls.Load() != 0 {
		t.Fatalf("the old connection counts for the new server: %+v", ts)
	}
}

func TestOAuthAddErrors(t *testing.T) {
	o := newOAuthRig(t)
	ctx := userCtx(t, "acme", "admin")
	plain := newFake(t, nil)
	for _, c := range []struct {
		name string
		req  wire.AddToolsetRequest
		want error
	}{
		{"user scope without oauth", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Scope: "user"}, agentapi.ErrToolsetInvalid},
		{"sse", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth", Transport: "sse"}, agentapi.ErrToolsetInvalid},
		{"headers", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth", Headers: map[string]string{"X-A": "b"}}, agentapi.ErrToolsetInvalid},
		{"a client secret as is", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth", OAuthClient: &wire.OAuthClient{ClientID: "c", ClientSecret: "s3cret"}}, agentapi.ErrToolsetInvalid},
		{"a client secret variable not allowed", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth", OAuthClient: &wire.OAuthClient{ClientID: "c", ClientSecret: "${HOME}"}}, agentapi.ErrToolsetInvalid},
		{"no client id", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth", OAuthClient: &wire.OAuthClient{}}, agentapi.ErrToolsetInvalid},
		{"oauth_client without oauth", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, OAuthClient: &wire.OAuthClient{ClientID: "c"}}, agentapi.ErrToolsetInvalid},
		{"a server without OAuth", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: plain.ts.URL, Auth: "oauth"}, agentapi.ErrServerUnreachable},
	} {
		if _, err := o.m.Add(ctx, "k", c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// An OAuth server added without auth: 422 auth_required, saying OAuth.
	_, err := o.m.Add(ctx, "k", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url})
	var are *agentapi.AuthRequiredError
	if !errors.As(err, &are) || are.Auth != wire.AuthOAuth || !strings.Contains(err.Error(), `auth "oauth"`) {
		t.Fatalf("an OAuth server without auth: %v", err)
	}
	// Without WithOAuth, auth oauth is refused, and the 401 says what is missing.
	r := newRig(t)
	if _, err := r.m.Add(ctx, "k", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url, Auth: "oauth"}); !errors.Is(err, agentapi.ErrToolsetInvalid) || !strings.Contains(err.Error(), "AGENTKIT_PUBLIC_URL") {
		t.Errorf("oauth without WithOAuth: %v", err)
	}
	if r.m.Dynamic() != nil {
		t.Error("Dynamic without WithOAuth")
	}
	_, err = r.m.Add(ctx, "k", wire.AddToolsetRequest{Type: "mcp", Name: "a", URL: o.p.url})
	if !errors.As(err, &are) || are.Auth != wire.AuthOAuth {
		t.Errorf("an OAuth server, no OAuth here: %v", err)
	}
	if all, _ := o.st.ListToolsets(ctx); len(all) != 0 {
		t.Fatalf("failed adds stored %d toolsets", len(all))
	}
}

// The connect flow hands back the sealed flow it began; Complete still
// checks it is the caller's and the toolset's, so a flow begun by alice,
// or for another toolset, never stores a connection for bob or for this
// one.
func TestOAuthDynamicComplete(t *testing.T) {
	o := newOAuthRig(t)
	alice, bob := userCtx(t, "acme", "alice"), userCtx(t, "acme", "bob")
	o.addOAuth(alice, t, "crm", "user")
	o.addOAuth(alice, t, "crm2", "user")
	d := o.m.Dynamic()
	authURL, data, err := d.Begin(alice, "alice", "crm", "state-1")
	if err != nil {
		t.Fatal(err)
	}
	// The browser signs in at the authorization server, which redirects
	// back with a code.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(authURL) //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if err := d.Complete(bob, "bob", "crm", data, q); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("bob completing alice's flow: %v", err)
	}
	if err := d.Complete(alice, "alice", "crm2", data, q); !errors.Is(err, oauth.ErrBadState) {
		t.Errorf("alice's crm flow completing crm2: %v", err)
	}
	for _, who := range []struct {
		ctx        context.Context
		user, slot string
	}{{bob, "bob", "crm"}, {alice, "alice", "crm2"}} {
		if ts, _ := o.m.Get(who.ctx, who.slot); ts.Connection != wire.CredentialNotConnected {
			t.Errorf("%s's %s: %s", who.user, who.slot, ts.Connection)
		}
	}
	if err := d.Complete(alice, "alice", "crm", data, q); err != nil {
		t.Fatalf("alice completing her flow: %v", err)
	}
	if ts, _ := o.m.Get(alice, "crm"); ts.Connection != wire.CredentialConnected {
		t.Errorf("alice's crm: %s", ts.Connection)
	}
}
